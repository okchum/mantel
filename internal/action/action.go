// Package action runs the server-side actions a display may trigger
// (reset a quota, restart a container, call a Home Assistant service, ...).
package action

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/okchum/mantel/internal/collect"
	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/state"
	"github.com/okchum/mantel/internal/textutil"
)

var (
	ErrUnknown  = errors.New("unknown action")
	ErrParam    = errors.New("parameter not allowed")
	ErrBusy     = errors.New("already running")
	ErrCooldown = errors.New("ran moments ago; try again shortly")
	ErrClosed   = errors.New("shutting down")
)

// MaxParams caps how many distinct parameters of one action are remembered
// (cooldowns and results); free-form params would otherwise grow without bound.
const MaxParams = 32

// Shared HTTP clients: one keep-alive pool for verified TLS and one for
// actions that opted into insecure. A per-run transport would leak idle
// sockets and their goroutines. Timeouts come from the per-run context.
var (
	httpClient     = collect.NewClient(collect.ClientOpts{})
	insecureClient = collect.NewClient(collect.ClientOpts{Insecure: true})
)

// Runner executes configured actions one at a time per action.
type Runner struct {
	Store   *state.Store
	Actions []config.Action
	// Kick re-runs a collector after success (collect.Kick).
	Kick func(name string) bool
	// Notify, if set and the action asks for it, reports the result (Bark).
	Notify func(title, body string)
	// Demo handles type "demo" actions; returns the message to show.
	Demo func(name, param string) (string, error)
	// Plan handles the built-in "plan" action (subscription reset; param is the account key).
	Plan func(ctx context.Context, param string) (string, error)
	// Context is the root of every run; cancel it on shutdown so commands and
	// requests stop, then call Shutdown to wait for them. Nil means Background.
	Context context.Context

	mu sync.Mutex
	// last is keyed by action name (cooldown), so it holds at most one entry
	// per configured action; running by name:param.
	last    map[string]time.Time
	running map[string]bool
	closed  bool
	wg      sync.WaitGroup
}

// Shutdown refuses new runs and waits up to timeout for running ones to
// finish. It reports whether everything finished in time.
func (r *Runner) Shutdown(timeout time.Duration) bool {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

func (r *Runner) root() context.Context {
	if r.Context != nil {
		return r.Context
	}
	return context.Background()
}

// DefaultTimeout bounds a run whose action sets no timeout.
const DefaultTimeout = 20 * time.Second

// Timeout is how long a run of the named action may take.
func (r *Runner) Timeout(name string) time.Duration {
	a, _ := r.find(name)
	return a.Timeout.Or(DefaultTimeout)
}

func (r *Runner) find(name string) (config.Action, bool) {
	for _, a := range r.Actions {
		if a.Name == name {
			return a, true
		}
	}
	return config.Action{}, false
}

// Run executes the action and records the result in the store. The returned
// error is for refusals (unknown, bad param, busy, cooldown); a failed run is
// a result with OK false.
func (r *Runner) Run(name, param, by string) (state.ActionResult, error) {
	a, ok := r.find(name)
	if !ok {
		return state.ActionResult{}, ErrUnknown
	}
	if !a.Param.Allows(param) {
		return state.ActionResult{}, ErrParam
	}
	// A pattern-matched value starting with "-" would be read as an option by
	// the command; only values listed verbatim may look like flags.
	if a.Type == "exec" && strings.HasPrefix(param, "-") && !slices.Contains(a.Param.Values, param) {
		return state.ActionResult{}, ErrParam
	}
	key := name
	if param != "" {
		key += ":" + param
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return state.ActionResult{}, ErrClosed
	}
	if r.last == nil {
		r.last, r.running = map[string]time.Time{}, map[string]bool{}
	}
	if r.running[key] {
		r.mu.Unlock()
		return state.ActionResult{}, ErrBusy
	}
	// The cooldown is per action, not per value: a bounce of "web" also
	// holds back "db" so one tap cannot fan out over every allowed value.
	if t, ok := r.last[name]; ok && time.Since(t) < a.Cooldown.Or(5*time.Second) {
		r.mu.Unlock()
		return state.ActionResult{}, ErrCooldown
	}
	r.running[key] = true
	r.last[name] = time.Now()
	r.wg.Add(1)
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.running, key)
		r.last[name] = time.Now()
		r.mu.Unlock()
		r.wg.Done()
	}()

	start := time.Now()
	r.record(key, state.ActionResult{At: start, Running: true, By: by})
	msg, err := r.exec(a, param)
	res := state.ActionResult{At: time.Now(), OK: err == nil, Message: msg, By: by, TookMS: time.Since(start).Milliseconds()}
	if err != nil {
		res.Message = err.Error()
	}
	r.record(key, res)
	log.Printf("action: %s ran %s → ok=%v %q (%dms)", by, key, res.OK, res.Message, res.TookMS)
	if res.OK && r.Kick != nil {
		for _, s := range a.Refresh {
			r.Kick(s)
		}
	}
	if a.Notify && r.Notify != nil {
		status := "完成"
		if !res.OK {
			status = "失败"
		}
		r.Notify(a.Title+" "+status, res.Message)
	}
	return res, nil
}

