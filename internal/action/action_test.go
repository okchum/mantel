package action

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/state"
)

func dur(d time.Duration) config.Duration { return config.Duration{Duration: d} }

// seen is what the test server received.
type seen struct {
	method, path, rawPath, query, body, ctype, auth, ua, item string
}

func newServer(t *testing.T, status int, respBody string) (*httptest.Server, <-chan seen) {
	t.Helper()
	ch := make(chan seen, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- seen{
			method: r.Method, path: r.URL.Path, rawPath: r.URL.EscapedPath(), query: r.URL.RawQuery,
			body: string(b), ctype: r.Header.Get("Content-Type"), auth: r.Header.Get("Authorization"),
			ua: r.Header.Get("User-Agent"), item: r.Header.Get("X-Item"),
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

func newRunner(actions ...config.Action) *Runner {
	return &Runner{Store: state.NewStore("t", "", nil, nil, nil, nil), Actions: actions}
}

func TestRunHTTPSubstitution(t *testing.T) {
	srv, ch := newServer(t, 200, `{"message":"restarted"}`)
	a := config.Action{
		Name: "restart", Title: "Restart", Type: "http", Method: "POST",
		URL:     srv.URL + "/containers/{param}/restart",
		Body:    `{"name":"{param}"}`,
		Headers: map[string]string{"Authorization": "Bearer k", "X-Item": "item={param}"},
		Param:   &config.Param{Values: []string{`a b/"c"`}},
	}
	r := newRunner(a)
	res, err := r.Run("restart", `a b/"c"`, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Message != "restarted" || res.By != "tester" || res.Running {
		t.Fatalf("result = %+v", res)
	}
	got := <-ch
	if got.method != "POST" {
		t.Errorf("method = %q", got.method)
	}
	if got.rawPath != "/containers/a%20b%2F%22c%22/restart" {
		t.Errorf("param not path-escaped: %q", got.rawPath)
	}
	if got.body != `{"name":"a b/\"c\""}` {
		t.Errorf("param not JSON-escaped in body: %s", got.body)
	}
	if got.ctype != "application/json" || got.auth != "Bearer k" || got.ua != "github.com/okchum/mantel/1" {
		t.Errorf("headers: ctype %q auth %q ua %q", got.ctype, got.auth, got.ua)
	}
	if got.item != `item=a b/"c"` {
		t.Errorf("header param = %q", got.item)
	}
	snap := r.Store.Read()
	if rec, ok := snap.Actions[`restart:a b/"c"`]; !ok || !rec.OK || rec.Message != "restarted" {
		t.Errorf("store record = %+v (ok %v); all %v", rec, ok, snap.Actions)
	}
}

func TestRunHTTPMethodAndNoBody(t *testing.T) {
	srv, ch := newServer(t, 204, "")
	r := newRunner(config.Action{Name: "get", Type: "http", Method: "DELETE", URL: srv.URL + "/x?y=1"})
	res, err := r.Run("get", "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := <-ch
	if got.method != "DELETE" || got.path != "/x" || got.query != "y=1" || got.body != "" || got.ctype != "" {
		t.Errorf("request = %+v", got)
	}
	if !res.OK || res.Message != "HTTP 204" {
		t.Errorf("result = %+v", res)
	}
	if _, ok := r.Store.Read().Actions["get"]; !ok {
		t.Error("result not recorded under plain name")
	}
}

func TestRunHTTPStatusAndSummary(t *testing.T) {
	long := strings.Repeat("é", 200)
	cases := []struct {
		name    string
		status  int
		expect  int
		body    string
		wantOK  bool
		wantMsg string
	}{
		{"2xx with message", 200, 0, `{"message":"ok then","msg":"no"}`, true, "ok then"},
		{"msg field", 201, 0, `{"msg":"created"}`, true, "created"},
		{"detail field", 200, 0, `{"detail":"d","error":"e"}`, true, "d"},
		{"error field", 200, 0, `{"error":"e","result":"r"}`, true, "e"},
		{"result field", 200, 0, `{"result":"r"}`, true, "r"},
		{"status field", 200, 0, `{"status":"queued"}`, true, "queued"},
		{"empty message skipped", 200, 0, `{"message":"","msg":"m"}`, true, "m"},
		{"non-string message skipped", 200, 0, `{"message":5,"detail":"d"}`, true, "d"},
		{"json without known field", 200, 0, `{"x":1}`, true, `{"x":1}`},
		{"plain text last line", 200, 0, "line one\nline two\n", true, "line two"},
		{"empty 200", 200, 0, "", true, "HTTP 200"},
		{"long text clipped", 200, 0, long, true, strings.Repeat("é", 140) + "…"},
		{"500 with message", 500, 0, `{"error":"boom"}`, false, "HTTP 500: boom"},
		{"404 empty", 404, 0, "", false, "HTTP 404"},
		{"3xx not ok", 304, 0, "", false, "HTTP 304"},
		{"expect matches non-2xx", 409, 409, `{"message":"already"}`, true, "already"},
		{"expect mismatch on 200", 200, 202, `{"message":"hm"}`, false, "HTTP 200: hm"},
		{"expect 202", 202, 202, "", true, "HTTP 202"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := newServer(t, c.status, c.body)
			r := newRunner(config.Action{Name: "a", Type: "http", Method: "POST", URL: srv.URL, Expect: c.expect})
			res, err := r.Run("a", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if res.OK != c.wantOK || res.Message != c.wantMsg {
				t.Errorf("got ok=%v %q, want ok=%v %q", res.OK, res.Message, c.wantOK, c.wantMsg)
			}
		})
	}
}

func TestRunHTTPConnectionError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	r := newRunner(config.Action{Name: "a", Type: "http", Method: "POST", URL: u})
	res, err := r.Run("a", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Message == "" || strings.Contains(res.Message, u) {
		t.Errorf("want a failed result without the URL wrapper, got %+v", res)
	}
}

func TestRunHTTPTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	r := newRunner(config.Action{Name: "a", Type: "http", Method: "POST", URL: srv.URL, Timeout: dur(50 * time.Millisecond)})
	start := time.Now()
	res, err := r.Run("a", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || !strings.Contains(res.Message, "deadline") {
		t.Errorf("result = %+v", res)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("timeout not applied: took %s", time.Since(start))
	}
}

func needCmd(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s not available: %v", n, err)
		}
	}
}

func TestRunExec(t *testing.T) {
	needCmd(t, "echo", "false", "sleep")
	cases := []struct {
		name    string
		a       config.Action
		param   string
		wantOK  bool
		wantMsg string // substring
	}{
		{"param substituted", config.Action{Command: []string{"echo", "hello {param}!"}, Param: &config.Param{Pattern: `\w+`}}, "web", true, "hello web!"},
		{"json message", config.Action{Command: []string{"echo", `{"message":"done it"}`}}, "", true, "done it"},
		{"last line wins", config.Action{Command: []string{"printf", "a\nb\nc\n"}}, "", true, "c"},
		{"failure", config.Action{Command: []string{"false"}}, "", false, "exit status 1"},
		{"missing binary", config.Action{Command: []string{"/definitely/not/here"}}, "", false, "no such file"},
		{"timeout", config.Action{Command: []string{"sleep", "5"}, Timeout: dur(50 * time.Millisecond)}, "", false, "timed out after 50ms"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.a.Name, c.a.Type = "x", "exec"
			r := newRunner(c.a)
			res, err := r.Run("x", c.param, "")
			if err != nil {
				t.Fatal(err)
			}
			if res.OK != c.wantOK || !strings.Contains(res.Message, c.wantMsg) {
				t.Errorf("got ok=%v %q, want ok=%v containing %q", res.OK, res.Message, c.wantOK, c.wantMsg)
			}
			if c.wantMsg == "hello web!" && res.Message != "hello web!" {
				t.Errorf("message = %q", res.Message)
			}
		})
	}
}

func TestRunExecEnvAndDir(t *testing.T) {
	needCmd(t, "sh")
	dir := t.TempDir()
	// sh is only the test's way to print both values; the runner itself never uses a shell.
	r := newRunner(config.Action{Name: "x", Type: "exec", Dir: dir,
		Env: map[string]string{"MANTEL_TEST_V": "vv"}, Command: []string{"sh", "-c", `echo "$MANTEL_TEST_V $(pwd -P)"`}})
	res, err := r.Run("x", "", "")
	if err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Message != "vv "+real {
		t.Errorf("result = %+v, want message %q", res, "vv "+real)
	}
}

func TestRunRefusals(t *testing.T) {
	r := newRunner(
		config.Action{Name: "plain", Type: "demo"},
		config.Action{Name: "vals", Type: "demo", Param: &config.Param{Values: []string{"a"}}},
		config.Action{Name: "pat", Type: "demo", Param: &config.Param{Pattern: `[a-z]+`}},
	)
	cases := []struct {
		name, param string
		want        error
	}{
		{"nope", "", ErrUnknown},
		{"plain", "x", ErrParam},
		{"vals", "b", ErrParam},
		{"vals", "", ErrParam},
		{"pat", "ABC", ErrParam},
		{"pat", "abc/..", ErrParam},
	}
	for _, c := range cases {
		res, err := r.Run(c.name, c.param, "")
		if !errors.Is(err, c.want) {
			t.Errorf("Run(%q, %q) err = %v, want %v", c.name, c.param, err, c.want)
		}
		if res != (state.ActionResult{}) {
			t.Errorf("Run(%q, %q) result = %+v, want zero", c.name, c.param, res)
		}
	}
	if n := len(r.Store.Read().Actions); n != 0 {
		t.Errorf("refusals recorded %d results", n)
	}
}

func TestRunDemoWithoutHandler(t *testing.T) {
	r := newRunner(config.Action{Name: "d", Type: "demo"})
	start := time.Now()
	res, err := r.Run("d", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || !strings.Contains(res.Message, "demo mode") {
		t.Errorf("result = %+v", res)
	}
	if time.Since(start) > time.Second {
		t.Error("demo without handler should fail fast")
	}
}

func TestRunUnknownType(t *testing.T) {
	r := newRunner(config.Action{Name: "z", Type: "carrier-pigeon"})
	res, err := r.Run("z", "", "")
	if err != nil || res.OK || !strings.Contains(res.Message, `unknown type "carrier-pigeon"`) {
		t.Errorf("res=%+v err=%v", res, err)
	}
}

func TestRunBusy(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		_, _ = io.WriteString(w, `{"message":"slow done"}`)
	}))
	defer srv.Close()
	r := newRunner(config.Action{Name: "slow", Type: "http", Method: "POST", URL: srv.URL + "/{param}",
		Param: &config.Param{Pattern: `[a-z]`}})

	done := make(chan state.ActionResult)
	go func() {
		res, err := r.Run("slow", "a", "first")
		if err != nil {
			t.Errorf("first run: %v", err)
		}
		done <- res
	}()
	<-entered

	// While it runs: the store shows Running, and a second run of the same key is refused.
	if rec := r.Store.Read().Actions["slow:a"]; !rec.Running || rec.By != "first" {
		t.Errorf("running record = %+v", rec)
	}
	if _, err := r.Run("slow", "a", "second"); !errors.Is(err, ErrBusy) {
		t.Errorf("concurrent run err = %v, want ErrBusy", err)
	}
	close(release)
	// A different param is not busy, but the action as a whole is cooling down.
	if _, err := r.Run("slow", "b", "third"); !errors.Is(err, ErrCooldown) {
		t.Errorf("other param err = %v, want ErrCooldown", err)
	}
	res := <-done
	if !res.OK || res.Message != "slow done" {
		t.Errorf("first result = %+v", res)
	}
	if rec := r.Store.Read().Actions["slow:a"]; rec.Running || !rec.OK {
		t.Errorf("final record = %+v", rec)
	}
}

