package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/okchum/mantel/internal/action"
	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/dash"
	"github.com/okchum/mantel/internal/state"
)

const (
	viewTok = "view-token-0123456789"
	actTok  = "act-token-0123456789a"
	pushTok = "push-token-0123456789"
)

type testAPI struct {
	h      http.Handler
	st     *state.Store
	d      *dash.Dash
	mu     sync.Mutex
	kicked []string
	hits   int
	runner *action.Runner
}

// newTestAPI wires the API the way run() does, behind the token gate.
// With tokens false the config has none (open LAN mode).
func newTestAPI(t *testing.T, tokens bool) *testAPI {
	t.Helper()
	ta := &testAPI{}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cam.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = io.WriteString(w, "png")
			return
		}
		ta.mu.Lock()
		ta.hits++
		ta.mu.Unlock()
		_, _ = io.WriteString(w, `{"message":"restarted"}`)
	}))
	t.Cleanup(target.Close)

	toks := `[]`
	if tokens {
		toks = fmt.Sprintf(`[
			{"name":"viewer","token":%q},
			{"name":"actor","token":%q,"scopes":["act"]},
			{"name":"pusher","token":%q,"scopes":["push"]}]`, viewTok, actTok, pushTok)
	}
	js := fmt.Sprintf(`{
		"public_url": "http://example.com:8080/",
		"tokens": %s,
		"sources": [
			{"name":"pushed","type":"push"},
			{"name":"polled","type":"http_json","url":"http://127.0.0.1:1/"}
		],
		"actions": [
			{"name":"restart","type":"http","url":%q,"refresh":["pushed"],"param":{"values":["web"]}},
			{"name":"show","type":"demo"}
		],
		"pages": [{"name":"main","widgets":[
			{"id":"num","type":"stat","source":"pushed","at":[0,0,2,2]},
			{"id":"btn","type":"buttons","actions":["restart","show"],"at":[2,0,2,2]},
			{"id":"cam","type":"image","url":%q,"at":[4,0,2,2]}
		]}]
	}`, toks, target.URL+"/{param}", target.URL+"/cam.png")
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	ta.st = state.NewStore(cfg.Title, "UTC", nil, nil, nil, []string{"pushed", "polled"})
	ta.d = dash.New(cfg)
	runner := &action.Runner{Store: ta.st, Actions: ta.d.Actions(), Kick: func(name string) bool {
		ta.mu.Lock()
		defer ta.mu.Unlock()
		ta.kicked = append(ta.kicked, name)
		return true
	}}
	ta.runner = runner
	mux := http.NewServeMux()
	(&api{cfg: cfg, st: ta.st, dash: ta.d, runner: runner}).register(mux)
	ta.h = newGate(cfg).wrap(mux)
	return ta
}

type req struct {
	method, path, body, token, origin, remote, xff string
}

func (ta *testAPI) do(t *testing.T, r req) (int, map[string]any, string) {
	t.Helper()
	w := ta.record(t, r)
	raw := w.Body.String()
	var m map[string]any
	if strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("%s %s: invalid JSON %q: %v", r.method, r.path, raw, err)
		}
	}
	return w.Code, m, raw
}

func (ta *testAPI) record(t *testing.T, r req) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if r.body != "" {
		body = strings.NewReader(r.body)
	}
	hr := httptest.NewRequest(r.method, r.path, body)
	if r.token != "" {
		hr.Header.Set("Authorization", "Bearer "+r.token)
	}
	if r.origin != "" {
		hr.Header.Set("Origin", r.origin)
	}
	if r.remote != "" {
		hr.RemoteAddr = r.remote
	}
	if r.xff != "" {
		hr.Header.Set("X-Forwarded-For", r.xff)
	}
	w := httptest.NewRecorder()
	ta.h.ServeHTTP(w, hr)
	return w
}

