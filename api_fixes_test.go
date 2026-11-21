package main

import (
	"strings"
	"testing"
	"time"

	"github.com/okchum/mantel/internal/state"
)

// View-only callers do not learn token names or probe credentials.
func TestViewerRedaction(t *testing.T) {
	ta := newTestAPI(t, true)
	ta.st.Update(func(s *state.Snapshot) {
		s.Actions = map[string]state.ActionResult{"restart:web": {OK: true, By: "actor", Message: "done"}}
		s.Probes = []state.Probe{{Name: "web", URL: "https://user:pw@web.local/health?key=k"}}
	})
	for _, path := range []string{"/api/v1/dashboard", "/api/v1/state", "/api/state"} {
		_, _, view := ta.do(t, req{method: "GET", path: path, token: viewTok})
		if strings.Contains(view, `"actor"`) || strings.Contains(view, "pw@") || strings.Contains(view, "key=k") {
			t.Errorf("%s leaks to a view token: %s", path, view)
		}
		_, _, act := ta.do(t, req{method: "GET", path: path, token: actTok})
		if !strings.Contains(act, `"actor"`) {
			t.Errorf("%s: act token should see who ran actions: %s", path, act)
		}
	}
	_, _, view := ta.do(t, req{method: "GET", path: "/api/v1/state", token: viewTok})
	if !strings.Contains(view, `"url":"https://web.local/health"`) {
		t.Errorf("probe URL should be redacted, not dropped: %s", view)
	}
}

func TestImageHeaders(t *testing.T) {
	ta := newTestAPI(t, true)
	hr := req{method: "GET", path: "/api/v1/image/cam", token: viewTok}
	code, _, body := ta.do(t, hr)
	if code != 200 || body != "png" {
		t.Fatalf("%d %q", code, body)
	}
	w := ta.record(t, hr)
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("headers = %v", w.Header())
	}
}

func TestActionAfterShutdown(t *testing.T) {
	ta := newTestAPI(t, true)
	if !ta.runner.Shutdown(time.Second) {
		t.Fatal("idle runner must shut down at once")
	}
	code, _, _ := ta.do(t, req{method: "POST", path: "/api/v1/actions/restart", body: `{"param":"web"}`, token: actTok})
	if code != 503 {
		t.Fatalf("action after shutdown: %d", code)
	}
}
