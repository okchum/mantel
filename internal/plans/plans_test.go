package plans

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCPA plays CLIProxyAPI and the two upstreams behind it.
type fakeCPA struct {
	t   *testing.T
	now time.Time

	mu        sync.Mutex
	atLimit   bool
	resetCode int    // HTTP status for reset_rate_limits / consume
	result    string // claude result
	codexCode string // codex code
	claims    []string
	consumes  []string
	cooldowns []string
	calls     []string
}

func (f *fakeCPA) handler() http.Handler {
	mux := http.NewServeMux()
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer mgmt-key" {
			http.Error(w, `{"error":"invalid management key"}`, http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /v8/management/credentials", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{
			{"auth_index": "a1", "name": "claude-me@x.com.json", "provider": "claude", "email": "me@x.com"},
			{"auth_index": "b2", "name": "codex-me.json", "type": "codex",
				"id_token": map[string]any{"chatgpt_account_id": "acc-1", "plan_type": "pro", "email": "me@x.com"}},
			{"auth_index": "c3", "name": "claude-old.json", "provider": "claude", "disabled": true},
			{"auth_index": "d4", "name": "gemini.json", "provider": "gemini-cli"},
		}})
	})
	mux.HandleFunc("POST /v8/management/routing/cooldown/reset", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.cooldowns = append(f.cooldowns, in["auth_index"])
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("POST /v8/management/requests/api-call", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		var in struct {
			AuthIndex string            `json:"auth_index"`
			Method    string            `json:"method"`
			URL       string            `json:"url"`
			Header    map[string]string `json:"header"`
			Data      string            `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			f.t.Fatal(err)
		}
		if in.Header["Authorization"] != "Bearer $TOKEN$" {
			f.t.Errorf("%s: Authorization = %q, want the $TOKEN$ placeholder", in.URL, in.Header["Authorization"])
		}
		status, body := f.upstream(in.AuthIndex, in.Method, in.URL, in.Header, in.Data)
		s, _ := json.Marshal(body)
		_ = json.NewEncoder(w).Encode(map[string]any{"status_code": status, "header": map[string]any{}, "body": string(s)})
	})
	return mux
}

func (f *fakeCPA) upstream(idx, method, url string, h map[string]string, data string) (int, any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method+" "+url)
	future := f.now.Add(48 * time.Hour).Format(time.RFC3339)
	switch {
	case url == codexUsageURL:
		if idx != "b2" || h["Chatgpt-Account-Id"] != "acc-1" {
			f.t.Errorf("codex usage: idx %s account %q", idx, h["Chatgpt-Account-Id"])
		}
		return 200, map[string]any{"plan_type": "pro", "rate_limit": map[string]any{
			"limit_reached":    false,
			"primary_window":   map[string]any{"used_percent": 97, "reset_after_seconds": 3600, "limit_window_seconds": 18000},
			"secondary_window": map[string]any{"used_percent": 40, "reset_at": f.now.Add(72 * time.Hour).Unix(), "limit_window_seconds": 604800},
		}}
	case url == codexCreditsURL:
		return 200, map[string]any{"available_count": 3, "credits": []map[string]any{
			{"id": "c1", "status": "available", "reset_type": "codex_rate_limits", "expires_at": future},
			{"id": "c2", "status": "available", "reset_type": "codex_rate_limits", "expires_at": f.now.Add(-time.Hour).Format(time.RFC3339)},
			{"id": "c3", "status": "redeemed", "reset_type": "codex_rate_limits", "expires_at": future},
		}}
	case url == codexConsumeURL:
		var in map[string]string
		_ = json.Unmarshal([]byte(data), &in)
		f.consumes = append(f.consumes, in["redeem_request_id"])
		if f.resetCode != 200 {
			return f.resetCode, map[string]any{"error": "boom"}
		}
		return 200, map[string]any{"code": f.codexCode}
	case strings.HasPrefix(url, claudeAPI+"/api/oauth/usage"):
		return 200, map[string]any{
			"five_hour":      map[string]any{"utilization": 100, "resets_at": f.now.Add(2 * time.Hour).Format(time.RFC3339)},
			"seven_day":      map[string]any{"utilization": 61.5, "resets_at": future},
			"seven_day_opus": nil,
			"cedar_ember": map[string]any{
				"eligible": true, "at_limit": f.atLimit, "next_grant_id": "g1",
				"grants": []map[string]any{
					{"id": "g1", "resets_total": 2, "resets_left": 1, "ends_at": future, "usable_now": true},
					{"id": "g0", "resets_total": 1, "resets_left": 0, "ends_at": future, "usable_now": true},
				},
			},
		}
	case url == claudeAPI+claudeProfile:
		return 200, map[string]any{"organization": map[string]any{"uuid": "0f0e0d0c-1111-2222-3333-444455556666"}}
	case url == claudeAPI+"/api/organizations/0f0e0d0c-1111-2222-3333-444455556666/reset_rate_limits":
		var in map[string]string
		_ = json.Unmarshal([]byte(data), &in)
		if in["program"] != "cedar_ember" || in["grant_id"] != "g1" {
			f.t.Errorf("claim body %v", in)
		}
		f.claims = append(f.claims, in["request_id"])
		if f.resetCode != 200 {
			return f.resetCode, map[string]any{"error": "boom"}
		}
		return 200, map[string]any{"result": f.result}
	}
	f.t.Errorf("unexpected upstream %s %s", method, url)
	return 404, nil
}

func setup(t *testing.T) (*fakeCPA, *Service) {
	now := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)
	f := &fakeCPA{t: t, now: now, atLimit: true, resetCode: 200, result: "reset", codexCode: "reset"}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	s := &Service{Backend: NewCPA(srv.URL+"/v8/management/", "mgmt-key", nil), Now: func() time.Time { return now }}
	return f, s
}

func refresh(t *testing.T, s *Service) map[string]Account {
	t.Helper()
	accts, err := s.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]Account{}
	for _, a := range accts {
		m[a.Key] = a
	}
	return m
}

func TestRefresh(t *testing.T) {
	f, s := setup(t)
	m := refresh(t, s)
	if len(m) != 2 {
		t.Fatalf("accounts %v, want claude + codex (disabled and gemini skipped)", m)
	}
	cl := m["claude:claude-me@x.com.json"]
	if cl.Error != "" || len(cl.Windows) != 2 || cl.Windows[0].ID != "five_hour" || cl.Windows[0].Used != 100 || cl.Windows[1].Used != 61.5 {
		t.Fatalf("claude %+v", cl)
	}
	if !cl.Limited || cl.Resets == nil || cl.Resets.Available != 1 || !cl.Resets.Usable || cl.Resets.NextExpiry == nil {
		t.Fatalf("claude resets %+v limited %v", cl.Resets, cl.Limited)
	}
	cx := m["codex:codex-me.json"]
	if cx.Plan != "Pro" || cx.Email != "me@x.com" || len(cx.Windows) != 2 {
		t.Fatalf("codex %+v", cx)
	}
	if w := cx.Windows[0]; w.Label != "5 小时" || w.Used != 97 || w.ResetsAt == nil || !w.ResetsAt.Equal(f.now.Add(time.Hour)) {
		t.Fatalf("codex 5h %+v", w)
	}
	if w := cx.Windows[1]; w.Label != "本周" || w.ResetsAt == nil || !w.ResetsAt.Equal(f.now.Add(72*time.Hour)) {
		t.Fatalf("codex week %+v", w)
	}
	if r := cx.Resets; r == nil || r.Available != 1 || !r.Usable {
		t.Fatalf("codex resets %+v (expired and redeemed credits must not count)", r)
	}

	f.atLimit = false
	m = refresh(t, s)
	r := m["claude:claude-me@x.com.json"].Resets
	if r.Usable || r.Available != 1 || !strings.Contains(r.Blocker, "还没到限额") {
		t.Fatalf("not at limit: %+v", r)
	}
}

func TestOnly(t *testing.T) {
	_, s := setup(t)
	s.Only = []string{"CODEX"}
	m := refresh(t, s)
	if len(m) != 1 || m["codex:codex-me.json"].Key == "" {
		t.Fatalf("%v", m)
	}
}

func TestClaudeReset(t *testing.T) {
	f, s := setup(t)
	refresh(t, s)
	msg, err := s.Reset(context.Background(), "claude:claude-me@x.com.json")
	if err != nil || msg != "Claude 用量已重置" {
		t.Fatalf("%q %v", msg, err)
	}
	if len(f.claims) != 1 || len(f.cooldowns) != 1 || f.cooldowns[0] != "a1" {
		t.Fatalf("claims %v cooldowns %v", f.claims, f.cooldowns)
	}
}

func TestClaudeResetDoubtReusesRequestID(t *testing.T) {
	f, s := setup(t)
	refresh(t, s)
	f.resetCode = 502
	if _, err := s.Reset(context.Background(), "claude:claude-me@x.com.json"); err != errDoubt {
		t.Fatalf("err %v, want doubt", err)
	}
	f.resetCode, f.result = 200, "already_used"
	msg, err := s.Reset(context.Background(), "claude:claude-me@x.com.json")
	if err != nil || !strings.Contains(msg, "已经生效") {
		t.Fatalf("%q %v", msg, err)
	}
	if len(f.claims) != 2 || f.claims[0] != f.claims[1] {
		t.Fatalf("retry must resend the same request id: %v", f.claims)
	}
	// Settled: the next reset is a new request.
	f.result = "reset"
	_, _ = s.Reset(context.Background(), "claude:claude-me@x.com.json")
	if f.claims[2] == f.claims[1] {
		t.Fatal("settled request id was reused")
	}
}

func TestClaudeResetBlockedSpendsNothing(t *testing.T) {
	f, s := setup(t)
	refresh(t, s)
	f.atLimit = false
	_, err := s.Reset(context.Background(), "claude:claude-me@x.com.json")
	if err == nil || !strings.Contains(err.Error(), "还没到限额") {
		t.Fatalf("err %v", err)
	}
	if len(f.claims) != 0 {
		t.Fatal("blocked reset must not be sent")
	}
}

func TestCodexReset(t *testing.T) {
	f, s := setup(t)
	refresh(t, s)
	key := "codex:codex-me.json"
	if msg, err := s.Reset(context.Background(), key); err != nil || msg != "Codex 用量已重置" {
		t.Fatalf("%q %v", msg, err)
	}
	if len(f.cooldowns) != 1 || f.cooldowns[0] != "b2" {
		t.Fatalf("cooldowns %v", f.cooldowns)
	}

	f.codexCode = "nothing_to_reset"
	if _, err := s.Reset(context.Background(), key); err == nil || !strings.Contains(err.Error(), "没有花掉") {
		t.Fatalf("err %v", err)
	}

	f.resetCode = 503
	if _, err := s.Reset(context.Background(), key); err != errDoubt {
		t.Fatalf("err %v", err)
	}
	f.resetCode, f.codexCode = 200, "already_redeemed"
	msg, err := s.Reset(context.Background(), key)
	if err != nil || !strings.Contains(msg, "已经生效") {
		t.Fatalf("%q %v", msg, err)
	}
	n := len(f.consumes)
	if f.consumes[n-1] != f.consumes[n-2] || f.consumes[n-2] == f.consumes[n-3] {
		t.Fatalf("request ids %v", f.consumes)
	}
}

func TestUnknownAccountAndBadKey(t *testing.T) {
	_, s := setup(t)
	if _, err := s.Reset(context.Background(), "claude:nope"); err != ErrUnknownAccount {
		t.Fatalf("err %v", err)
	}
	s.Backend.(*CPA).Key = "wrong"
	if _, err := s.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "管理密钥") {
		t.Fatalf("err %v", err)
	}
}

func TestFailedAccountKeepsLastReading(t *testing.T) {
	f, s := setup(t)
	refresh(t, s)
	srvBackend := s.Backend
	s.Backend = failing{srvBackend}
	m := refresh(t, s)
	cl := m["claude:claude-me@x.com.json"]
	if cl.Error == "" || len(cl.Windows) != 2 {
		t.Fatalf("%+v", cl)
	}
	_ = f
}

type failing struct{ Backend }

func (failing) Call(context.Context, Cred, Request) (Response, error) {
	return Response{Status: 401}, nil
}

func TestParseTime(t *testing.T) {
	want := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)
	for _, v := range []any{"2026-11-20T03:00:00Z", "2026-11-20T16:00:00+13:00", float64(want.Unix()), float64(want.UnixMilli()), "1795143600"} {
		got := parseTime(v)
		if got == nil || !got.Equal(want) {
			t.Errorf("%v → %v", v, got)
		}
	}
	for _, v := range []any{nil, "", "soon", float64(0), true} {
		if parseTime(v) != nil {
			t.Errorf("%v should not parse", v)
		}
	}
}