func TestRunCooldown(t *testing.T) {
	r := newRunner(
		config.Action{Name: "dflt", Type: "demo"},
		config.Action{Name: "short", Type: "demo", Cooldown: dur(60 * time.Millisecond)},
		config.Action{Name: "p", Type: "demo", Param: &config.Param{Values: []string{"x", "y"}}},
	)
	if _, err := r.Run("dflt", "", ""); err != nil {
		t.Fatal(err)
	}
	// Default cooldown is 5s; a failed run counts too.
	if _, err := r.Run("dflt", "", ""); !errors.Is(err, ErrCooldown) {
		t.Errorf("second run err = %v, want ErrCooldown", err)
	}

	if _, err := r.Run("short", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run("short", "", ""); !errors.Is(err, ErrCooldown) {
		t.Errorf("within cooldown err = %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	if _, err := r.Run("short", "", ""); err != nil {
		t.Errorf("after cooldown err = %v", err)
	}

	// Cooldown is per action: another value must wait too, so one tap
	// cannot run the action for every allowed value back to back.
	if _, err := r.Run("p", "x", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run("p", "y", ""); !errors.Is(err, ErrCooldown) {
		t.Errorf("other param err = %v, want ErrCooldown", err)
	}
	if _, err := r.Run("p", "x", ""); !errors.Is(err, ErrCooldown) {
		t.Errorf("same param err = %v", err)
	}
}

func TestRunKickAndNotify(t *testing.T) {
	ok, _ := newServer(t, 200, `{"message":"fine"}`)
	bad, _ := newServer(t, 500, `{"error":"nope"}`)
	var mu sync.Mutex
	var kicked []string
	type note struct{ title, body string }
	var notes []note
	r := newRunner(
		config.Action{Name: "good", Title: "Good", Type: "http", Method: "POST", URL: ok.URL, Refresh: []string{"quota:ds", "weather"}, Notify: true},
		config.Action{Name: "fail", Title: "Fail", Type: "http", Method: "POST", URL: bad.URL, Refresh: []string{"weather"}, Notify: true},
		config.Action{Name: "quiet", Title: "Quiet", Type: "http", Method: "POST", URL: ok.URL, Refresh: []string{"s"}},
	)
	r.Kick = func(name string) bool {
		mu.Lock()
		defer mu.Unlock()
		kicked = append(kicked, name)
		return true
	}
	r.Notify = func(title, body string) {
		mu.Lock()
		defer mu.Unlock()
		notes = append(notes, note{title, body})
	}

	for _, n := range []string{"good", "fail", "quiet"} {
		if _, err := r.Run(n, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(kicked, ",") != "quota:ds,weather,s" {
		t.Errorf("kicked = %v (failed runs must not kick)", kicked)
	}
	want := []note{{"Good 完成", "fine"}, {"Fail 失败", "HTTP 500: nope"}}
	if len(notes) != len(want) || notes[0] != want[0] || notes[1] != want[1] {
		t.Errorf("notes = %+v, want %+v", notes, want)
	}
}

func TestRunWithoutStore(t *testing.T) {
	r := &Runner{Actions: []config.Action{{Name: "d", Type: "demo"}}}
	if _, err := r.Run("d", "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestSummarizeAndJSONEscape(t *testing.T) {
	cases := map[string]string{
		"":                        "",
		"  padded  ":              "padded",
		`"just a string"`:         `"just a string"`,
		`[1,2]`:                   `[1,2]`,
		"x\n\n  y  \n":            "y",
		`{"message":"m"}`:         "m",
		`{"status":"s","msg":""}`: "s",
	}
	for in, want := range cases {
		if got := summarize([]byte(in)); got != want {
			t.Errorf("summarize(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{`a"b`: `a\"b`, "a\nb": `a\nb`, `\`: `\\`, "<x>": `\u003cx\u003e`} {
		if got := jsonEscape(in); got != want {
			t.Errorf("jsonEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunPlan(t *testing.T) {
	r := newRunner(config.Action{Name: "plan-reset", Type: "plan", Param: &config.Param{Pattern: config.PlanKeyPattern}})
	res, err := r.Run("plan-reset", "codex:me.json", "t")
	if err != nil || res.OK || !strings.Contains(res.Message, "not configured") {
		t.Fatalf("without hook: %+v %v", res, err)
	}
	var got string
	r.Plan = func(_ context.Context, p string) (string, error) { got = p; return "已重置", nil }
	r.last = nil
	res, err = r.Run("plan-reset", "codex:me.json", "t")
	if err != nil || !res.OK || res.Message != "已重置" || got != "codex:me.json" {
		t.Fatalf("%+v %v %q", res, err, got)
	}
	if _, err := r.Run("plan-reset", "codex:../x", "t"); err != ErrParam {
		t.Fatalf("bad key: %v", err)
	}
}
