package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/okchum/mantel/internal/config"
)

func TestGate(t *testing.T) {
	tok := "abcdefghijklmnopqrstuvwxyz012345"
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	h := gate{tokens: []config.Token{{Name: "nesthub", Token: tok}}}.wrap(ok)

	do := func(path string, mod func(*http.Request)) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if mod != nil {
			mod(r)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if w := do("/", nil); w.Code != 401 || !strings.Contains(w.Body.String(), "?k=") {
		t.Fatalf("page without token: %d %q", w.Code, w.Body)
	}
	if w := do("/api/state", nil); w.Code != 401 || !strings.Contains(w.Body.String(), "unauthorized") {
		t.Fatalf("api without token: %d", w.Code)
	}
	if w := do("/?k=wrong", nil); w.Code != 401 {
		t.Fatalf("wrong token: %d", w.Code)
	}
	for _, p := range []string{"/app.js", "/app.css", "/fonts/x.woff2", "/healthz"} {
		if w := do(p, nil); w.Code != 200 {
			t.Fatalf("%s should be open: %d", p, w.Code)
		}
	}

	w := do("/?receiver=1&k="+tok, nil)
	if w.Code != 200 {
		t.Fatalf("query token: %d", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != cookieName || cookies[0].Value != tok || !cookies[0].HttpOnly || cookies[0].Secure {
		t.Fatalf("cookie = %+v", cookies)
	}
	if w := do("/api/state", func(r *http.Request) { r.AddCookie(cookies[0]) }); w.Code != 200 {
		t.Fatalf("cookie auth: %d", w.Code)
	}
	if w := do("/api/state", func(r *http.Request) { r.AddCookie(cookies[0]) }); len(w.Result().Cookies()) != 0 {
		t.Fatalf("cookie auth must not set the cookie again: %+v", w.Result().Cookies())
	}
	if w := do("/api/v1/state", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }); w.Code != 200 || len(w.Result().Cookies()) != 0 {
		t.Fatalf("bearer auth: %d (and must not set a cookie)", w.Code)
	}
	w = do("/?k="+tok, func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") })
	if c := w.Result().Cookies(); len(c) != 1 || !c[0].Secure {
		t.Fatalf("behind TLS proxy cookie must be Secure: %+v", c)
	}
}

func TestGateOpenWithoutTokens(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/state", nil)
	r.Host = "192.168.1.10:8080"
	gate{}.wrap(ok).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("no tokens should mean open: %d", w.Code)
	}
}

// Without tokens, a DNS-rebound name (evil.example → this LAN address) must not
// reach the API or the page; the server's own names still work.
func TestOpenModeRefusesForeignHost(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	g := newGate(&config.Config{PublicURL: "https://dash.home.example:8443/", AllowedHosts: []string{"mantel.example.org", "*.tail1234.ts.net"}})
	cases := []struct {
		host string
		want int
	}{
		{"evil.example", 421},
		{"evil.example:8080", 421},
		{"dash.home.example.evil.example", 421},
		{"ts.net", 421},
		{"192.168.1.10:8080", 200},
		{"[::1]:8080", 200},
		{"localhost:8080", 200},
		{"nas:8080", 200},
		{"mantel.local", 200},
		{"dash.home.example", 200},
		{"DASH.home.example:8443", 200},
		{"mantel.example.org", 200},
		{"box.tail1234.ts.net", 200},
	}
	for _, c := range cases {
		for _, path := range []string{"/api/v1/state", "/"} {
			r := httptest.NewRequest("GET", path, nil)
			r.Host = c.host
			w := httptest.NewRecorder()
			g.wrap(ok).ServeHTTP(w, r)
			if w.Code != c.want {
				t.Errorf("Host %q %s: %d, want %d", c.host, path, w.Code, c.want)
			}
		}
	}
	// A spoofed X-Forwarded-Host does not help.
	r := httptest.NewRequest("POST", "/api/v1/actions/x", nil)
	r.Host = "evil.example"
	r.Header.Set("X-Forwarded-Host", "localhost")
	w := httptest.NewRecorder()
	g.wrap(ok).ServeHTTP(w, r)
	if w.Code != 421 {
		t.Errorf("forwarded host must not be trusted: %d", w.Code)
	}
}

func TestOpenedLogOncePerToken(t *testing.T) {
	o := &openedLog{last: map[string]time.Time{}}
	now := time.Now()
	if !o.first("tv", now) || o.first("tv", now.Add(time.Minute)) || !o.first("hub", now) {
		t.Fatal("first fetch per token logs, repeats within the window do not")
	}
	if !o.first("tv", now.Add(openedEvery)) {
		t.Fatal("logs again after the window")
	}
}
