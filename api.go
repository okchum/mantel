package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/okchum/mantel/internal/action"
	"github.com/okchum/mantel/internal/collect"
	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/dash"
	"github.com/okchum/mantel/internal/state"
)

// api serves the widget dashboard and its interactive endpoints.
type api struct {
	cfg    *config.Config
	st     *state.Store
	dash   *dash.Dash
	runner *action.Runner
}

func (a *api) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/state", a.state)
	mux.HandleFunc("GET /api/v1/state", a.state) // stable path for other clients (iOS, scripts)
	mux.HandleFunc("GET /api/v1/dashboard", a.dashboard)
	mux.HandleFunc("POST /api/v1/actions/{name}", a.action)
	mux.HandleFunc("POST /api/v1/refresh/{source}", a.refresh)
	mux.HandleFunc("POST /api/v1/push/{source}", a.push)
	mux.HandleFunc("PUT /api/v1/push/{source}", a.push)
	mux.HandleFunc("GET /api/v1/image/{id}", a.image)
	mux.HandleFunc("GET /api/v1/quiet", a.quietGet)
	mux.HandleFunc("POST /api/v1/quiet", a.quietSet)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// sameOrigin refuses browser requests coming from another site (CSRF): browsers
// always send Origin on cross-site POSTs; scripts and the iOS app send none.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" || o == "null" {
		return o == ""
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		host = fh
	}
	return strings.EqualFold(u.Host, host)
}

// GET /api/v1/state: the raw snapshot.
func (a *api) state(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, viewFor(a.st.Read(), callerOf(r).scopes(config.ScopeAct)))
}

// GET /api/v1/dashboard?v=<layout version>&page=<name>
func (a *api) dashboard(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	canAct := callerOf(r).scopes(config.ScopeAct)
	writeJSON(w, http.StatusOK, a.dash.Build(viewFor(a.st.Read(), canAct), q.Get("v"), q.Get("page"), canAct))
}

// viewFor hides token names and probe URL credentials from view-only callers.
func viewFor(s state.Snapshot, canAct bool) state.Snapshot {
	if canAct {
		return s
	}
	return s.ForViewer()
}

