// Package alert watches the snapshot and pushes notifications when servers or
// probes go down and come back, and when an AI balance runs low.
package alert

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/okchum/mantel-kit/conf"
	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/jpath"
	"github.com/okchum/mantel/internal/plans"
	"github.com/okchum/mantel/internal/state"
)

// Message is one notification.
type Message struct {
	Title string
	Body  string
	Level string // Bark levels: active, timeSensitive, passive, critical; "" = notifier default
}

type Notifier interface {
	Notify(ctx context.Context, m Message) error
}

// Bark posts to a Bark server (https://github.com/Finb/bark-server).
type Bark struct {
	Cfg    config.Bark
	URL    string // opened when the notification is tapped
	Client *http.Client
}

func (b *Bark) Notify(ctx context.Context, m Message) error {
	body := map[string]any{
		"device_key": b.Cfg.Key,
		"title":      m.Title,
		"body":       m.Body,
		"group":      b.Cfg.Group,
	}
	if level := cmp.Or(m.Level, b.Cfg.Level); level != "" {
		body["level"] = level
	}
	for k, v := range map[string]string{"sound": b.Cfg.Sound, "icon": b.Cfg.Icon, "url": b.URL} {
		if v != "" {
			body[k] = v
		}
	}
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(b.Cfg.Server, "/")+"/push", bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	c := b.Client
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	out, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	var r struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(out, &r)
	if res.StatusCode != http.StatusOK || (r.Code != 0 && r.Code != 200) {
		return fmt.Errorf("bark: HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(out)))
	}
	return nil
}

// track is the alert state of one watched thing.
type track struct {
	badSince time.Time // zero while healthy
	notified bool      // a "down" message went out for the current incident
	detail   string
}

// Engine compares successive snapshots and decides what to send.
type Engine struct {
	Store    *state.Store
	Notifier Notifier
	After    time.Duration // how long something must stay bad before alerting
	Servers  []config.Server
	Probes   []config.Probe
	Quotas   []config.Quota
	Sources  []config.Source // their alert rules
	Plans    *config.Plans   // subscription windows and expiring resets
	Every    time.Duration   // check cadence; default 15s
	// Maintenance windows ("HH:MM-HH:MM" in Loc): outages inside them are
	// expected (a scheduled router reboot) and "down" is not reported; the
	// After delay counts from the window's end.
	Maintenance []string
	Loc         *time.Location // default time.Local
	Now         func() time.Time
	tracks      map[string]*track
	pendingUp   []upLine  // recovery lines that failed to send, retried next round
	failures    int       // consecutive failed sends
	retryAt     time.Time // no sends before this while the notifier keeps failing
}

// upLine is a recovery line and the track key it reports on.
type upLine struct{ key, line string }

// Backoff after failed sends: 15s, 30s, 1m, ... up to maxBackoff.
const (
	minBackoff = 15 * time.Second
	maxBackoff = 10 * time.Minute
)

// send delivers m unless a previous failure asked to back off, and reports
// whether it went out.
func (e *Engine) send(ctx context.Context, now time.Time, m Message) bool {
	if now.Before(e.retryAt) {
		return false
	}
	if err := e.Notifier.Notify(ctx, m); err != nil {
		e.failures++
		wait := maxBackoff
		if e.failures <= 6 { // 15s << 6 already exceeds maxBackoff
			wait = min(minBackoff<<(e.failures-1), maxBackoff)
		}
		e.retryAt = now.Add(wait)
		log.Printf("alert: %v (next try in %s)", err, wait)
		return false
	}
	e.failures, e.retryAt = 0, time.Time{}
	log.Printf("alert: sent %q", m.Title)
	return true
}

func (e *Engine) Run(ctx context.Context) {
	every := e.Every
	if every <= 0 {
		every = 15 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.Check(ctx, e.Store.Read())
		}
	}
}

type finding struct {
	key, label, detail string
	bad, watched       bool
	kind               string        // "down", "low" or "rule"
	after              time.Duration // rules: own delay; 0 = Engine.After
}

