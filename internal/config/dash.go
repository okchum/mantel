package config

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/okchum/mantel-kit/conf"
)

// Source is a generic data feed. Its latest JSON document is what widgets read
// with dot paths.
//
//	http_json  GET (or Method) URL every Interval; Select keeps a sub-document
//	exec       run Command every Interval; stdout is JSON (a bare number becomes {"value": n})
//	push       others POST /api/v1/push/<name>; nothing is polled
type Source struct {
	Name     string            `json:"name"`
	Type     string            `json:"type"`
	Interval Duration          `json:"interval"`
	Timeout  Duration          `json:"timeout"`
	URL      string            `json:"url"`
	Method   string            `json:"method"`
	Headers  map[string]string `json:"headers"`
	Body     string            `json:"body"`
	Insecure bool              `json:"insecure"`
	Select   string            `json:"select"`
	Command  []string          `json:"command"`
	Dir      string            `json:"dir"`
	Env      map[string]string `json:"env"`
	// StaleAfter marks the data stale (dimmed, "N 分钟前") when no update came in
	// that long. Default: 3× Interval for polled sources, off for push.
	StaleAfter Duration `json:"stale_after"`
	// Alerts push a Bark notification when a value crosses a line.
	Alerts []Rule `json:"alerts"`
}

// Rule is a threshold on a number (Above/Below) or a text value (Not).
type Rule struct {
	Title string   `json:"title"` // what the alert calls it, default "<source> <path>"
	Path  string   `json:"path"`
	Above *float64 `json:"above"`
	Below *float64 `json:"below"`
	Not   *string  `json:"not"` // alert when the text value differs, e.g. "not": "ok"
	// After is how long the condition must hold; default 0 (alerts.after does not apply).
	After Duration `json:"after"`
}

// Action is something a display can ask the server to do, like resetting a
// quota or restarting a container. Only tokens with the "act" scope may run them.
type Action struct {
	Name  string `json:"name"`
	Title string `json:"title"` // button text
	Type  string `json:"type"`  // http | exec | demo
	// http
	URL      string            `json:"url"`
	Method   string            `json:"method"` // default POST
	Headers  map[string]string `json:"headers"`
	Body     string            `json:"body"`
	Insecure bool              `json:"insecure"`
	Expect   int               `json:"expect"` // expected status; 0 = any 2xx
	// exec (no shell: arguments are passed as-is)
	Command []string          `json:"command"`
	Dir     string            `json:"dir"`
	Env     map[string]string `json:"env"`

	Timeout  Duration `json:"timeout"`  // default 20s
	Cooldown Duration `json:"cooldown"` // minimum gap between runs, default 5s
	// Confirm is how a touch screen guards the button: "hold" (default; press and
	// hold about a second), "tap" (tap twice), or "none".
	Confirm string `json:"confirm"`
	// Param lets one action serve many items: "{param}" in URL, Body, Headers and
	// Command is replaced by the value the display sends, which must match.
	Param *Param `json:"param"`
	// Refresh lists collectors to re-run after a successful run, e.g.
	// "quota:DeepSeek", "server:nas", "weather", "calendar" or a source name.
	Refresh []string `json:"refresh"`
	// Notify pushes the result through alerts (Bark) too.
	Notify bool `json:"notify"`
}

type Param struct {
	Values  []string `json:"values"`  // allowed values
	Pattern string   `json:"pattern"` // or a regexp every value must match fully

	re *regexp.Regexp // Pattern, anchored; set by Compile
}

// PatternParam returns a param accepting values that fully match pattern,
// which must be a valid regexp (it is a programming error otherwise).
func PatternParam(pattern string) *Param {
	p := &Param{Pattern: pattern}
	if err := p.Compile(); err != nil {
		panic(err)
	}
	return p
}

// Compile prepares Pattern for Allows; config loading calls it once.
func (p *Param) Compile() error {
	if p == nil || p.Pattern == "" {
		return nil
	}
	re, err := regexp.Compile("^(?:" + p.Pattern + ")$")
	if err != nil {
		return err
	}
	p.re = re
	return nil
}

// Allows reports whether v may be substituted.
func (p *Param) Allows(v string) bool {
	if p == nil {
		return v == ""
	}
	if slices.Contains(p.Values, v) {
		return true
	}
	if p.Pattern == "" {
		return false
	}
	re := p.re
	if re == nil { // not loaded through the config: compile on the spot
		var err error
		if re, err = regexp.Compile("^(?:" + p.Pattern + ")$"); err != nil {
			return false
		}
	}
	return re.MatchString(v)
}

