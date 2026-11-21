package alert

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/state"
)

type fake struct {
	got   []Message
	fail  bool
	failT string // fail messages whose title contains this
	calls int
}

func (f *fake) Notify(_ context.Context, m Message) error {
	f.calls++
	if f.fail || f.failT != "" && strings.Contains(m.Title, f.failT) {
		return errors.New("offline")
	}
	f.got = append(f.got, m)
	return nil
}

type rig struct {
	e   *Engine
	n   *fake
	st  *state.Store
	now time.Time
}

func newRig() *rig {
	r := &rig{n: &fake{}, now: time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC)}
	r.st = state.NewStore("t", "", []string{"nas", "quiet"}, []string{"blog"}, []string{"deepseek"}, nil)
	r.e = &Engine{
		Store: r.st, Notifier: r.n, After: 2 * time.Minute,
		Servers: []config.Server{{Name: "nas"}, {Name: "quiet", Mute: true}},
		Probes:  []config.Probe{{Name: "blog"}},
		Quotas:  []config.Quota{{Name: "deepseek", AlertBelow: 10}},
		Now:     func() time.Time { return r.now },
	}
	return r
}

func (r *rig) step(d time.Duration, fn func(s *state.Snapshot)) {
	r.now = r.now.Add(d)
	if fn != nil {
		r.st.Update(fn)
	}
	r.e.Check(context.Background(), r.st.Read())
}

func TestDownAfterGraceThenRecover(t *testing.T) {
	r := newRig()
	r.step(0, func(s *state.Snapshot) {
		s.Servers[0].Up = true
		s.Probes[0].Checked, s.Probes[0].Up = true, true
	})
	r.step(15*time.Second, func(s *state.Snapshot) { s.Servers[0].Up, s.Servers[0].Error = false, "connection refused" })
	r.step(time.Minute, nil)
	if len(r.n.got) != 0 {
		t.Fatalf("alerted inside grace period: %+v", r.n.got)
	}
	r.step(90*time.Second, nil)
	if len(r.n.got) != 1 || r.n.got[0].Title != "nas 离线" || r.n.got[0].Body != "connection refused" {
		t.Fatalf("want one down alert, got %+v", r.n.got)
	}
	r.step(15*time.Second, nil)
	if len(r.n.got) != 1 {
		t.Fatalf("must not repeat: %+v", r.n.got)
	}
	r.step(15*time.Second, func(s *state.Snapshot) { s.Servers[0].Up, s.Servers[0].Error = true, "" })
	if len(r.n.got) != 2 || !strings.Contains(r.n.got[1].Body, "nas 已恢复（中断 3 分钟）") {
		t.Fatalf("want recovery, got %+v", r.n.got)
	}
}

func TestBlipStaysQuiet(t *testing.T) {
	r := newRig()
	r.step(0, func(s *state.Snapshot) { s.Probes[0].Checked, s.Probes[0].Up = true, false })
	r.step(time.Minute, func(s *state.Snapshot) { s.Probes[0].Up = true })
	r.step(5*time.Minute, nil)
	if len(r.n.got) != 0 {
		t.Fatalf("a 1-minute blip should not alert: %+v", r.n.got)
	}
}

func TestBurstIsOneMessageAndMutedIgnored(t *testing.T) {
	r := newRig()
	r.step(0, func(s *state.Snapshot) {
		for i := range s.Servers {
			s.Servers[i].Error = "timeout"
		}
		s.Probes[0].Checked, s.Probes[0].Status = true, 503
	})
	r.step(3*time.Minute, nil)
	if len(r.n.got) != 1 {
		t.Fatalf("want 1 combined message, got %+v", r.n.got)
	}
	m := r.n.got[0]
	if m.Title != "2 项离线" || !strings.Contains(m.Body, "blog：HTTP 503") || strings.Contains(m.Body, "quiet") {
		t.Fatalf("bad burst message: %+v", m)
	}
}

func TestNeverScrapedIsNotDown(t *testing.T) {
	r := newRig()
	r.step(0, nil)
	r.step(10*time.Minute, nil)
	if len(r.n.got) != 0 {
		t.Fatalf("empty slots must not alert: %+v", r.n.got)
	}
}

func TestQuotaLowImmediatelyAndTopUp(t *testing.T) {
	r := newRig()
	set := func(v float64) func(s *state.Snapshot) {
		return func(s *state.Snapshot) {
			q := &s.Quotas[0]
			q.Value, q.Unit, q.Mode, q.Updated = v, "¥", "remaining", r.now
		}
	}
	r.step(0, set(12))
	r.step(time.Minute, set(9.5))
	if len(r.n.got) != 1 || r.n.got[0].Title != "deepseek 额度不足" || r.n.got[0].Body != "剩 ¥9.5，低于 ¥10" {
		t.Fatalf("want low alert, got %+v", r.n.got)
	}
	r.step(time.Minute, set(8))
	if len(r.n.got) != 1 {
		t.Fatalf("must not repeat while still low: %+v", r.n.got)
	}
	r.step(time.Minute, set(100))
	if len(r.n.got) != 2 || r.n.got[1].Body != "deepseek 已充值" {
		t.Fatalf("want top-up message, got %+v", r.n.got)
	}
}

