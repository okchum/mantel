package dash

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/state"
)

var now = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// one builds a dashboard with a single page holding widgets, each source
// listed as a push source.
func one(sources []config.Source, widgets ...config.Widget) *Dash {
	for i := range widgets {
		if widgets[i].ID == "" {
			widgets[i].ID = "w" + string(rune('0'+i))
		}
		if widgets[i].At == nil {
			widgets[i].At = []int{i, 0, 1, 1}
		}
	}
	return New(&config.Config{
		Title:   "T",
		Sources: sources,
		Actions: []config.Action{{Name: "go", Title: "Go", Confirm: "hold", Type: "demo"}},
		Display: config.Display{Cols: 12, Rows: 8, Home: "p"},
		Pages:   []config.Page{{Name: "p", Title: "P", Widgets: widgets}},
	})
}

func snap(srcs ...state.Source) state.Snapshot {
	return state.Snapshot{Now: now, Sources: srcs, Hist: map[string][]state.Sample{}}
}

func src(name, js string, t *testing.T) state.Source {
	return state.Source{Name: name, Data: decode(t, js), Updated: now.Add(-time.Minute)}
}

func data(t *testing.T, d *Dash, s state.Snapshot, id string) map[string]any {
	t.Helper()
	r := d.Build(s, "", "", false)
	m, ok := r.Data[id].(map[string]any)
	if !ok {
		t.Fatalf("data[%s] = %#v", id, r.Data[id])
	}
	return m
}