func TestDashboardLayoutVersion(t *testing.T) {
	ta := newTestAPI(t, true)
	code, m, _ := ta.do(t, req{method: "GET", path: "/api/v1/dashboard", token: viewTok})
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	layout, ok := m["layout"].(map[string]any)
	if !ok {
		t.Fatalf("first fetch must carry the layout: %v", m)
	}
	v, _ := m["version"].(string)
	if v == "" || layout["version"] != v || layout["can_act"] != false {
		t.Errorf("version %q layout %v", v, layout)
	}
	if _, ok := m["data"].(map[string]any)["num"]; !ok {
		t.Errorf("data lacks widget: %v", m["data"])
	}

	code, m, _ = ta.do(t, req{method: "GET", path: "/api/v1/dashboard?v=" + v, token: viewTok})
	if code != 200 || m["version"] != v {
		t.Fatalf("code %d %v", code, m)
	}
	if _, ok := m["layout"]; ok {
		t.Error("layout must be omitted when v matches")
	}
	_, m, _ = ta.do(t, req{method: "GET", path: "/api/v1/dashboard?v=old", token: actTok})
	if l, ok := m["layout"].(map[string]any); !ok || l["can_act"] != true {
		t.Errorf("act token: layout %v", m["layout"])
	}
	_, m, _ = ta.do(t, req{method: "GET", path: "/api/v1/dashboard?page=nope", token: viewTok})
	if d := m["data"].(map[string]any); len(d) != 0 {
		t.Errorf("unknown page data = %v", d)
	}
	if code, _, _ := ta.do(t, req{method: "GET", path: "/api/v1/dashboard"}); code != 401 {
		t.Errorf("no token: %d", code)
	}
}

func TestActionScopes(t *testing.T) {
	ta := newTestAPI(t, true)
	post := func(r req) (int, map[string]any) {
		r.method = "POST"
		code, m, _ := ta.do(t, r)
		return code, m
	}

	// View-only and push-only tokens cannot run real actions.
	for _, tok := range []string{viewTok, pushTok} {
		code, m := post(req{path: "/api/v1/actions/restart", body: `{"param":"web"}`, token: tok})
		if code != 403 || !strings.Contains(fmt.Sprint(m["error"]), "act 权限") {
			t.Errorf("token %s: %d %v", tok, code, m)
		}
	}
	if ta.hits != 0 {
		t.Fatalf("refused action reached the target %d times", ta.hits)
	}

	// Demo actions only touch demo data, so any token may run them.
	code, m := post(req{path: "/api/v1/actions/show", token: viewTok})
	if code != 200 || m["ok"] != false || !strings.Contains(fmt.Sprint(m["message"]), "demo mode") || m["by"] != "viewer" {
		t.Errorf("demo action by viewer: %d %v", code, m)
	}

	// The act token runs it; the result is recorded under name:param and refresh kicks.
	code, m = post(req{path: "/api/v1/actions/restart", body: `{"param":"web"}`, token: actTok})
	if code != 200 || m["ok"] != true || m["message"] != "restarted" || m["by"] != "actor" {
		t.Fatalf("act token: %d %v", code, m)
	}
	if ta.hits != 1 || len(ta.kicked) != 1 || ta.kicked[0] != "pushed" {
		t.Errorf("hits %d kicked %v", ta.hits, ta.kicked)
	}
	_, m, _ = ta.do(t, req{method: "GET", path: "/api/v1/dashboard", token: viewTok})
	if acts, _ := m["actions"].(map[string]any); acts["restart:web"] == nil {
		t.Errorf("dashboard actions = %v", m["actions"])
	}

	// Cooldown → 429; unknown → 404; bad param → 400; bad body → 400.
	cases := []struct {
		name string
		r    req
		want int
	}{
		{"cooldown", req{path: "/api/v1/actions/restart", body: `{"param":"web"}`, token: actTok}, 429},
		{"unknown", req{path: "/api/v1/actions/nope", token: actTok}, 404},
		{"param not allowed", req{path: "/api/v1/actions/restart", body: `{"param":"db"}`, token: actTok}, 400},
		{"missing param", req{path: "/api/v1/actions/restart", token: actTok}, 400},
		{"bad body", req{path: "/api/v1/actions/restart", body: `param=web`, token: actTok}, 400},
		{"cross-site", req{path: "/api/v1/actions/show", token: actTok, origin: "https://evil.example"}, 403},
		{"null origin", req{path: "/api/v1/actions/show", token: actTok, origin: "null"}, 403},
		{"no token", req{path: "/api/v1/actions/show"}, 401},
	}
	for _, c := range cases {
		code, m := post(c.r)
		if code != c.want || m["error"] == nil {
			t.Errorf("%s: %d %v, want %d", c.name, code, m, c.want)
		}
		if c.name == "cross-site" && m["error"] != "cross-site request" {
			t.Errorf("cross-site message = %v", m["error"])
		}
	}
	if ta.hits != 1 {
		t.Errorf("refusals reached the target: %d hits", ta.hits)
	}
}