// Display holds page-level behaviour.
type Display struct {
	Cols int `json:"cols"` // grid columns, default 12
	Rows int `json:"rows"` // grid rows, default 8
	// Home is the page shown first and returned to after IdleReturn without touch.
	Home       string   `json:"home"`
	IdleReturn Duration `json:"idle_return"` // default 90s; "0s" = never
	// Rotate cycles through pages on its own; default off. Touch pauses it.
	Rotate Duration `json:"rotate"`
	// RotateQuiet pauses rotation inside this local window, e.g. "22:00-08:00".
	RotateQuiet string `json:"rotate_quiet"`
	// TextSize scales all text: small | medium (default) | large | xlarge.
	TextSize string `json:"text_size"`
	// Theme is the colour scheme (default petrol); ThemeNight, when set, is
	// used instead while it is night. See Themes.
	Theme      string `json:"theme"`
	ThemeNight string `json:"theme_night"`
}

// TextSizes and Themes list the names the page knows; the values live in web/.
var (
	TextSizes = []string{"small", "medium", "large", "xlarge"}
	Themes    = []string{"petrol", "graphite", "midnight", "forest", "ember", "contrast", "paper"}
)

// Page is one screen of widgets.
type Page struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	// Template names a preset arrangement; widgets then pick a Slot instead of At.
	Template string `json:"template"`
	// Auto places widgets without At or Slot by itself, in order, using Span
	// (or a per-type default size). What does not fit goes onto extra pages.
	Auto    bool     `json:"auto"`
	Widgets []Widget `json:"widgets"`
}

// Widget is one tile. At is [column, row, width, height] in grid cells, from 0.
type Widget struct {
	ID     string `json:"id"` // default "<page>.<index>"
	Type   string `json:"type"`
	Title  string `json:"title"`
	At     []int  `json:"at"`
	Source string `json:"source"` // generic source name, or "server:<name>", "probe:<name>", "quota:<name>", "weather", "calendar"
	Accent string `json:"accent"` // seaglass | sodium | flare | bone | sky | plum | lichen, or any CSS colour

	// Paths into the source document. Defaults follow the push convention:
	// value="value", max="max", items="items", text="text".
	Value string `json:"value"`
	Max   string `json:"max"`
	Items string `json:"items"`
	Text  string `json:"text"`
	// Keys inside each item of Items (dot paths too). Defaults: label/value/detail/state.
	Label  string `json:"label"`
	ItemV  string `json:"item_value"`
	Detail string `json:"detail"`
	State  string `json:"state"`
	Badge  string `json:"badge"`
	Param  string `json:"param"` // item key whose value is sent as the action param

	MaxValue *float64 `json:"max_value"` // a fixed max instead of a Max path
	Unit     string   `json:"unit"`
	Decimals *int     `json:"decimals"`
	Format   string   `json:"format"` // number (default) | percent | bytes | bitrate | duration | money | int
	Scale    float64  `json:"scale"`  // multiply values by this first (e.g. 100 for ratios shown as %)
	Warn     *float64 `json:"warn"`
	Crit     *float64 `json:"crit"`
	Invert   bool     `json:"invert"` // lower is worse (battery, balance)

	Trend  bool     `json:"trend"`  // stat: sparkline + change over Window
	Window Duration `json:"window"` // history span for trend/timeseries; default 1h
	Series []Series `json:"series"` // timeseries
	Limit  int      `json:"limit"`  // max list rows
	Names  []string `json:"names"`  // servers/probes/quotas widgets: only these

	Actions []string `json:"actions"` // buttons on the tile (buttons widget: the whole tile)
	// ItemActions are offered per list/status/progress item; the item's Param key is sent.
	ItemActions []string `json:"item_actions"`

	To      string            `json:"to"`  // countdown: "2026-12-24" or "2026-12-24T18:00"
	URL     string            `json:"url"` // image: fetched by the server (so LAN cameras and secrets work)
	Headers map[string]string `json:"headers"`
	Refresh Duration          `json:"refresh"` // image refresh, default 60s
	Size    string            `json:"size"`    // text: s | m | l | xl
	Layout  string            `json:"layout"`  // weather/agenda/servers: "full" (default) | "compact"

	Slot string `json:"slot"` // template pages: which slot this widget fills
	Span []int  `json:"span"` // auto pages: [width, height] in cells; default depends on type
}

type Series struct {
	Label  string `json:"label"`
	Source string `json:"source"` // default the widget's source
	Value  string `json:"value"`  // path
	Color  string `json:"color"`
	Points string `json:"points"` // path to an array of numbers, [t,v] pairs or {t,v} objects, instead of history
}

