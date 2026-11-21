package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/okchum/mantel/internal/config"
)

// cookieName keeps the token after the first ?k= visit.
const cookieName = "mantel_k"

// tokenFrom is where a request's token came from.
type tokenFrom int

const (
	fromQuery tokenFrom = iota + 1
	fromBearer
	fromCookie
)

// gate checks access tokens. A token may arrive as ?k=, as a cookie set the
// first time ?k= was seen, or as "Authorization: Bearer" (scripts, iOS app).
// Only the page itself and /api/ are gated: the JS, CSS and font carry no data,
// and leaving them open means a missing cookie cannot break a page that loaded.
type gate struct {
	tokens []config.Token
	// hosts are extra Host names accepted in open mode (public_url's host and
	// allowed_hosts); see hostAllowed.
	hosts []string
	// opened remembers when each token last logged "opened" for a ?k= image
	// fetch, so refreshing cameras do not fill the log. Nil disables the log.
	opened *openedLog
}

func newGate(cfg *config.Config) gate {
	g := gate{tokens: cfg.Tokens, hosts: append([]string(nil), cfg.AllowedHosts...), opened: &openedLog{last: map[string]time.Time{}}}
	if u, err := url.Parse(cfg.PublicURL); err == nil && u.Hostname() != "" {
		g.hosts = append(g.hosts, strings.ToLower(u.Hostname()))
	}
	return g
}

// openedLog rate-limits "access: X opened" lines for image fetches.
type openedLog struct {
	mu   sync.Mutex
	last map[string]time.Time
}

const openedEvery = 10 * time.Minute

// first reports whether name has not been logged within openedEvery.
func (o *openedLog) first(name string, now time.Time) bool {
	if o == nil {
		return true
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if t, ok := o.last[name]; ok && now.Sub(t) < openedEvery {
		return false
	}
	o.last[name] = now
	return true
}

// hostAllowed reports whether an open-mode request's Host header names this
// server rather than a domain an attacker controls. Without tokens a browser
// tricked by DNS rebinding (evil.example resolving to this LAN address) would
// otherwise read the state and, being on the LAN, run actions. Accepted: IP
// literals, localhost, single-label names, the private suffixes .local, .lan,
// .home.arpa, .internal and .localhost, public_url's host, and allowed_hosts
// (exact or "*.suffix"). X-Forwarded-Host is not trusted here.
func (g gate) hostAllowed(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if host == "" || net.ParseIP(host) != nil || host == "localhost" || !strings.Contains(host, ".") {
		return true
	}
	for _, s := range []string{".local", ".lan", ".home.arpa", ".internal", ".localhost"} {
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	for _, a := range g.hosts {
		if a == host || (strings.HasPrefix(a, "*.") && strings.HasSuffix(host, a[1:])) {
			return true
		}
	}
	return false
}

// who returns the token, where it came from (and its raw value), and ok.
func (g gate) who(r *http.Request) (tok config.Token, from tokenFrom, raw string, ok bool) {
	try := func(v string) (config.Token, bool) {
		if v == "" {
			return config.Token{}, false
		}
		for _, t := range g.tokens {
			if subtle.ConstantTimeCompare([]byte(v), []byte(t.Token)) == 1 {
				return t, true
			}
		}
		return config.Token{}, false
	}
	q := r.URL.Query().Get("k")
	if n, ok := try(q); ok {
		return n, fromQuery, q, true
	}
	if a := r.Header.Get("Authorization"); len(a) > 7 && strings.EqualFold(a[:7], "bearer ") {
		v := strings.TrimSpace(a[7:])
		if n, ok := try(v); ok {
			return n, fromBearer, v, true
		}
	}
	if ck, err := r.Cookie(cookieName); err == nil {
		if n, ok := try(ck.Value); ok {
			return n, fromCookie, ck.Value, true
		}
	}
	return config.Token{}, 0, "", false
}

type tokenKey struct{}

// caller is who made a request, for scope checks and logs.
type caller struct {
	name   string
	scopes func(string) bool
}

// callerOf returns the request's caller. Without configured tokens, viewing is
// open and acting/pushing is allowed only for direct requests from the LAN or
// this machine (no X-Forwarded-For, so a reverse proxy in front does not count).
func callerOf(r *http.Request) caller {
	if t, ok := r.Context().Value(tokenKey{}).(config.Token); ok {
		return caller{name: t.Name, scopes: t.Can}
	}
	local := false
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && r.Header.Get("X-Forwarded-For") == "" {
		if ip := net.ParseIP(host); ip != nil {
			local = ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
		}
	}
	return caller{name: "lan", scopes: func(s string) bool { return s == config.ScopeView || local }}
}

func gated(path string) bool {
	return path == "/" || path == "/index.html" || strings.HasPrefix(path, "/api/")
}

func (g gate) wrap(h http.Handler) http.Handler {
	if len(g.tokens) == 0 {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !g.hostAllowed(r.Host) {
				log.Printf("access: refused Host %q for %s %s from %s (add it to allowed_hosts if it is yours)", r.Host, r.Method, r.URL.Path, r.RemoteAddr)
				w.Header().Set("Cache-Control", "no-store")
				if strings.HasPrefix(r.URL.Path, "/api/") {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusMisdirectedRequest)
					_, _ = w.Write([]byte(`{"error":"unknown host"}` + "\n"))
					return
				}
				http.Error(w, "unknown host: add it to allowed_hosts in config.json", http.StatusMisdirectedRequest)
				return
			}
			h.ServeHTTP(w, r)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !gated(r.URL.Path) {
			h.ServeHTTP(w, r)
			return
		}
		tok, from, raw, ok := g.who(r)
		name := tok.Name
		if !ok {
			log.Printf("access: denied %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
			w.Header().Set("Cache-Control", "no-store")
			if strings.HasPrefix(r.URL.Path, "/api/") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}` + "\n"))
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(deniedPage))
			return
		}
		if from == fromQuery {
			// Image widgets refresh with ?k= every few seconds; log those once
			// per token per openedEvery instead of on every fetch.
			if !strings.HasPrefix(r.URL.Path, "/api/v1/image/") || g.opened.first(name, time.Now()) {
				log.Printf("access: %s opened %s from %s", name, r.URL.Path, r.RemoteAddr)
			}
		}
		if from == fromQuery {
			secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
			http.SetCookie(w, &http.Cookie{
				Name: cookieName, Value: raw, Path: "/",
				MaxAge: 400 * 24 * 3600, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
			})
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tokenKey{}, tok)))
	})
}

const deniedPage = `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>需要访问令牌</title>
<body style="background:#0f2629;color:#ece6d6;font:18px/1.6 system-ui,sans-serif;display:grid;place-items:center;height:100vh;margin:0">
<p>这个看板需要访问令牌：在网址后面加上 <code>?k=你的令牌</code>。</p>
`

// newToken returns 32 random bytes, URL-safe.
func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