func TestActionSameOriginAllowed(t *testing.T) {
	ta := newTestAPI(t, true)
	// httptest requests are for host example.com.
	code, m, _ := ta.do(t, req{method: "POST", path: "/api/v1/actions/show", token: actTok, origin: "http://EXAMPLE.com"})
	if code != 200 {
		t.Errorf("same origin: %d %v", code, m)
	}
}

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		origin, fwdHost string
		want            bool
	}{
		{"", "", true},
		{"null", "", false},
		{"http://example.com", "", true},
		{"https://example.com:443", "", false},
		{"http://evil.example", "", false},
		{"https://dash.home.net", "dash.home.net", true},
		{"http://example.com", "dash.home.net", false},
		{"://bad", "", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/", nil)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if c.fwdHost != "" {
			r.Header.Set("X-Forwarded-Host", c.fwdHost)
		}
		if got := sameOrigin(r); got != c.want {
			t.Errorf("sameOrigin(%q, fwd %q) = %v, want %v", c.origin, c.fwdHost, got, c.want)
		}
	}
}

func TestOpenModeActsOnlyFromLAN(t *testing.T) {
	ta := newTestAPI(t, false)
	// Without a param, a request that passes the scope check is refused later
	// at the param check (400) — distinguishable from the scope refusal (403).
	cases := []struct {
		name string
		r    req
		want int
	}{
		{"internet address", req{remote: "203.0.113.9:5000"}, 403},
		{"proxied loopback", req{remote: "127.0.0.1:5000", xff: "203.0.113.9"}, 403},
		{"loopback", req{remote: "127.0.0.1:5000"}, 400},
		{"private LAN", req{remote: "192.168.1.20:5000", body: `{"param":"web"}`}, 200},
	}
	for _, c := range cases {
		c.r.method, c.r.path = "POST", "/api/v1/actions/restart"
		if code, m, _ := ta.do(t, c.r); code != c.want {
			t.Errorf("%s: %d %v, want %d", c.name, code, m, c.want)
		}
	}
	// Viewing stays open from anywhere.
	if code, _, _ := ta.do(t, req{method: "GET", path: "/api/v1/dashboard", remote: "203.0.113.9:5000"}); code != 200 {
		t.Errorf("open view: %d", code)
	}
}

func TestPush(t *testing.T) {
	ta := newTestAPI(t, true)
	source := func(name string) state.Source {
		s := ta.st.Read()
		return s.Sources[s.SourceIndex(name)]
	}
	cases := []struct {
		name string
		r    req
		want int
	}{
		{"view token", req{path: "/api/v1/push/pushed", body: `{"value":1}`, token: viewTok}, 403},
		{"act token", req{path: "/api/v1/push/pushed", body: `{"value":1}`, token: actTok}, 403},
		{"polled source", req{path: "/api/v1/push/polled", body: `{"value":1}`, token: pushTok}, 404},
		{"unknown source", req{path: "/api/v1/push/nope", body: `{"value":1}`, token: pushTok}, 404},
		{"cross-site", req{path: "/api/v1/push/pushed", body: `{"value":1}`, token: pushTok, origin: "http://evil.example"}, 403},
	}
	for _, c := range cases {
		c.r.method = "POST"
		code, m, _ := ta.do(t, c.r)
		if code != c.want || m["error"] == nil {
			t.Errorf("%s: %d %v, want %d", c.name, code, m, c.want)
		}
	}
	if source("pushed").Data != nil || source("polled").Data != nil {
		t.Fatal("refused push changed data")
	}

	steps := []struct {
		method, path, body string
		want               any
	}{
		{"POST", "/api/v1/push/pushed", `{"a":1,"b":2}`, map[string]any{"a": 1.0, "b": 2.0}},
		{"POST", "/api/v1/push/pushed?merge=1", `{"b":3}`, map[string]any{"a": 1.0, "b": 3.0}},
		{"PUT", "/api/v1/push/pushed", `{"c":4}`, map[string]any{"c": 4.0}},
		{"POST", "/api/v1/push/pushed", "17.5\n", map[string]any{"value": 17.5}},
		{"POST", "/api/v1/push/pushed?value=8", "", map[string]any{"value": 8.0}},
		{"POST", "/api/v1/push/pushed", "backup ok", map[string]any{"text": "backup ok"}},
	}
	for _, s := range steps {
		code, m, _ := ta.do(t, req{method: s.method, path: s.path, body: s.body, token: pushTok})
		if code != 200 || m["ok"] != true {
			t.Fatalf("%s %s: %d %v", s.method, s.path, code, m)
		}
		if got := source("pushed").Data; fmt.Sprint(got) != fmt.Sprint(s.want) {
			t.Errorf("%s %s %q: data = %#v, want %#v", s.method, s.path, s.body, got, s.want)
		}
	}

	big := strings.Repeat("x", 1<<20+1)
	if code, _, _ := ta.do(t, req{method: "POST", path: "/api/v1/push/pushed", body: big, token: pushTok}); code != 413 {
		t.Errorf("oversized push: %d", code)
	}
}