// WidgetTypes lists what the page knows how to draw.
var WidgetTypes = []string{
	"clock", "weather", "agenda", "servers", "probes", "quotas", "plans",
	"stat", "gauge", "timeseries", "bars", "status", "list", "progress",
	"text", "countdown", "image", "buttons",
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// Scopes a token may carry.
const (
	ScopeView = "view" // see the page and data (implied by every token)
	ScopeAct  = "act"  // run actions, refresh sources, mute alerts
	ScopePush = "push" // POST /api/v1/push/<source>
)

// Can reports whether the token carries scope.
func (t Token) Can(scope string) bool {
	return scope == ScopeView || slices.Contains(t.Scopes, scope)
}

// BuiltinSources names the sources the fixed collectors provide.
func (c *Config) BuiltinSources() []string {
	var out []string
	for _, s := range c.Servers {
		out = append(out, "server:"+s.Name)
	}
	for _, p := range c.Probes {
		out = append(out, "probe:"+p.Name)
	}
	for _, q := range c.Quotas {
		out = append(out, "quota:"+q.Name)
	}
	if c.Weather != nil {
		out = append(out, "weather")
	}
	if len(c.Calendars) > 0 {
		out = append(out, "calendar")
	}
	if c.Plans != nil {
		out = append(out, PlanSource)
	}
	return out
}

func (c *Config) normalizeDash() error {
	var errs []error
	bad := func(f string, a ...any) { errs = append(errs, fmt.Errorf(f, a...)) }

	// Source names double as Kick names for POST /api/v1/refresh, so they
	// must be unique across built-in collectors and custom sources.
	known := map[string]bool{}
	for _, n := range c.BuiltinSources() {
		if known[n] {
			bad("%s: duplicate name; give each server, probe and quota a distinct name", n)
		}
		known[n] = true
	}
	if c.Demo {
		for _, n := range DemoSources {
			known[n] = true
		}
	}
	for i := range c.Sources {
		s := &c.Sources[i]
		if !nameRe.MatchString(s.Name) {
			bad("sources[%d]: name %q must be letters, digits, _ . -", i, s.Name)
			continue
		}
		if known[s.Name] {
			bad("sources[%d]: duplicate name %q", i, s.Name)
		} else if s.Name == "demo" || s.Name == "demo-sources" {
			bad("sources[%d]: name %q is reserved", i, s.Name)
		}
		known[s.Name] = true
		switch s.Type {
		case "http_json":
			if s.URL == "" {
				bad("sources[%d] (%s): url is required", i, s.Name)
			}
		case "exec":
			if len(s.Command) == 0 {
				bad("sources[%d] (%s): command is required", i, s.Name)
			}
		case "push":
		default:
			bad("sources[%d] (%s): type must be http_json, exec or push", i, s.Name)
		}
		expandMap(s.Headers)
		expandMap(s.Env)
		s.URL = conf.ExpandEnv(s.URL)
		if s.StaleAfter.Duration == 0 && s.Type != "push" {
			s.StaleAfter.Duration = 3 * s.Interval.Or(time.Minute)
		}
		for j, r := range s.Alerts {
			if r.Above == nil && r.Below == nil && r.Not == nil {
				bad("sources[%d].alerts[%d]: set above, below or not", i, j)
			}
		}
	}

	actions := map[string]bool{}
	for i := range c.Actions {
		a := &c.Actions[i]
		if !nameRe.MatchString(a.Name) || actions[a.Name] {
			bad("actions[%d]: name %q missing, malformed or duplicate", i, a.Name)
		}
		actions[a.Name] = true
		if a.Title == "" {
			a.Title = a.Name
		}
		switch a.Type {
		case "http":
			if a.URL == "" {
				bad("actions[%d] (%s): url is required", i, a.Name)
			}
			if a.Method == "" {
				a.Method = "POST"
			}
		case "exec":
			if len(a.Command) == 0 {
				bad("actions[%d] (%s): command is required", i, a.Name)
			}
		case "demo":
		default:
			bad("actions[%d] (%s): type must be http or exec", i, a.Name)
		}
		switch a.Confirm {
		case "":
			a.Confirm = "hold"
		case "hold", "tap", "none":
		default:
			bad("actions[%d] (%s): confirm must be hold, tap or none", i, a.Name)
		}
		if err := a.Param.Compile(); err != nil {
			bad("actions[%d] (%s): param.pattern: %v", i, a.Name, err)
		}
		expandMap(a.Headers)
		expandMap(a.Env)
		a.URL = conf.ExpandEnv(a.URL)
		a.Body = conf.ExpandEnv(a.Body)
		if a.Type == "http" && strings.Contains(urlAuthority(a.URL), "{param}") {
			bad("actions[%d] (%s): url: {param} may only appear in the path or query, not the scheme or host", i, a.Name)
		}
		if a.Type == "exec" && slices.ContainsFunc(shellScripts(a.Command), func(s string) bool { return strings.Contains(s, "{param}") }) {
			bad(`actions[%d] (%s): command: {param} inside a shell -c script can inject commands; pass it as an argument instead, e.g. ["sh","-c","docker restart \"$1\"","sh","{param}"]`, i, a.Name)
		}
		for _, r := range a.Refresh {
			if !known[r] && r != "demo" {
				bad("actions[%d] (%s): refresh: unknown source %q", i, a.Name, r)
			}
		}
	}
	if c.Demo {
		for _, n := range DemoActions {
			actions[n] = true
		}
	}
	if c.Plans.ResetEnabled() {
		actions[PlanResetAction] = true
	}
	checkActions := func(where string, names []string) {
		for _, n := range names {
			if !actions[n] {
				bad("%s: unknown action %q", where, n)
			}
		}
	}
	for _, s := range c.Servers {
		checkActions("servers "+s.Name, s.Actions)
	}
	for _, p := range c.Probes {
		checkActions("probes "+p.Name, p.Actions)
	}
	for _, q := range c.Quotas {
		checkActions("quotas "+q.Name, q.Actions)
	}

	d := &c.Display
	if d.Cols <= 0 {
		d.Cols = 12
	}
	if d.Rows <= 0 {
		d.Rows = 8
	}
	if d.IdleReturn.Duration == 0 {
		d.IdleReturn.Duration = 90 * time.Second
	}
	if d.RotateQuiet != "" {
		if _, _, err := conf.ParseWindow(d.RotateQuiet); err != nil {
			bad("display.rotate_quiet: %v", err)
		}
	}
	if d.TextSize == "" {
		d.TextSize = "medium"
	} else if !slices.Contains(TextSizes, d.TextSize) {
		bad("display.text_size: %q is not one of %s", d.TextSize, strings.Join(TextSizes, ", "))
	}
	if d.Theme == "" {
		d.Theme = "petrol"
	} else if !slices.Contains(Themes, d.Theme) {
		bad("display.theme: %q is not one of %s", d.Theme, strings.Join(Themes, ", "))
	}
	if d.ThemeNight != "" && !slices.Contains(Themes, d.ThemeNight) {
		bad("display.theme_night: %q is not one of %s", d.ThemeNight, strings.Join(Themes, ", "))
	}

	c.Pages = arrangePages(c.Pages, d.Cols, d.Rows, bad)

	pages := map[string]bool{}
	ids := map[string]bool{}
	for pi := range c.Pages {
		p := &c.Pages[pi]
		if p.Name == "" {
			p.Name = fmt.Sprintf("page%d", pi+1)
		}
		if pages[p.Name] {
			bad("pages[%d]: duplicate name %q", pi, p.Name)
		}
		pages[p.Name] = true
		if p.Title == "" {
			p.Title = p.Name
		}
		occupied := map[[2]int]string{}
		for wi := range p.Widgets {
			w := &p.Widgets[wi]
			where := fmt.Sprintf("pages %s widgets[%d] (%s)", p.Name, wi, w.Type)
			if w.ID == "" {
				w.ID = fmt.Sprintf("%s.%d", p.Name, wi)
			}
			if ids[w.ID] {
				bad("%s: duplicate id %q", where, w.ID)
			}
			ids[w.ID] = true
			if !slices.Contains(WidgetTypes, w.Type) {
				bad("%s: unknown type; have %s", where, strings.Join(WidgetTypes, ", "))
			}
			if len(w.At) != 4 || w.At[2] < 1 || w.At[3] < 1 || w.At[0] < 0 || w.At[1] < 0 ||
				w.At[0]+w.At[2] > d.Cols || w.At[1]+w.At[3] > d.Rows {
				bad("%s: at %v must be [col,row,w,h] inside the %dx%d grid", where, w.At, d.Cols, d.Rows)
			} else {
			cells:
				for x := w.At[0]; x < w.At[0]+w.At[2]; x++ {
					for y := w.At[1]; y < w.At[1]+w.At[3]; y++ {
						if o, ok := occupied[[2]int{x, y}]; ok {
							bad("%s: overlaps %s at cell [%d,%d]", where, o, x, y)
							break cells
						}
						occupied[[2]int{x, y}] = w.ID
					}
				}
			}
			if w.Source != "" && !known[w.Source] {
				bad("%s: unknown source %q", where, w.Source)
			}
			for si, s := range w.Series {
				if s.Source != "" && !known[s.Source] {
					bad("%s: series[%d]: unknown source %q", where, si, s.Source)
				}
				if s.Source == "" && w.Source == "" {
					bad("%s: series[%d]: no source", where, si)
				}
			}
			needsSource := map[string]bool{"stat": true, "gauge": true, "bars": true, "list": true, "progress": true}
			if needsSource[w.Type] && w.Source == "" {
				bad("%s: source is required", where)
			}
			if w.Type == "status" && w.Source == "" && len(w.Names) == 0 {
				bad("%s: source is required", where)
			}
			if w.Type == "plans" && c.Plans == nil && !c.Demo {
				bad("%s: plans widgets need a \"plans\" section (CLIProxyAPI)", where)
			}
			if w.Type == "countdown" && w.To == "" {
				bad("%s: to is required", where)
			}
			if w.Type == "image" && w.URL == "" {
				bad("%s: url is required", where)
			}
			expandMap(w.Headers)
			w.URL = conf.ExpandEnv(w.URL)
			checkActions(where, w.Actions)
			checkActions(where, w.ItemActions)
		}
	}
	if d.Home == "" && len(c.Pages) > 0 {
		d.Home = c.Pages[0].Name
	}
	if d.Home != "" && len(c.Pages) > 0 && !pages[d.Home] {
		bad("display.home: no page %q", d.Home)
	}

	for i := range c.Tokens {
		for _, s := range c.Tokens[i].Scopes {
			if s != ScopeView && s != ScopeAct && s != ScopePush {
				bad("tokens[%d] (%s): unknown scope %q (have view, act, push)", i, c.Tokens[i].Name, s)
			}
		}
	}
	return errors.Join(errs...)
}

// DemoActions are the actions demo mode adds (defined in internal/dash).
var DemoActions = []string{
	"demo-topup", "demo-restart", "demo-pause", "demo-rerun", "demo-done",
	"demo-lights", "demo-fan", "demo-gate", PlanResetAction,
}

// DemoSources are the generic sources the demo generator fills.
var DemoSources = []string{
	"power", "climate", "containers", "downloads", "github", "todo", "orders",
	"backup", "fx", "notes", "ai_usage", PlanSource,
}

func expandMap(m map[string]string) {
	for k, v := range m {
		m[k] = conf.ExpandEnv(v)
	}
}

// urlAuthority returns the scheme and host part of a URL ("https://h:1" of
// "https://h:1/p?q"), where a substituted value could redirect the request.
func urlAuthority(u string) string {
	rest := u
	if i := strings.Index(u, "://"); i >= 0 {
		rest = u[i+3:]
	}
	if j := strings.IndexAny(rest, "/?#"); j >= 0 {
		return u[:len(u)-len(rest)+j]
	}
	return u
}

// shellScripts returns the inline scripts a command runs through a shell's
// -c flag. Rather than parse every wrapper's options (env -u X, nice -n 5,
// timeout 5, sudo -u, busybox, docker exec …), it treats each argument named
// like a shell (sh, bash, …, by base name) as the start of a shell command
// line, reads that shell's options, and takes the first non-option after a
// -c (also combined, like -ec or -lc) as its script. A shell given a script
// file instead ends its options there, so ["bash","./restart.sh","-c","x"]
// has no inline script. Only shells count: "-c" means something else to
// other programs (ping -c 3, curl -c jar), where a {param} is just an argument.
func shellScripts(cmd []string) []string {
	var out []string
	for i, arg := range cmd {
		switch path.Base(arg) {
		case "sh", "bash", "zsh", "dash", "ash", "ksh", "mksh", "fish":
			if s, ok := shellC(cmd[i+1:]); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// shellC reads a shell's arguments and returns the script of its -c flag.
func shellC(args []string) (string, bool) {
	c := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "-o", "+o", "-O", "+O", "--rcfile", "--init-file":
			i++ // the option's own argument, e.g. -o pipefail
			continue
		}
		if strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "+") {
			c = c || !strings.HasPrefix(arg, "--") && strings.Contains(arg, "c")
			continue
		}
		return arg, c // the script, or a script file: options end here
	}
	return "", false
}