func (r *Runner) record(key string, res state.ActionResult) {
	if r.Store == nil {
		return
	}
	r.Store.Update(func(s *state.Snapshot) {
		if s.Actions == nil {
			s.Actions = map[string]state.ActionResult{}
		}
		s.Actions[key] = res
		if name, _, ok := strings.Cut(key, ":"); ok {
			pruneParams(s.Actions, name+":", MaxParams)
		}
	})
}

// pruneParams keeps the newest max results whose key starts with prefix,
// never dropping one that is still running.
func pruneParams(m map[string]state.ActionResult, prefix string, limit int) {
	var keys []string
	for k := range m {
		if strings.HasPrefix(k, prefix) && !m[k].Running {
			keys = append(keys, k)
		}
	}
	running := 0
	for k, v := range m {
		if strings.HasPrefix(k, prefix) && v.Running {
			running++
		}
	}
	extra := len(keys) + running - limit
	if extra <= 0 {
		return
	}
	slices.SortFunc(keys, func(a, b string) int { return m[a].At.Compare(m[b].At) })
	for _, k := range keys[:min(extra, len(keys))] {
		delete(m, k)
	}
}

func (r *Runner) exec(a config.Action, param string) (string, error) {
	timeout := a.Timeout.Or(DefaultTimeout)
	ctx, cancel := context.WithTimeout(r.root(), timeout)
	defer cancel()
	switch a.Type {
	case "http":
		return runHTTP(ctx, a, param)
	case "exec":
		cmd := make([]string, len(a.Command))
		for i, c := range a.Command {
			cmd[i] = strings.ReplaceAll(c, "{param}", param)
		}
		out, err := collect.RunCommand(ctx, cmd, a.Dir, a.Env, timeout)
		return summarize(out), err
	case "plan":
		if r.Plan == nil {
			return "", errors.New("plans are not configured")
		}
		return r.Plan(ctx, param)
	case "demo":
		if r.Demo == nil {
			return "", errors.New("demo actions only work in demo mode")
		}
		select {
		case <-time.After(1200 * time.Millisecond):
		case <-ctx.Done():
		}
		return r.Demo(a.Name, param)
	}
	return "", fmt.Errorf("unknown type %q", a.Type)
}

func runHTTP(ctx context.Context, a config.Action, param string) (string, error) {
	// Path part and query part need different escaping.
	u := a.URL
	if q := strings.IndexByte(u, '?'); q >= 0 {
		u = strings.ReplaceAll(u[:q], "{param}", url.PathEscape(param)) + "?" +
			strings.ReplaceAll(u[q+1:], "{param}", url.QueryEscape(param))
	} else {
		u = strings.ReplaceAll(u, "{param}", url.PathEscape(param))
	}
	body := strings.ReplaceAll(a.Body, "{param}", jsonEscape(param))
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, a.Method, u, rd)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "github.com/okchum/mantel/1")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range a.Headers {
		req.Header.Set(k, strings.ReplaceAll(v, "{param}", param))
	}
	client := httpClient
	if a.Insecure {
		client = insecureClient
	}
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	ok := resp.StatusCode/100 == 2
	if a.Expect != 0 {
		ok = resp.StatusCode == a.Expect
	}
	msg := summarize(b)
	if !ok {
		if msg == "" {
			return "", fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return msg, nil
}

// summarize picks a human message out of a response: a JSON "message"/"msg"/
// "detail"/"error" field when there is one, else the text itself, clipped.
func summarize(b []byte) string {
	var m map[string]any
	if json.Unmarshal(b, &m) == nil {
		for _, k := range []string{"message", "msg", "detail", "error", "result", "status"} {
			if s, ok := m[k].(string); ok && s != "" {
				return textutil.Clip(s, 140)
			}
		}
	}
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[i+1:])
	}
	return textutil.Clip(s, 140)
}

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}