func (e *Engine) findings(s state.Snapshot) []finding {
	var out []finding
	for i, srv := range s.Servers {
		if i >= len(e.Servers) || e.Servers[i].Mute {
			continue
		}
		// Up is false before the first scrape too; only an error means down.
		out = append(out, finding{key: "server:" + srv.Name, label: srv.Name, kind: "down",
			watched: srv.Up || srv.Error != "", bad: !srv.Up && srv.Error != "", detail: srv.Error})
	}
	for i, p := range s.Probes {
		if i >= len(e.Probes) || e.Probes[i].Mute {
			continue
		}
		detail := p.Error
		if detail == "" && p.Status != 0 {
			detail = fmt.Sprintf("HTTP %d", p.Status)
		}
		out = append(out, finding{key: "probe:" + p.Name, label: p.Name, kind: "down",
			watched: p.Checked, bad: p.Checked && !p.Up, detail: detail})
	}
	for i, q := range s.Quotas {
		if i >= len(e.Quotas) || e.Quotas[i].AlertBelow <= 0 || q.Updated.IsZero() || q.Error != "" {
			continue
		}
		left := q.Value
		if q.Mode == "used" {
			if q.Limit <= 0 {
				continue // nothing to compare against
			}
			left = q.Limit - q.Value
		}
		th := e.Quotas[i].AlertBelow
		out = append(out, finding{key: "quota:" + q.Name, label: q.Name, kind: "low", watched: true,
			bad: left < th, detail: fmt.Sprintf("剩 %s%s，低于 %s%s", q.Unit, num(left), q.Unit, num(th))})
	}
	for _, src := range e.Sources {
		i := s.SourceIndex(src.Name)
		if i < 0 {
			continue
		}
		cur := s.Sources[i]
		for j, r := range src.Alerts {
			f := finding{key: fmt.Sprintf("rule:%s:%d", src.Name, j), label: r.Title, kind: "rule", after: r.After.Duration}
			if f.label == "" {
				f.label = src.Name + " " + r.Path
			}
			if cur.Updated.IsZero() || cur.Error != "" {
				out = append(out, f) // unknown: keep the track, don't judge
				continue
			}
			f.watched = true
			f.bad, f.detail = EvalRule(r, cur.Data)
			out = append(out, f)
		}
	}
	out = append(out, e.planFindings(s)...)
	return out
}

// Check runs one round. Exported so tests can drive it with a fake clock.
func (e *Engine) Check(ctx context.Context, s state.Snapshot) {
	now := time.Now()
	if e.Now != nil {
		now = e.Now()
	}
	groups, up := e.judge(now, s)
	e.flushGroups(ctx, now, groups)
	e.flushRecoveries(ctx, now, up)
}

// group is one message per kind; it remembers the tracks it reports, so a
// send that went out is not repeated when another kind's send fails.
type group struct {
	what, level string
	lines       []string
	tracks      []*track
}

// judge updates the tracks from this round's findings and returns the
// "bad" lines due now, grouped by kind, and the new recovery lines.
func (e *Engine) judge(now time.Time, s state.Snapshot) ([]*group, []upLine) {
	if e.tracks == nil {
		e.tracks = map[string]*track{}
	}
	down, low, rules, plan := &group{what: "离线"}, &group{what: "额度不足", level: "active"},
		&group{what: "提醒", level: "active"}, &group{what: "订阅用量", level: "active"}
	byKind := map[string]*group{"plan": plan, "rule": rules, "low": low}
	var up []upLine
	seen := map[string]bool{}
	for _, f := range e.findings(s) {
		seen[f.key] = true
		tr := e.tracks[f.key]
		if tr == nil {
			tr = &track{}
			e.tracks[f.key] = tr
		}
		if !f.watched {
			continue
		}
		if f.bad {
			if e.due(now, f, tr) {
				g := cmp.Or(byKind[f.kind], down)
				line := f.label
				if f.detail != "" {
					line += "：" + f.detail
				}
				g.lines = append(g.lines, line)
				g.tracks = append(g.tracks, tr)
			}
			continue
		}
		if u, ok := recovery(now, f, tr); ok {
			up = append(up, u)
		}
		tr.badSince, tr.notified, tr.detail = time.Time{}, false, ""
	}
	for k := range e.tracks {
		if !seen[k] {
			delete(e.tracks, k)
		}
	}
	return []*group{down, low, rules, plan}, up
}

// due records a bad finding on its track and reports whether it is to be
// reported now: not yet notified, outside maintenance, and bad long enough.
func (e *Engine) due(now time.Time, f finding, tr *track) bool {
	if tr.badSince.IsZero() {
		tr.badSince = now
	}
	tr.detail = f.detail
	if tr.notified {
		return false
	}
	// Low balance is not flaky like a network check: report it at once.
	if f.kind == "low" || f.kind == "plan" {
		return true
	}
	after := e.After
	if f.kind == "rule" {
		after = f.after
	}
	since := tr.badSince
	if f.kind == "down" {
		var quiet bool
		if quiet, since = e.maintenance(now, since); quiet {
			return false
		}
	}
	return now.Sub(since) >= after
}

