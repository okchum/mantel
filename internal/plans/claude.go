package plans

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

const (
	claudeAPI        = "https://api.anthropic.com"
	claudeUsagePath  = "/api/oauth/usage?cedar_ember=1"
	claudeStatusPath = "/api/oauth/usage?cedar_ember=1&skip_spend=1"
	claudeProfile    = "/api/oauth/profile"
	claudeProgram    = "cedar_ember"
)

var (
	orgRe   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	grantRe = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)
)

func claudeHeader() map[string]string {
	return map[string]string{
		"Authorization":  "Bearer $TOKEN$",
		"Content-Type":   "application/json",
		"User-Agent":     "claude-cli/2.1.280 (external, cli)",
		"anthropic-beta": "oauth-2025-04-20",
	}
}

type claudeWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    any      `json:"resets_at"`
}

type claudeGrant struct {
	ID               string             `json:"id"`
	Label            string             `json:"label"`
	ResetsTotal      int                `json:"resets_total"`
	ResetsLeft       int                `json:"resets_left"`
	StartsAt         any                `json:"starts_at"`
	EndsAt           any                `json:"ends_at"`
	Paused           bool               `json:"paused"`
	UsableNow        *bool              `json:"usable_now"`
	UseRequiresLimit *bool              `json:"use_requires_limit"`
	GivesBackWeekly  bool               `json:"gives_back_weekly"`
	PercentUsed      map[string]float64 `json:"percent_used"`
}

type claudeEmber struct {
	Eligible         bool          `json:"eligible"`
	IneligibleReason string        `json:"ineligible_reason"`
	AtLimit          bool          `json:"at_limit"`
	Grants           []claudeGrant `json:"grants"`
	NextGrantID      string        `json:"next_grant_id"`
	CooldownUntil    any           `json:"cooldown_until"`
}

// claudeWindows are shown in this order; seven_day_oauth_apps stands in for
// seven_day when the account reports only that.
var claudeWindows = []struct{ key, label string }{
	{"five_hour", "5 小时"},
	{"seven_day", "本周"},
	{"seven_day_opus", "Opus 本周"},
	{"seven_day_sonnet", "Sonnet 本周"},
}

func claudeUsage(ctx context.Context, b Backend, c Cred, now time.Time) (Account, error) {
	a := Account{Label: "Claude"}
	doc, err := claudeGet(ctx, b, c, claudeUsagePath)
	if err != nil {
		return a, err
	}
	win := func(key string) *claudeWindow {
		raw, ok := doc[key]
		if !ok {
			return nil
		}
		var w claudeWindow
		if json.Unmarshal(raw, &w) != nil || w.Utilization == nil {
			return nil
		}
		return &w
	}
	for _, k := range claudeWindows {
		w := win(k.key)
		if w == nil && k.key == "seven_day" {
			w = win("seven_day_oauth_apps")
		}
		if w == nil {
			continue
		}
		a.Windows = append(a.Windows, Window{ID: k.key, Label: k.label, Used: clampPct(*w.Utilization), ResetsAt: parseTime(w.ResetsAt)})
	}
	if raw, ok := doc["cedar_ember"]; ok {
		var e claudeEmber
		if json.Unmarshal(raw, &e) == nil {
			res, _ := claudeResets(e, now)
			a.Resets = res
		}
	}
	return a, nil
}