func TestFailedSendIsRetried(t *testing.T) {
	r := newRig()
	r.n.fail = true
	r.step(0, func(s *state.Snapshot) { s.Servers[0].Error = "x" })
	r.step(3*time.Minute, nil)
	r.n.fail = false
	r.step(15*time.Second, nil)
	if len(r.n.got) != 1 || r.n.got[0].Title != "nas 离线" {
		t.Fatalf("want retry after failure, got %+v", r.n.got)
	}
}

func TestBarkRequest(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/push" || r.Method != http.MethodPost {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"code":200,"message":"success"}`))
	}))
	defer srv.Close()
	b := &Bark{Cfg: config.Bark{Server: srv.URL + "/", Key: "k123", Group: "Home", Level: "timeSensitive"}, URL: "https://hub.example/"}
	if err := b.Notify(context.Background(), Message{Title: "nas 离线", Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if body["device_key"] != "k123" || body["level"] != "timeSensitive" || body["url"] != "https://hub.example/" || body["group"] != "Home" {
		t.Fatalf("body = %v", body)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"code":400,"message":"failed to get device token"}`))
	}))
	defer bad.Close()
	b.Cfg.Server = bad.URL
	if err := b.Notify(context.Background(), Message{Title: "x"}); err == nil || !strings.Contains(err.Error(), "device token") {
		t.Fatalf("want error with server message, got %v", err)
	}
}

func TestSourceRule(t *testing.T) {
	above := 30.0
	ok := "ok"
	r1 := config.Rule{Title: "机房温度", Path: "temp", Above: &above}
	if bad, d := EvalRule(r1, map[string]any{"temp": 31.5}); !bad || d != "31.5，高于 30" {
		t.Fatalf("above: %v %q", bad, d)
	}
	if bad, _ := EvalRule(r1, map[string]any{"temp": "29"}); bad {
		t.Fatal("29 is fine")
	}
	r2 := config.Rule{Path: "status", Not: &ok}
	if bad, _ := EvalRule(r2, map[string]any{"status": "ok"}); bad {
		t.Fatal("ok is fine")
	}
	if bad, d := EvalRule(r2, map[string]any{"status": "degraded"}); !bad || d != "当前 degraded" {
		t.Fatalf("not: %v %q", bad, d)
	}
	if bad, _ := EvalRule(r1, map[string]any{}); bad {
		t.Fatal("missing path is not a violation")
	}
}

func TestPlanAlerts(t *testing.T) {
	n := &fake{}
	now := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)
	st := state.NewStore("t", "", nil, nil, nil, []string{config.PlanSource})
	e := &Engine{Store: st, Notifier: n, After: 2 * time.Minute, Now: func() time.Time { return now },
		Plans: &config.Plans{AlertAt: 90}}
	set := func(used float64, avail int, expiry time.Duration) {
		exp := now.Add(expiry)
		acct := map[string]any{"key": "codex:me", "provider": "codex", "label": "Codex", "email": "me@x.com",
			"windows": []any{map[string]any{"id": "seven_day", "label": "本周", "used": used}},
			"resets":  map[string]any{"available": avail, "usable": avail > 0, "next_expiry": exp}}
		st.Update(func(s *state.Snapshot) {
			s.Sources[0].Data = map[string]any{"accounts": []any{acct}}
			s.Sources[0].Updated = now
		})
		e.Check(context.Background(), st.Read())
	}
	set(50, 1, 72*time.Hour)
	if len(n.got) != 0 {
		t.Fatalf("quiet expected, got %+v", n.got)
	}
	set(93, 1, 72*time.Hour)
	if len(n.got) != 1 || !strings.Contains(n.got[0].Title, "订阅用量") || !strings.Contains(n.got[0].Body, "本周已用 93%，还有 1 次重置，现在可以重置") {
		t.Fatalf("%+v", n.got)
	}
	set(95, 1, 72*time.Hour)
	if len(n.got) != 1 {
		t.Fatal("must not repeat while still hot")
	}
	set(0, 0, 72*time.Hour) // reset spent: no "all clear" message
	if len(n.got) != 1 {
		t.Fatalf("%+v", n.got)
	}
	set(10, 1, 5*time.Hour)
	if len(n.got) != 2 || !strings.Contains(n.got[1].Body, "过期") {
		t.Fatalf("%+v", n.got)
	}
}