// recovery is the "all clear" line for a finding that is fine again after a
// reported incident.
func recovery(now time.Time, f finding, tr *track) (upLine, bool) {
	// A window that rolled over or was reset needs no "all clear".
	if !tr.notified || f.kind == "plan" {
		return upLine{}, false
	}
	switch f.kind {
	case "rule":
		return upLine{f.key, f.label + " 已恢复正常"}, true
	case "low":
		return upLine{f.key, f.label + " 已充值"}, true
	}
	return upLine{f.key, fmt.Sprintf("%s 已恢复（中断 %s）", f.label, human(now.Sub(tr.badSince)))}, true
}

// flushGroups sends one message per non-empty group and marks its tracks
// notified when it went out (otherwise they are retried next round).
func (e *Engine) flushGroups(ctx context.Context, now time.Time, groups []*group) {
	for _, g := range groups {
		if len(g.lines) > 0 && e.send(ctx, now, compose(g.what, g.lines, g.level)) {
			for _, tr := range g.tracks {
				tr.notified = true
			}
		}
	}
}

// flushRecoveries sends the recoveries still queued from failed sends plus
// this round's in one message, and queues them again if that fails.
func (e *Engine) flushRecoveries(ctx context.Context, now time.Time, up []upLine) {
	// A queued recovery whose target has gone bad again would be a lie now.
	// Drop it, but the phone still shows the earlier "down": count that as
	// notified, so the next recovery is reported even if this relapse ends
	// inside the grace period.
	var all []upLine
	for _, u := range e.pendingUp {
		tr := e.tracks[u.key]
		if tr == nil || tr.badSince.IsZero() {
			all = append(all, u)
		} else {
			tr.notified = true
		}
	}
	all = append(all, up...)
	e.pendingUp = nil
	lines := make([]string, len(all))
	for i, u := range all {
		lines[i] = u.line
	}
	if len(all) > 0 && !e.send(ctx, now, compose("恢复", lines, "active")) {
		e.pendingUp = all
	}
}

// maintenance reports whether now is inside a maintenance window and, if
// not, when an outage that started at since counts as starting: the end of
// the latest window occurrence it overlapped, so the grace period runs after it.
func (e *Engine) maintenance(now, since time.Time) (bool, time.Time) {
	loc := e.Loc
	if loc == nil {
		loc = time.Local
	}
	for _, w := range e.Maintenance {
		start, end, err := conf.ParseWindow(w)
		if err != nil || start == end {
			continue
		}
		for _, o := range occurrences(start, end, now.In(loc)) {
			if !now.Before(o.start) && now.Before(o.end) {
				return true, since
			}
			if !o.end.After(now) && o.end.After(since) {
				since = o.end
			}
		}
	}
	return false, since
}

// span is one occurrence of a daily window, as absolute instants.
type span struct{ start, end time.Time }

// occurrences returns the window's occurrences that start on local's date and
// the day before; they are the only ones that can contain local or be the
// latest to have ended before it. start and end are minutes since midnight;
// end <= start wraps past midnight. A DST change can stretch or shorten an
// occurrence: it runs from the first time the clock reads start to the last
// time it reads end, and a time the clock skips is the instant it jumps over it.
func occurrences(start, end int, local time.Time) []span {
	y, m, today := local.Date()
	var out []span
	for _, d := range []int{today - 1, today} {
		endDay := d
		if end <= start {
			endDay++
		}
		o := span{clockAt(y, m, d, start, local.Location(), false), clockAt(y, m, endDay, end, local.Location(), true)}
		if o.end.After(o.start) { // a window lost wholly to a DST gap is empty
			out = append(out, o)
		}
	}
	return out
}