// POST /api/v1/actions/{name}  body {"param": "..."}
func (a *api) action(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		fail(w, http.StatusForbidden, "cross-site request")
		return
	}
	name := r.PathValue("name")
	var demo bool
	for _, x := range a.runner.Actions {
		if x.Name == name {
			demo = x.Type == "demo"
		}
	}
	c := callerOf(r)
	if !demo && !c.scopes(config.ScopeAct) {
		log.Printf("action: %s may not run %s (token lacks the act scope)", c.name, name)
		fail(w, http.StatusForbidden, "这个令牌不能执行操作（需要 act 权限）")
		return
	}
	var body struct {
		Param string `json:"param"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			fail(w, http.StatusBadRequest, "body must be JSON like {\"param\":\"...\"}")
			return
		}
	}
	// The server's WriteTimeout is shorter than the longest action; let this
	// response wait for the action (plus slack for the reply itself).
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(a.runner.Timeout(name) + 15*time.Second))
	res, err := a.runner.Run(name, body.Param, c.name)
	switch {
	case errors.Is(err, action.ErrUnknown):
		fail(w, http.StatusNotFound, "没有这个操作")
	case errors.Is(err, action.ErrParam):
		fail(w, http.StatusBadRequest, "参数不被允许")
	case errors.Is(err, action.ErrBusy):
		fail(w, http.StatusConflict, "正在执行")
	case errors.Is(err, action.ErrCooldown):
		fail(w, http.StatusTooManyRequests, "刚执行过，稍后再试")
	case errors.Is(err, action.ErrClosed):
		fail(w, http.StatusServiceUnavailable, "服务正在关闭")
	case err != nil:
		fail(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusOK, res)
	}
}

// POST /api/v1/refresh/{source}: run a collector now ("quota:DeepSeek", "weather", a source name).
func (a *api) refresh(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		fail(w, http.StatusForbidden, "cross-site request")
		return
	}
	if !callerOf(r).scopes(config.ScopeAct) {
		fail(w, http.StatusForbidden, "需要 act 权限")
		return
	}
	if !collect.Kick(r.PathValue("source")) {
		fail(w, http.StatusNotFound, "没有这个数据源，或它不是定时采集的")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

// POST /api/v1/push/{source}: replace a push source's document (?merge=1 merges
// top-level keys). JSON body, or a bare number / text.
func (a *api) push(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		fail(w, http.StatusForbidden, "cross-site request")
		return
	}
	c := callerOf(r)
	if !c.scopes(config.ScopePush) {
		log.Printf("push: %s may not push (token lacks the push scope)", c.name)
		fail(w, http.StatusForbidden, "需要 push 权限")
		return
	}
	name := r.PathValue("source")
	ok := false
	for _, s := range a.cfg.Sources {
		if s.Name == name && s.Type == "push" {
			ok = true
		}
	}
	if !ok {
		fail(w, http.StatusNotFound, "没有这个 push 类型的数据源："+name)
		return
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(b) > 1<<20 {
		fail(w, http.StatusRequestEntityTooLarge, "body over 1 MB")
		return
	}
	doc := collect.ParseOutput(b)
	if v := r.URL.Query().Get("value"); v != "" && len(strings.TrimSpace(string(b))) == 0 {
		doc = collect.ParseOutput([]byte(v))
	}
	merge := r.URL.Query().Get("merge") == "1"
	if err := collect.Push(a.st, name, doc, merge); err != nil {
		fail(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// GET /api/v1/image/{id}: the picture of an image widget, fetched by the server.
func (a *api) image(w http.ResponseWriter, r *http.Request) {
	body, ct, err := a.dash.Image(r.Context(), r.PathValue("id"))
	if errors.Is(err, dash.ErrNoWidget) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		// Details go to the log only: the caller may be a viewer, and the
		// error may describe the camera behind the widget.
		if r.Context().Err() == nil {
			log.Printf("image %s: %v", r.PathValue("id"), err)
		}
		http.Error(w, "image unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Even if a document slipped through, it may not run script on this origin.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	_, _ = w.Write(body)
}

// quietGet returns the last phone sleep report (the keep process reads it).
func (a *api) quietGet(w http.ResponseWriter, r *http.Request) {
	snap := a.st.Read()
	writeJSON(w, http.StatusOK, map[string]any{"asleep": snap.Asleep})
}

// quietSet takes a phone's sleep report: ?asleep=1 / ?asleep=0, or a JSON
// body {"asleep": true}. Meant for an iOS Shortcuts automation on the Sleep
// focus; needs a token with the act or push scope.
func (a *api) quietSet(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		fail(w, http.StatusForbidden, "cross-site request")
		return
	}
	c := callerOf(r)
	if !c.scopes(config.ScopeAct) && !c.scopes(config.ScopePush) {
		fail(w, http.StatusForbidden, "需要 act 或 push 权限")
		return
	}
	var on *bool
	if v := r.URL.Query().Get("asleep"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			fail(w, http.StatusBadRequest, "asleep 只能是 1/0/true/false")
			return
		}
		on = &b
	} else {
		var body struct {
			Asleep *bool `json:"asleep"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
		on = body.Asleep
	}
	if on == nil {
		fail(w, http.StatusBadRequest, `缺少 asleep：用 ?asleep=1 或 {"asleep": true}`)
		return
	}
	rep := &state.Asleep{On: *on, At: time.Now(), By: c.name}
	a.st.Update(func(s *state.Snapshot) { s.Asleep = rep })
	log.Printf("quiet: %s says asleep=%v", c.name, *on)
	writeJSON(w, http.StatusOK, rep)
}