func TestStatValueAndState(t *testing.T) {
	doc := `{"value": 70, "nested": {"temp": "21.5"}, "ratio": 0.42, "word": "hi"}`
	cases := []struct {
		name      string
		w         config.Widget
		wantVal   any
		wantState any
		wantErr   string
	}{
		{"plain", config.Widget{}, 70.0, nil, ""},
		{"path and numeric string", config.Widget{Value: "nested.temp"}, 21.5, nil, ""},
		{"scale", config.Widget{Value: "ratio", Scale: 100}, 42.0, nil, ""},
		{"ok", config.Widget{Warn: f(80), Crit: f(90)}, 70.0, "ok", ""},
		{"warn at line", config.Widget{Warn: f(70), Crit: f(90)}, 70.0, "warn", ""},
		{"crit wins", config.Widget{Warn: f(10), Crit: f(60)}, 70.0, "crit", ""},
		{"only crit", config.Widget{Crit: f(100)}, 70.0, "ok", ""},
		{"invert ok", config.Widget{Invert: true, Warn: f(50), Crit: f(20)}, 70.0, "ok", ""},
		{"invert warn", config.Widget{Invert: true, Warn: f(70), Crit: f(20)}, 70.0, "warn", ""},
		{"invert crit", config.Widget{Invert: true, Warn: f(90), Crit: f(80)}, 70.0, "crit", ""},
		{"scaled before threshold", config.Widget{Value: "ratio", Scale: 100, Warn: f(40)}, 42.0, "warn", ""},
		{"not a number", config.Widget{Value: "word"}, nil, nil, "is not a number"},
		{"missing", config.Widget{Value: "nope"}, nil, nil, `no "nope"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := c.w
			w.Type, w.Source = "stat", "s"
			d := one(nil, w)
			p := data(t, d, snap(src("s", doc, t)), "w0")
			if p["value"] != c.wantVal {
				t.Errorf("value = %#v, want %#v", p["value"], c.wantVal)
			}
			if p["state"] != c.wantState {
				t.Errorf("state = %#v, want %#v", p["state"], c.wantState)
			}
			if c.wantErr == "" && p["error"] != nil || c.wantErr != "" && !strings.Contains(p["error"].(string), c.wantErr) {
				t.Errorf("error = %v, want %q", p["error"], c.wantErr)
			}
			if p["updated"] != now.Add(-time.Minute) {
				t.Errorf("updated = %v", p["updated"])
			}
		})
	}
}

func TestGaugeMax(t *testing.T) {
	doc := `{"value": 0.5, "max": 2, "cap": 8}`
	cases := []struct {
		name string
		w    config.Widget
		want float64
	}{
		{"max path", config.Widget{}, 2},
		{"custom max path", config.Widget{Max: "cap"}, 8},
		{"max path scaled", config.Widget{Max: "cap", Scale: 10}, 80},
		{"fixed max wins", config.Widget{MaxValue: f(5)}, 5},
		{"default 100", config.Widget{Max: "none"}, 100},
		{"percent defaults to 1", config.Widget{Max: "none", Format: "percent"}, 1},
		{"percent with scale uses 100", config.Widget{Max: "none", Format: "percent", Scale: 100}, 100},
	}
	for _, c := range cases {
		w := c.w
		w.Type, w.Source = "gauge", "s"
		p := data(t, one(nil, w), snap(src("s", doc, t)), "w0")
		if p["max"] != c.want {
			t.Errorf("%s: max = %v, want %v", c.name, p["max"], c.want)
		}
	}
}

func TestPayloadFlags(t *testing.T) {
	sources := []config.Source{{Name: "s", Type: "push", StaleAfter: dur(5 * time.Minute)}}
	w := config.Widget{Type: "stat", Source: "s"}
	cases := []struct {
		name string
		src  state.Source
		want map[string]any
	}{
		{"waiting: never reported", state.Source{Name: "s"},
			map[string]any{"waiting": true}},
		{"error without data", state.Source{Name: "s", Error: "HTTP 500"},
			map[string]any{"error": "HTTP 500"}},
		{"error keeps last data", state.Source{Name: "s", Data: map[string]any{"value": 3.0}, Updated: now.Add(-time.Minute), Error: "timeout"},
			map[string]any{"error": "timeout", "updated": now.Add(-time.Minute), "value": 3.0}},
		{"fresh", state.Source{Name: "s", Data: map[string]any{"value": 3.0}, Updated: now.Add(-4 * time.Minute)},
			map[string]any{"updated": now.Add(-4 * time.Minute), "value": 3.0}},
		{"stale", state.Source{Name: "s", Data: map[string]any{"value": 3.0}, Updated: now.Add(-6 * time.Minute)},
			map[string]any{"updated": now.Add(-6 * time.Minute), "value": 3.0, "stale": true}},
	}
	for _, c := range cases {
		p := data(t, one(sources, w), snap(c.src), "w0")
		if !reflect.DeepEqual(p, c.want) {
			t.Errorf("%s: payload = %#v, want %#v", c.name, p, c.want)
		}
	}
	// A source missing from the snapshot altogether is waiting too.
	if p := data(t, one(sources, w), snap(), "w0"); !reflect.DeepEqual(p, map[string]any{"waiting": true}) {
		t.Errorf("absent source payload = %#v", p)
	}
	// Without stale_after nothing is ever stale.
	old := state.Source{Name: "s", Data: map[string]any{"value": 1.0}, Updated: now.Add(-48 * time.Hour)}
	if p := data(t, one(nil, w), snap(old), "w0"); p["stale"] != nil {
		t.Errorf("stale without stale_after: %#v", p)
	}
}

func items(t *testing.T, p map[string]any) []Item {
	t.Helper()
	its, ok := p["items"].([]Item)
	if !ok {
		t.Fatalf("items = %#v (payload %#v)", p["items"], p)
	}
	return its
}

func TestListItems(t *testing.T) {
	doc := `{
		"items": [
			{"label": "a", "value": 5, "detail": "d", "badge": "new", "state": "running", "id": "x1"},
			{"label": "b", "value": "12", "state": false},
			{"name": "c", "v": {"n": 3}, "s": "weird"}
		],
		"objs": {"zeta": 1, "alpha": {"value": 2, "state": "down"}, "mid": "ok"},
		"flat": ["x", 9],
		"notalist": 5
	}`
	s := snap(src("s", doc, t))

	// Array of objects with default keys.
	p := data(t, one(nil, config.Widget{Type: "list", Source: "s", Warn: f(10)}), s, "w0")
	got := items(t, p)
	if len(got) != 3 || p["total"] != 3 {
		t.Fatalf("items = %+v total %v", got, p["total"])
	}
	a, b := got[0], got[1]
	if a.Label != "a" || a.Value != 5.0 || *a.Num != 5 || a.Detail != "d" || a.Badge != "new" || a.State != "ok" || a.Param != "" {
		t.Errorf("item a = %+v", a)
	}
	// "12" is a number for Num; the explicit state (false → crit) beats the threshold.
	if b.Num == nil || *b.Num != 12 || b.State != "crit" {
		t.Errorf("item b = %+v", b)
	}
	if got[2].Label != "" || got[2].Num != nil || got[2].Value != nil {
		t.Errorf("item c with default keys = %+v", got[2])
	}

	// Custom item keys, param key and item actions.
	w := config.Widget{Type: "status", Source: "s", Label: "name", ItemV: "v.n", State: "s", Param: "name", ItemActions: []string{"go"}}
	got = items(t, data(t, one(nil, w), s, "w0"))
	if c := got[2]; c.Label != "c" || *c.Num != 3 || c.State != "weird" || c.Param != "c" {
		t.Errorf("custom keys item = %+v", c)
	}
	if got[0].Param != "" {
		t.Errorf("param key missing on item should stay empty, got %q", got[0].Param)
	}

	// Object map: keys sorted, key is the label, item_actions without a param key send the label.
	w = config.Widget{Type: "list", Source: "s", Items: "objs", ItemActions: []string{"go"}, Warn: f(1)}
	got = items(t, data(t, one(nil, w), s, "w0"))
	var labels []string
	for _, it := range got {
		labels = append(labels, it.Label+"="+it.State+"/"+it.Param)
	}
	if want := "alpha=crit/alpha,mid=/mid,zeta=warn/zeta"; strings.Join(labels, ",") != want {
		t.Errorf("object items = %v, want %s", labels, want)
	}

	// Scalars: status maps them to a state; list does not.
	got = items(t, data(t, one(nil, config.Widget{Type: "status", Source: "s", Items: "flat"}), s, "w0"))
	if got[0].Value != "x" || got[0].State != "x" || *got[1].Num != 9 {
		t.Errorf("status scalars = %+v", got)
	}
	got = items(t, data(t, one(nil, config.Widget{Type: "list", Source: "s", Items: "flat"}), s, "w0"))
	if got[0].State != "" {
		t.Errorf("list scalar state = %q", got[0].State)
	}

	// Limit keeps total.
	p = data(t, one(nil, config.Widget{Type: "progress", Source: "s", Limit: 2}), s, "w0")
	if len(items(t, p)) != 2 || p["total"] != 3 {
		t.Errorf("limit: %d items, total %v", len(items(t, p)), p["total"])
	}

	// Not a list / missing.
	p = data(t, one(nil, config.Widget{Type: "list", Source: "s", Items: "notalist"}), s, "w0")
	if p["error"] != "notalist is not a list" || p["total"] != 0 {
		t.Errorf("not a list payload = %#v", p)
	}
	p = data(t, one(nil, config.Widget{Type: "bars", Source: "s", Items: "gone"}), s, "w0")
	if e, _ := p["error"].(string); !strings.Contains(e, `no "gone"`) {
		t.Errorf("missing items payload = %#v", p)
	}
}

func TestBoolItemValueHasNoNum(t *testing.T) {
	s := snap(src("s", `{"items":[{"label":"x","value":true}]}`, t))
	got := items(t, data(t, one(nil, config.Widget{Type: "list", Source: "s"}), s, "w0"))
	if got[0].Num != nil || got[0].Value != true {
		t.Errorf("bool value item = %+v", got[0])
	}
}

func TestNormState(t *testing.T) {
	cases := map[any]string{
		true: "ok", false: "crit", nil: "",
		"OK": "ok", " Healthy ": "ok", "running": "ok", 1.0: "ok",
		"degraded": "warn", "in_progress": "warn", "paused": "warn",
		"down": "crit", "Exited": "crit", 0.0: "crit", "timed_out": "crit",
		"stopped": "off", "idle": "off",
		"Something Else": "something else", 2.0: "2",
	}
	for in, want := range cases {
		if got := NormState(in); got != want {
			t.Errorf("NormState(%#v) = %q, want %q", in, got, want)
		}
	}
}

func TestTextWidget(t *testing.T) {
	s := snap(src("s", `{"text":"hello","n":{"m":4}}`, t))
	if p := data(t, one(nil, config.Widget{Type: "text", Text: "static"}), s, "w0"); p["text"] != "static" {
		t.Errorf("static text = %#v", p)
	}
	if p := data(t, one(nil, config.Widget{Type: "text", Source: "s"}), s, "w0"); p["text"] != "hello" {
		t.Errorf("source text = %#v", p)
	}
	if p := data(t, one(nil, config.Widget{Type: "text", Source: "s", Text: "n"}), s, "w0"); p["text"] != `{"m":4}` {
		t.Errorf("object text = %#v", p)
	}
}

func TestLayoutVersionAndCanAct(t *testing.T) {
	mk := func(title string) *Dash {
		return one([]config.Source{{Name: "s", Type: "push"}},
			config.Widget{Type: "stat", Source: "s", Title: title, Trend: true},
			config.Widget{Type: "image", URL: "http://cam/x.jpg"},
		)
	}
	a, b, c := mk("A"), mk("A"), mk("B")
	if a.Version() == "" || len(a.Version()) != 12 {
		t.Fatalf("version = %q", a.Version())
	}
	if a.Version() != b.Version() {
		t.Errorf("same config, different versions: %s %s", a.Version(), b.Version())
	}
	if a.Version() == c.Version() {
		t.Error("changed config kept the version")
	}
	if a.Layout(true).Version != a.Version() || !a.Layout(true).CanAct || a.Layout(false).CanAct {
		t.Error("Layout(canAct) must keep the version and set CanAct")
	}
	l := a.Layout(false)
	if l.Grid != [2]int{12, 8} || l.Home != "p" || l.Title != "T" {
		t.Errorf("layout = %+v", l)
	}
	if av := l.Actions["go"]; av != (ActionView{Title: "Go", Confirm: "hold"}) {
		t.Errorf("action view = %+v", av)
	}
	ws := l.Pages[0].Widgets
	if ws[0].Window != 3600 || ws[1].Refresh != 60 || ws[0].Refresh != 0 || ws[1].Window != 0 {
		t.Errorf("widget views = %+v", ws)
	}

	s := snap(src("s", `{"value":1}`, t))
	r := a.Build(s, "", "", true)
	if r.Layout == nil || r.Version != a.Version() || !r.Layout.CanAct {
		t.Fatalf("Build without version must send the layout: %+v", r.Layout)
	}
	if r := a.Build(s, "stale-version", "", false); r.Layout == nil || r.Layout.CanAct {
		t.Error("mismatched version must send the layout")
	}
	if r := a.Build(s, a.Version(), "", true); r.Layout != nil {
		t.Error("matching version must omit the layout")
	}
	if img := r.Data["w1"].(map[string]any); img["src"] != "api/v1/image/w1" {
		t.Errorf("image data = %#v", img)
	}
	// The version survives a JSON round trip of the response.
	b2, _ := json.Marshal(r)
	if !strings.Contains(string(b2), `"version":"`+a.Version()+`"`) {
		t.Errorf("response JSON lacks version: %s", b2)
	}
}

func TestBuildPageFilterAndActions(t *testing.T) {
	cfg := &config.Config{Pages: []config.Page{
		{Name: "a", Widgets: []config.Widget{{ID: "a.0", Type: "clock", At: []int{0, 0, 1, 1}}}},
		{Name: "b", Widgets: []config.Widget{{ID: "b.0", Type: "buttons", At: []int{0, 0, 1, 1}}}},
	}}
	d := New(cfg)
	s := snap()
	s.Actions = map[string]state.ActionResult{"go:x": {OK: true}}
	r := d.Build(s, "", "b", false)
	if _, ok := r.Data["a.0"]; ok || len(r.Data) != 1 {
		t.Errorf("page filter: %v", r.Data)
	}
	if r.Actions["go:x"].OK != true || !r.Now.Equal(now) {
		t.Errorf("response passthrough = %+v", r)
	}
	if len(d.Build(s, "", "", false).Data) != 2 {
		t.Error("empty page must return every widget")
	}
	if w, ok := d.Widget("b.0"); !ok || w.Type != "buttons" {
		t.Errorf("Widget(b.0) = %+v %v", w, ok)
	}
	if _, ok := d.Widget("zz"); ok {
		t.Error("Widget(zz) found")
	}
	// Defaults applied by New for an unnormalized display.
	if l := d.Layout(false); l.Grid != [2]int{12, 8} {
		t.Errorf("grid = %v", l.Grid)
	}
}

func TestNewClassicAndDemo(t *testing.T) {
	d := New(&config.Config{Probes: []config.Probe{{Name: "web", URL: "http://x"}}})
	l := d.Layout(false)
	if len(l.Pages) != 1 || l.Home != "main" {
		t.Fatalf("classic layout = %+v", l)
	}
	if len(d.Actions()) != 0 {
		t.Errorf("classic actions = %v", d.Actions())
	}

	dm := New(&config.Config{Demo: true, Actions: []config.Action{{Name: "demo-fan", Title: "Mine", Type: "http"}}})
	acts := dm.Actions()
	if len(acts) < 2 {
		t.Fatalf("demo actions = %v", acts)
	}
	for i := 1; i < len(acts); i++ {
		if acts[i-1].Name >= acts[i].Name {
			t.Errorf("Actions not sorted: %s >= %s", acts[i-1].Name, acts[i].Name)
		}
	}
	for _, a := range acts {
		if a.Name == "demo-fan" && a.Title != "Mine" {
			t.Errorf("configured action must win over the demo one: %+v", a)
		}
	}
	if len(dm.Layout(false).Pages) != 4 || dm.Layout(false).Home != "main" {
		t.Errorf("demo pages = %d", len(dm.Layout(false).Pages))
	}
}

func TestTracksFor(t *testing.T) {
	pages := []config.Page{
		{Widgets: []config.Widget{
			{Type: "stat", Source: "s", Trend: true},                              // s|value 1h
			{Type: "stat", Source: "s", Trend: false, Value: "other"},             // no trend: not tracked
			{Type: "gauge", Source: "s", Trend: true, Window: dur(3 * time.Hour)}, // s|value 3h (longest)
			{Type: "list", Source: "s", Trend: true},                              // not a chart
			{Type: "timeseries", Source: "t", Window: dur(10 * time.Minute)},      // t|value 10m
			{Type: "timeseries", Source: "u", Window: dur(2 * time.Hour), Series: []config.Series{
				{Value: "a"},                  // u|a
				{Source: "s", Value: "value"}, // s|value 2h (shorter than 3h)
				{Source: "v", Points: "pts"},  // points: no history
			}},
		}},
		{Widgets: []config.Widget{
			{Type: "timeseries", Source: "u", Window: dur(30 * time.Minute), Series: []config.Series{{Value: "a"}}}, // u|a keeps 2h
		}},
	}
	got := tracksFor(pages)
	want := []track{
		{"s", "value", 3 * time.Hour},
		{"t", "value", 10 * time.Minute},
		{"u", "a", 2 * time.Hour},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tracksFor = %+v, want %+v", got, want)
	}
}

func newStore(names ...string) *state.Store {
	return state.NewStore("t", "", nil, nil, nil, names)
}

func setSrc(st *state.Store, name string, doc any, updated time.Time, errMsg string) {
	st.Update(func(s *state.Snapshot) {
		i := s.SourceIndex(name)
		s.Sources[i].Data, s.Sources[i].Updated, s.Sources[i].Error = doc, updated, errMsg
	})
}

func TestSample(t *testing.T) {
	d := one(nil, config.Widget{Type: "stat", Source: "s", Trend: true, Value: "v"}) // window 1h → min step 10s
	st := newStore("s", "e", "n")
	st.Update(func(s *state.Snapshot) { s.Hist[HistKey("gone", "x")] = []state.Sample{{At: now, V: 1}} })
	key := HistKey("s", "v")
	hist := func() []state.Sample { return st.Read().Hist[key] }

	setSrc(st, "s", map[string]any{"v": 1.0}, now, "")
	d.sample(st, now)
	if h := hist(); len(h) != 1 || h[0].V != 1 || !h[0].At.Equal(now) {
		t.Fatalf("first sample = %+v", h)
	}
	if _, ok := st.Read().Hist[HistKey("gone", "x")]; ok {
		t.Error("untracked history not dropped")
	}

	// Same update time: nothing new.
	setSrc(st, "s", map[string]any{"v": 2.0}, now, "")
	d.sample(st, now.Add(time.Second))
	// Newer but closer than window/360: skipped.
	setSrc(st, "s", map[string]any{"v": 3.0}, now.Add(5*time.Second), "")
	d.sample(st, now.Add(5*time.Second))
	if h := hist(); len(h) != 1 {
		t.Fatalf("too-close samples were added: %+v", h)
	}
	// Far enough: added.
	setSrc(st, "s", map[string]any{"v": 4.0}, now.Add(20*time.Second), "")
	d.sample(st, now.Add(20*time.Second))
	// Not a number: skipped.
	setSrc(st, "s", map[string]any{"v": "n/a"}, now.Add(40*time.Second), "")
	d.sample(st, now.Add(40*time.Second))
	if h := hist(); len(h) != 2 || h[1].V != 4 {
		t.Fatalf("after second sample = %+v", h)
	}

	// Older than the window: trimmed.
	later := now.Add(61 * time.Minute)
	setSrc(st, "s", map[string]any{"v": 5.0}, later, "")
	d.sample(st, later)
	if h := hist(); len(h) != 1 || h[0].V != 5 {
		t.Fatalf("after trim = %+v", h)
	}
}

func TestSampleSkipsErrorsAndWaiting(t *testing.T) {
	d := one(nil,
		config.Widget{Type: "stat", Source: "e", Trend: true},
		config.Widget{Type: "stat", Source: "n", Trend: true},
		config.Widget{Type: "stat", Source: "z", Trend: true},
		config.Widget{Type: "stat", Source: "k", Trend: true},
	)
	st := newStore("e", "n", "k")
	setSrc(st, "e", map[string]any{"value": 1.0}, time.Time{}, "boom") // error, never updated
	setSrc(st, "k", map[string]any{"value": 7.0}, time.Time{}, "")     // data without a time: sampled at now
	d.sample(st, now)
	h := st.Read().Hist
	if len(h) != 1 || len(h[HistKey("k", "value")]) != 1 || !h[HistKey("k", "value")][0].At.Equal(now) {
		t.Errorf("hist = %+v", h)
	}
}

func TestSampleBackfill(t *testing.T) {
	d := one(nil, config.Widget{Type: "timeseries", Source: "s"})
	d.Backfill = true
	st := newStore("s")
	setSrc(st, "s", map[string]any{"value": 50.0}, now, "")
	d.sample(st, now)
	h := st.Read().Hist[HistKey("s", "value")]
	if len(h) != 121 {
		t.Fatalf("backfilled %d samples, want 121", len(h))
	}
	if !h[0].At.Equal(now.Add(-time.Hour)) || !h[120].At.Equal(now) || h[120].V != 50 {
		t.Errorf("backfill span %s..%s last %v", h[0].At, h[120].At, h[120].V)
	}
	for i := 1; i < len(h); i++ {
		if !h[i].At.After(h[i-1].At) {
			t.Fatalf("samples out of order at %d", i)
		}
		if h[i].V < 50-50*0.06*1.45-1e-9 || h[i].V > 50+50*0.06*1.45+1e-9 {
			t.Fatalf("backfill sample %d = %v strays too far", i, h[i].V)
		}
	}
	// Only on first sight.
	setSrc(st, "s", map[string]any{"value": 51.0}, now.Add(time.Minute), "")
	d.sample(st, now.Add(time.Minute))
	// One added; the two backfilled samples older than now+1m-1h trimmed.
	if n := len(st.Read().Hist[HistKey("s", "value")]); n != 120 {
		t.Errorf("second sample: %d samples, want 120", n)
	}

	// Zero values still wander (amp 1) rather than staying flat.
	b := backfill(now, time.Hour, 0)
	flat := true
	for _, s := range b {
		if s.V != 0 {
			flat = false
		}
	}
	if len(b) != 120 || flat {
		t.Errorf("backfill(0) len %d flat %v", len(b), flat)
	}
}

func TestTrendSparkAndDelta(t *testing.T) {
	d := one(nil, config.Widget{Type: "stat", Source: "s", Trend: true, Scale: 2, Window: dur(30 * time.Minute)})
	s := snap(src("s", `{"value": 10}`, t))
	s.Hist[HistKey("s", "value")] = []state.Sample{
		{At: now.Add(-time.Hour), V: 1}, // outside the window
		{At: now.Add(-20 * time.Minute), V: 3},
		{At: now.Add(-10 * time.Minute), V: 4},
	}
	p := data(t, d, s, "w0")
	if !reflect.DeepEqual(p["spark"], []float64{6, 8}) || p["delta"] != 14.0 || p["value"] != 20.0 {
		t.Errorf("trend payload = %#v", p)
	}
	// One point: spark but no delta.
	s.Hist[HistKey("s", "value")] = s.Hist[HistKey("s", "value")][2:]
	p = data(t, d, s, "w0")
	if _, ok := p["delta"]; ok || len(p["spark"].([]float64)) != 1 {
		t.Errorf("single point payload = %#v", p)
	}
}

func TestTimeseries(t *testing.T) {
	d := one(nil, config.Widget{Type: "timeseries", Source: "s", Title: "T", Window: dur(time.Hour), Scale: 10,
		Series: []config.Series{
			{Label: "hist", Value: "v"},
			{Source: "o", Points: "pts", Color: "sky"},
			{Label: "bad", Points: "nope"},
		}})
	s := snap(src("s", `{"v":1}`, t), src("o", `{"pts":[[1714564800,1],{"t":"2024-05-01T12:01:40Z","value":2},[1714564700000,"3"],"junk"]}`, t))
	s.Hist[HistKey("s", "v")] = []state.Sample{{At: now.Add(-time.Minute), V: 2}}
	p := data(t, d, s, "w0")
	series := p["series"].([]map[string]any)
	if len(series) != 3 || p["window"] != 3600.0 {
		t.Fatalf("series = %#v", p)
	}
	if pts := series[0]["points"].([][2]float64); len(pts) != 1 || pts[0][1] != 20 || series[0]["last"] != 20.0 || series[0]["label"] != "hist" {
		t.Errorf("history series = %#v", series[0])
	}
	pts := series[1]["points"].([][2]float64)
	want := [][2]float64{{1714564700000, 30}, {1714564800000, 10}, {1714564900000, 20}}
	if !reflect.DeepEqual(pts, want) || series[1]["label"] != "o" || series[1]["color"] != "sky" {
		t.Errorf("points series = %#v, want %v", series[1], want)
	}
	if e, _ := series[2]["error"].(string); !strings.Contains(e, `no "nope"`) || len(series[2]["points"].([][2]float64)) != 0 {
		t.Errorf("bad series = %#v", series[2])
	}
}

func TestParsePointsBareValues(t *testing.T) {
	pts := parsePoints([]any{1.0, "2", true, "x"}, now)
	if len(pts) != 3 {
		t.Fatalf("pts = %v", pts)
	}
	// Spread one minute apart, ending now (index 3 is the last slot).
	if pts[0][0] != float64(now.Add(-3*time.Minute).UnixMilli()) || pts[2][0] != float64(now.Add(-time.Minute).UnixMilli()) {
		t.Errorf("times = %v", pts)
	}
	if parsePoints("nope", now) != nil {
		t.Error("non-array must give nil")
	}
}

func TestDownsample(t *testing.T) {
	if got := downsample(nil, 5); got == nil || len(got) != 0 {
		t.Errorf("downsample(nil) = %#v, want empty non-nil", got)
	}
	var pts [][2]float64
	for i := range 10 {
		pts = append(pts, [2]float64{float64(i), float64(i)})
	}
	if got := downsample(pts, 20); len(got) != 10 {
		t.Errorf("short input changed: %v", got)
	}
	got := downsample(pts, 5)
	want := [][2]float64{{1, 0.5}, {3, 2.5}, {5, 4.5}, {7, 6.5}, {9, 8.5}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("downsample = %v, want %v", got, want)
	}
}

func TestBuiltinSourcesAsDocs(t *testing.T) {
	d := one(nil,
		config.Widget{Type: "stat", Source: "server:nas", Value: "cpu", Scale: 100},
		config.Widget{Type: "stat", Source: "probe:web", Value: "latency_ms"},
		config.Widget{Type: "stat", Source: "quota:ds", Value: "value"},
		config.Widget{Type: "stat", Source: "weather", Value: "temp"},
		config.Widget{Type: "stat", Source: "calendar", Value: "count"},
		config.Widget{Type: "status", Names: []string{"web"}},
	)
	s := snap()
	s.Servers = []state.Server{{Name: "nas", CPU: 0.25, Updated: now}}
	s.Probes = []state.Probe{{Name: "web", Up: true, Checked: true, LatencyMS: 12, At: now}, {Name: "other"}}
	s.Quotas = []state.Quota{{Name: "ds", Value: 9, Error: "late"}}
	s.Weather = &state.Weather{Temp: 18}
	s.Events = []state.Event{{Title: "x"}, {Title: "y"}}
	r := d.Build(s, "", "", false)
	vals := []any{25.0, 12.0, 9.0, 18.0, 2.0}
	for i, want := range vals {
		id := "w" + string(rune('0'+i))
		if got := r.Data[id].(map[string]any)["value"]; got != want {
			t.Errorf("%s value = %v, want %v", id, got, want)
		}
	}
	if e := r.Data["w2"].(map[string]any)["error"]; e != "late" {
		t.Errorf("quota error = %v", e)
	}
	st := r.Data["w5"].(map[string]any)
	its := st["items"].([]Item)
	if len(its) != 1 || its[0].State != "ok" || its[0].Detail != "12 ms" || its[0].Param != "web" {
		t.Errorf("probe status = %+v", its)
	}
}

func TestDemoActionNamesInSync(t *testing.T) {
	var got []string
	for _, a := range DemoActions() {
		got = append(got, a.Name)
	}
	if !slices.Equal(got, config.DemoActions) {
		t.Fatalf("config.DemoActions %v != dash.DemoActions %v", config.DemoActions, got)
	}
}

func TestPlansWidget(t *testing.T) {
	cfg := &config.Config{
		Title:   "T",
		Plans:   &config.Plans{CLIProxy: &config.CLIProxy{URL: "https://cpa", Key: "k"}},
		Display: config.Display{Cols: 12, Rows: 8, Home: "p"},
		Pages: []config.Page{{Name: "p", Widgets: []config.Widget{
			{ID: "all", Type: "plans", At: []int{0, 0, 6, 4}},
			{ID: "work", Type: "plans", Names: []string{"WORK@"}, At: []int{6, 0, 6, 4}},
		}}},
	}
	d := New(cfg)
	var reset *config.Action
	for _, a := range d.Actions() {
		if a.Name == config.PlanResetAction {
			reset = &a
		}
	}
	if reset == nil || reset.Type != "plan" || !reset.Param.Allows("claude:claude-me@x.com.json") || reset.Param.Allows("claude:a b") {
		t.Fatalf("plan-reset action %+v", reset)
	}
	s := snap(src(config.PlanSource, `{"accounts":[
		{"key":"claude:me.json","label":"Claude","email":"me@x.com","windows":[{"id":"five_hour","used":50}]},
		{"key":"codex:w.json","label":"Codex","email":"work@x.com","windows":[]}]}`, t))
	all := data(t, d, s, "all")
	if all["reset"] != config.PlanResetAction || len(all["accounts"].([]any)) != 2 {
		t.Fatalf("%v", all)
	}
	if w := data(t, d, s, "work")["accounts"].([]any); len(w) != 1 {
		t.Fatalf("names filter: %v", w)
	}

	off := false
	cfg.Plans.Reset = &off
	if p := data(t, New(cfg), s, "all"); p["reset"] != nil {
		t.Fatalf("reset disabled but offered: %v", p)
	}
}

func TestLayoutCarriesLookAndBumpsVersion(t *testing.T) {
	mk := func(d config.Display) *Dash {
		return New(&config.Config{Display: d, Pages: []config.Page{{Name: "p"}}})
	}
	base := config.Display{TextSize: "medium", Theme: "petrol"}
	a := mk(base)
	l := a.Layout(false)
	if l.TextSize != "medium" || l.Theme != "petrol" || l.ThemeNight != "" {
		t.Errorf("layout look = %q %q %q", l.TextSize, l.Theme, l.ThemeNight)
	}
	for _, d := range []config.Display{
		{TextSize: "large", Theme: "petrol"},
		{TextSize: "medium", Theme: "paper"},
		{TextSize: "medium", Theme: "petrol", ThemeNight: "ember"},
	} {
		if b := mk(d); b.Version() == a.Version() {
			t.Errorf("%+v kept the version of %+v", d, base)
		}
	}
	b, _ := json.Marshal(mk(config.Display{TextSize: "xlarge", Theme: "ember", ThemeNight: "contrast"}).Layout(false))
	for _, want := range []string{`"text_size":"xlarge"`, `"theme":"ember"`, `"theme_night":"contrast"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("layout JSON %s lacks %s", b, want)
		}
	}
}