func claudeGet(ctx context.Context, b Backend, c Cred, path string) (map[string]json.RawMessage, error) {
	r, err := b.Call(ctx, c, Request{Method: "GET", URL: claudeAPI + path, Header: claudeHeader()})
	if err != nil {
		return nil, err
	}
	if r.Status/100 != 2 {
		return nil, httpErr(r)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(r.Body, &doc); err != nil {
		return nil, fmt.Errorf("用量数据看不懂: %w", err)
	}
	return doc, nil
}

// claudeResets counts spendable resets and picks the grant a reset would use.
func claudeResets(e claudeEmber, now time.Time) (*Resets, *claudeGrant) {
	res := &Resets{}
	var pick *claudeGrant
	firstBlock := ""
	live := func(g *claudeGrant) bool {
		if g.Paused || g.ResetsLeft <= 0 {
			return false
		}
		if t := parseTime(g.StartsAt); t != nil && t.After(now) {
			return false
		}
		if t := parseTime(g.EndsAt); t != nil && !t.After(now) {
			return false
		}
		return true
	}
	order := make([]*claudeGrant, 0, len(e.Grants))
	for i := range e.Grants {
		g := &e.Grants[i]
		if g.ID == e.NextGrantID {
			order = append([]*claudeGrant{g}, order...)
		} else {
			order = append(order, g)
		}
	}
	for _, g := range order {
		if !live(g) {
			continue
		}
		res.Available += g.ResetsLeft
		if t := parseTime(g.EndsAt); t != nil && (res.NextExpiry == nil || t.Before(*res.NextExpiry)) {
			res.NextExpiry = t
		}
		why := claudeBlocker(e, g, now)
		if why == "" && pick == nil {
			pick = g
		} else if why != "" && firstBlock == "" {
			firstBlock = why
		}
	}
	switch {
	case !e.Eligible:
		res.Blocker = "这个账号不能用重置"
		pick = nil
	case pick != nil:
		res.Usable = true
	case res.Available == 0:
		res.Blocker = "没有可用的重置次数"
	default:
		res.Blocker = firstBlock
	}
	return res, pick
}

func claudeBlocker(e claudeEmber, g *claudeGrant, now time.Time) string {
	if t := parseTime(e.CooldownUntil); t != nil && t.After(now) {
		return "冷却中，" + t.In(time.Local).Format("15:04") + " 后可用"
	}
	if g.UsableNow == nil || !*g.UsableNow {
		return "官方暂时不让用这次重置"
	}
	requires := g.UseRequiresLimit == nil || *g.UseRequiresLimit
	if requires && !e.AtLimit {
		return "还没到限额，到了才能重置"
	}
	if g.GivesBackWeekly && len(g.PercentUsed) > 0 {
		zero := true
		for _, p := range g.PercentUsed {
			if p != 0 {
				zero = false
			}
		}
		if zero {
			return "还没有用量，重置不划算"
		}
	}
	return ""
}

func (s *Service) claudeReset(ctx context.Context, key string, c Cred) (string, error) {
	// Read fresh state right before spending; the screen may be minutes old.
	doc, err := claudeGet(ctx, s.Backend, c, claudeStatusPath)
	if err != nil {
		return "", err
	}
	var e claudeEmber
	if raw, ok := doc["cedar_ember"]; !ok || json.Unmarshal(raw, &e) != nil {
		return "", errors.New("这个账号没有重置信息")
	}
	res, g := claudeResets(e, s.now())
	if g == nil {
		return "", errors.New(cmpOr(res.Blocker, "现在不能重置"))
	}
	if !grantRe.MatchString(g.ID) {
		return "", errors.New("重置信息格式不对")
	}
	prof, err := claudeGet(ctx, s.Backend, c, claudeProfile)
	if err != nil {
		return "", err
	}
	var org struct {
		UUID string `json:"uuid"`
	}
	if raw, ok := prof["organization"]; !ok || json.Unmarshal(raw, &org) != nil || !orgRe.MatchString(org.UUID) {
		return "", errors.New("读不到账号的组织信息")
	}

	id, retry, err := s.requestID(key, g.ID)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]string{"program": claudeProgram, "grant_id": g.ID, "request_id": id})
	r, err := s.Backend.Call(ctx, c, Request{
		Method: "POST",
		URL:    claudeAPI + "/api/organizations/" + org.UUID + "/reset_rate_limits",
		Header: claudeHeader(),
		Body:   string(body),
	})
	if err != nil {
		return "", errDoubt
	}
	switch r.Status {
	case 429:
		s.settle(key)
		return "", errors.New("官方限流，没有花掉重置次数，稍后再试")
	case 401, 403:
		s.settle(key)
		return "", httpErr(r)
	}
	var out struct {
		Result string `json:"result"`
	}
	if r.Status/100 != 2 || json.Unmarshal(r.Body, &out) != nil {
		return "", errDoubt
	}
	switch out.Result {
	case "reset":
		s.settle(key)
		return "Claude 用量已重置", nil
	case "already_used":
		s.settle(key)
		if retry {
			return "Claude 用量已重置（上一次的请求其实已经生效）", nil
		}
		return "这次重置已经用过了", nil
	}
	// A refusal on a retry can't prove the earlier doubtful request didn't spend;
	// keep its id so the next press asks about the same request again.
	if !retry {
		s.settle(key)
	}
	switch out.Result {
	case "not_limited":
		return "", errors.New("还没到限额，没有花掉重置次数")
	case "cooldown":
		return "", errors.New("冷却中，稍后再试")
	case "ineligible":
		return "", errors.New("这个账号不能用重置")
	case "unavailable":
		return "", errors.New("官方暂时不提供重置")
	}
	return "", errDoubt
}
