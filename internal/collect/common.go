// Package collect runs the background collectors that feed state.Store.
package collect

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/okchum/mantel/internal/httpx"
)

// shortErr turns verbose net/url errors into something that fits on a wall display.
func shortErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return errors.New("timeout")
	}
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return oe.Err
	}
	return err
}

// ClientOpts describes an outgoing HTTP client.
type ClientOpts struct {
	// Timeout bounds a whole request; zero leaves it to the request context.
	Timeout time.Duration
	// Insecure skips TLS verification (opt-in per source for self-signed certs).
	Insecure bool
	// NoKeepAlive closes the connection after each request (probes measure a
	// fresh connect every time).
	NoKeepAlive bool
	// NoRedirect returns redirects as they are instead of following them.
	NoRedirect bool
}

// NewClient builds the HTTP client for a collector or action. Plain clients
// share http.DefaultTransport; the others get one transport each, cloned from
// it so they keep its dial, idle and proxy settings. Build clients once per
// collector, not per request: every transport owns a pool of idle sockets.
func NewClient(o ClientOpts) *http.Client {
	c := &http.Client{Timeout: o.Timeout}
	if o.Insecure || o.NoKeepAlive {
		t := http.DefaultTransport.(*http.Transport).Clone()
		if o.Insecure {
			t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in per source
		}
		t.DisableKeepAlives = o.NoKeepAlive
		t.IdleConnTimeout = 90 * time.Second
		c.Transport = t
	}
	if o.NoRedirect {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return c
}

// maxBody caps how much of a response a collector reads.
const maxBody = 4 << 20

// get GETs url with headers and returns the body (a longer one than maxBody
// is an error, not cut short); a non-2xx status is an *httpx.StatusError
// quoting the start of the body.
func get(ctx context.Context, client *http.Client, u, accept string, headers map[string]string) ([]byte, error) {
	b, err := httpx.Get(ctx, client, httpx.Request{URL: u, Accept: accept, Headers: headers}, maxBody)
	if err != nil {
		return nil, shortErr(err)
	}
	return b, nil
}

// httpStatus is the HTTP status behind err, or 0 when it is not a non-2xx answer.
func httpStatus(err error) int { return httpx.Status(err) }

// getJSON GETs url with headers and decodes the JSON body into v.
func getJSON(ctx context.Context, client *http.Client, u string, headers map[string]string, v any) error {
	body, err := get(ctx, client, u, "application/json", headers)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

// kicks lets an action ask a collector to run now instead of at its next tick.
var kicks = struct {
	sync.Mutex
	m map[string]chan struct{}
}{m: map[string]chan struct{}{}}

func kickChan(name string) chan struct{} {
	kicks.Lock()
	defer kicks.Unlock()
	c, ok := kicks.m[name]
	if !ok {
		c = make(chan struct{}, 1)
		kicks.m[name] = c
	}
	return c
}

// Kick makes the collector registered under name (e.g. "quota:DeepSeek",
// "server:nas", "weather", "calendar", or a source name) run right away.
// It reports whether such a collector is running.
func Kick(name string) bool {
	kicks.Lock()
	c, ok := kicks.m[name]
	kicks.Unlock()
	if ok {
		select {
		case c <- struct{}{}:
		default:
		}
	}
	return ok
}

// every runs fn now and then on each tick (or Kick(name)) until ctx ends.
func every(ctx context.Context, name string, d time.Duration, fn func()) {
	kick := kickChan(name)
	fn()
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn()
		case <-kick:
			fn()
			t.Reset(d)
		}
	}
}