// A source reporting "NaN" must not break the dashboard JSON for every widget.
func TestPushNonFiniteKeepsDashboardValid(t *testing.T) {
	ta := newTestAPI(t, true)
	for _, body := range []string{"NaN", `{"value":"nan"}`, `{"value":"-Inf"}`} {
		if code, m, _ := ta.do(t, req{method: "POST", path: "/api/v1/push/pushed", body: body, token: pushTok}); code != 200 {
			t.Fatalf("push %q: %d %v", body, code, m)
		}
		code, m, raw := ta.do(t, req{method: "GET", path: "/api/v1/dashboard", token: viewTok})
		if code != 200 || m == nil {
			t.Fatalf("after %q: %d %q", body, code, raw)
		}
		num, _ := m["data"].(map[string]any)["num"].(map[string]any)
		if num["value"] != nil || num["error"] == nil {
			t.Errorf("after %q: widget = %v, want an error and no value", body, num)
		}
	}
}

func TestQuietReport(t *testing.T) {
	ta := newTestAPI(t, true)
	for _, c := range []struct {
		r    req
		want int
	}{
		{req{path: "/api/v1/quiet?asleep=1", token: viewTok}, 403},
		{req{path: "/api/v1/quiet?asleep=maybe", token: actTok}, 400},
		{req{path: "/api/v1/quiet", body: `{}`, token: actTok}, 400},
		{req{path: "/api/v1/quiet?asleep=1", token: actTok, origin: "http://evil.example"}, 403},
	} {
		c.r.method = "POST"
		if code, m, _ := ta.do(t, c.r); code != c.want {
			t.Errorf("%s: %d %v, want %d", c.r.path, code, m, c.want)
		}
	}
	if ta.st.Read().Asleep != nil {
		t.Fatal("refused report stored")
	}
	if code, _, _ := ta.do(t, req{method: "POST", path: "/api/v1/quiet?asleep=1", token: actTok}); code != 200 {
		t.Fatal(code)
	}
	if a := ta.st.Read().Asleep; a == nil || !a.On || a.By != "actor" {
		t.Fatalf("%+v", a)
	}
	if code, _, _ := ta.do(t, req{method: "POST", path: "/api/v1/quiet", body: `{"asleep": false}`, token: pushTok}); code != 200 {
		t.Fatal(code)
	}
	// Mantel Cast reads it back with a view token.
	code, m, _ := ta.do(t, req{method: "GET", path: "/api/v1/quiet", token: viewTok})
	a, _ := m["asleep"].(map[string]any)
	if code != 200 || a == nil || a["on"] != false || a["by"] != "pusher" {
		t.Fatalf("%d %v", code, m)
	}
	if code, _, _ := ta.do(t, req{method: "GET", path: "/api/v1/quiet"}); code != 401 {
		t.Fatalf("no token should be refused: %d", code)
	}
}