// clockAt returns when the clock in loc reads hm (minutes since midnight) on
// the date y-m-d (normalized like time.Date). A reading that DST repeats is
// its first instant, or its last with latest; one that DST skips is the
// instant the clock jumps over it.
func clockAt(y int, m time.Month, d, hm int, loc *time.Location, latest bool) time.Time {
	want := time.Date(y, m, d, hm/60, hm%60, 0, 0, time.UTC)
	t := time.Date(y, m, d, hm/60, hm%60, 0, 0, loc)
	var hit time.Time
	// Each offset in force around t gives one candidate reading.
	for _, near := range []time.Time{t.Add(-24 * time.Hour), t, t.Add(24 * time.Hour)} {
		_, off := near.Zone()
		c := want.Add(-time.Duration(off) * time.Second).In(loc)
		if !wallClock(c).Equal(want) {
			continue
		}
		if hit.IsZero() || latest && c.After(hit) || !latest && c.Before(hit) {
			hit = c
		}
	}
	if !hit.IsZero() {
		return hit
	}
	// Skipped: time.Date moved t to one side of the jump.
	zoneStart, zoneEnd := t.ZoneBounds()
	if wallClock(t).After(want) {
		return zoneStart
	}
	return zoneEnd
}

// wallClock is t's clock reading as a UTC time, for comparing readings.
func wallClock(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// compose folds several lines into one message: a burst (e.g. the uplink drops
// and every probe fails) should buzz the phone once, not five times.
func compose(what string, lines []string, level string) Message {
	sort.Strings(lines)
	if len(lines) == 1 {
		name, rest, _ := strings.Cut(lines[0], "：")
		if what == "恢复" {
			return Message{Title: "恢复", Body: lines[0], Level: level}
		}
		return Message{Title: name + " " + what, Body: rest, Level: level}
	}
	return Message{Title: fmt.Sprintf("%d 项%s", len(lines), what), Body: strings.Join(lines, "\n"), Level: level}
}

func human(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d 小时 %d 分", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%d 天", int(d.Hours()/24))
}

func num(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return s
}

// EvalRule checks one source alert rule against a document; it returns
// whether the rule is violated and a human description of the value.
func EvalRule(r config.Rule, doc any) (bool, string) {
	v, err := jpath.Get(doc, r.Path)
	if err != nil {
		return false, ""
	}
	if r.Not != nil {
		got := jpath.ToStr(v)
		return got != *r.Not, "当前 " + got
	}
	n, ok := jpath.ToNum(v)
	if !ok {
		return false, ""
	}
	switch {
	case r.Above != nil && n > *r.Above:
		return true, fmt.Sprintf("%s，高于 %s", num(n), num(*r.Above))
	case r.Below != nil && n < *r.Below:
		return true, fmt.Sprintf("%s，低于 %s", num(n), num(*r.Below))
	}
	return false, num(n)
}

// planFindings warns when a subscription window is nearly used up while a
// saved reset is waiting, and before a saved reset expires unused.
func (e *Engine) planFindings(s state.Snapshot) []finding {
	if e.Plans == nil {
		return nil
	}
	i := s.SourceIndex(config.PlanSource)
	if i < 0 || s.Sources[i].Updated.IsZero() {
		return nil
	}
	var doc struct {
		Accounts []plans.Account `json:"accounts"`
	}
	b, err := json.Marshal(s.Sources[i].Data)
	if err != nil || json.Unmarshal(b, &doc) != nil {
		return nil
	}
	now := s.Now
	if e.Now != nil {
		now = e.Now()
	}
	var out []finding
	for _, a := range doc.Accounts {
		label := strings.TrimSpace(a.Label + " " + a.Email)
		r := a.Resets
		if r == nil || r.Available == 0 || a.Error != "" {
			// Keep the tracks; nothing to judge.
			out = append(out, finding{key: "plan:" + a.Key + ":hot", label: label, kind: "plan"},
				finding{key: "plan:" + a.Key + ":expiry", label: label, kind: "plan"})
			continue
		}
		hot := finding{key: "plan:" + a.Key + ":hot", label: label, kind: "plan", watched: e.Plans.AlertAt > 0}
		var top plans.Window
		for _, w := range a.Windows {
			if w.Used > top.Used {
				top = w
			}
		}
		if hot.watched && top.Used >= e.Plans.AlertAt {
			hot.bad = true
			hot.detail = fmt.Sprintf("%s已用 %.0f%%，还有 %d 次重置", top.Label, top.Used, r.Available)
			if r.Usable {
				hot.detail += "，现在可以重置"
			}
		}
		exp := finding{key: "plan:" + a.Key + ":expiry", label: label, kind: "plan", watched: e.Plans.ExpiryWarning() > 0}
		if exp.watched && r.NextExpiry != nil && r.NextExpiry.Sub(now) < e.Plans.ExpiryWarning() {
			exp.bad = true
			exp.detail = fmt.Sprintf("有一次重置 %s 后过期，没用就浪费了", human(r.NextExpiry.Sub(now)))
		}
		out = append(out, hot, exp)
	}
	return out
}
