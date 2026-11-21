// Package dash turns the configuration into pages of widgets and resolves each
// widget's data from the state snapshot. The page (web/app.js and its modules
// in web/js) and any other client (an iOS widget, a script) read the same
// GET /api/v1/dashboard.
package dash

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/state"
)

// Dash is the resolved layout plus what is needed to fill it.
type Dash struct {
	cfg     *config.Config
	pages   []config.Page
	display config.Display
	actions map[string]config.Action
	srcCfg  map[string]config.Source
	tracks  []track
	// histKeys holds HistKey of every track: the histories worth keeping.
	histKeys map[string]bool
	layout   Layout
	images   *imageCache
	loc      *time.Location // cfg.Timezone, loaded once
	norm     normCache

	// Backfill invents history for charts on first sight (demo mode only).
	Backfill bool
}

// Layout is the static part of the dashboard: grid, pages, widgets and
// action metadata. It changes only when the config does; Version tells.
type Layout struct {
	Version     string                `json:"version"`
	Title       string                `json:"title"`
	Timezone    string                `json:"timezone"`
	Grid        [2]int                `json:"grid"` // cols, rows
	Home        string                `json:"home"`
	IdleReturn  float64               `json:"idle_return"` // seconds, 0 = never
	Rotate      float64               `json:"rotate"`      // seconds, 0 = off
	RotateQuiet string                `json:"rotate_quiet,omitempty"`
	TextSize    string                `json:"text_size,omitempty"`
	Theme       string                `json:"theme,omitempty"`
	ThemeNight  string                `json:"theme_night,omitempty"`
	Pages       []PageView            `json:"pages"`
	Actions     map[string]ActionView `json:"actions"`
	CanAct      bool                  `json:"can_act"`
}

type PageView struct {
	Name    string       `json:"name"`
	Title   string       `json:"title"`
	Widgets []WidgetView `json:"widgets"`
}

// WidgetView is the part of a widget's config the page needs to draw it.
type WidgetView struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Title       string   `json:"title,omitempty"`
	At          []int    `json:"at"`
	Accent      string   `json:"accent,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	Decimals    *int     `json:"decimals,omitempty"`
	Format      string   `json:"format,omitempty"`
	Warn        *float64 `json:"warn,omitempty"`
	Crit        *float64 `json:"crit,omitempty"`
	Invert      bool     `json:"invert,omitempty"`
	Trend       bool     `json:"trend,omitempty"`
	Window      float64  `json:"window,omitempty"` // seconds
	Size        string   `json:"size,omitempty"`
	Layout      string   `json:"layout,omitempty"`
	Actions     []string `json:"actions,omitempty"`
	ItemActions []string `json:"item_actions,omitempty"`
	Refresh     float64  `json:"refresh,omitempty"` // image, seconds
	Source      string   `json:"source,omitempty"`
}

type ActionView struct {
	Title   string `json:"title"`
	Confirm string `json:"confirm"`
	Param   bool   `json:"param"`
}

// New builds the dashboard for cfg. Without configured pages it lays out the
// classic screen (or the demo pages in demo mode).
func New(cfg *config.Config) *Dash {
	d := &Dash{
		cfg:     cfg,
		display: cfg.Display,
		actions: map[string]config.Action{},
		srcCfg:  map[string]config.Source{},
		images:  newImageCache(),
		loc:     time.Local,
	}
	if cfg.Timezone != "" {
		if l, err := time.LoadLocation(cfg.Timezone); err == nil {
			d.loc = l
		}
	}
	for _, a := range cfg.Actions {
		d.actions[a.Name] = a
	}
	for _, s := range cfg.Sources {
		d.srcCfg[s.Name] = s
	}
	if cfg.Plans.ResetEnabled() {
		d.actions[config.PlanResetAction] = PlanResetAction()
	}
	if cfg.Demo {
		for _, a := range DemoActions() {
			if _, ok := d.actions[a.Name]; !ok {
				d.actions[a.Name] = a
			}
		}
	}
	d.pages = cfg.Pages
	if len(d.pages) == 0 {
		if cfg.Demo {
			d.pages = DemoPages()
		} else {
			d.pages = ClassicPages(cfg)
		}
		if d.display.Home == "" {
			d.display.Home = d.pages[0].Name
		}
	}
	if d.display.Cols == 0 {
		d.display.Cols, d.display.Rows = 12, 8
	}
	d.tracks = tracksFor(d.pages)
	d.histKeys = make(map[string]bool, len(d.tracks))
	for _, tr := range d.tracks {
		d.histKeys[HistKey(tr.source, tr.path)] = true
	}
	d.layout = d.buildLayout()
	return d
}

// Actions returns every action the dashboard offers (configured plus demo ones).
func (d *Dash) Actions() []config.Action {
	out := make([]config.Action, 0, len(d.actions))
	for _, a := range d.actions {
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b config.Action) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	})
	return out
}

func (d *Dash) buildLayout() Layout {
	l := Layout{
		Title:       d.cfg.Title,
		Timezone:    d.cfg.Timezone,
		Grid:        [2]int{d.display.Cols, d.display.Rows},
		Home:        d.display.Home,
		IdleReturn:  d.display.IdleReturn.Seconds(),
		Rotate:      d.display.Rotate.Seconds(),
		RotateQuiet: d.display.RotateQuiet,
		TextSize:    d.display.TextSize,
		Theme:       d.display.Theme,
		ThemeNight:  d.display.ThemeNight,
		Actions:     map[string]ActionView{},
	}
	for _, a := range d.actions {
		l.Actions[a.Name] = ActionView{Title: a.Title, Confirm: a.Confirm, Param: a.Param != nil}
	}
	for _, p := range d.pages {
		pv := PageView{Name: p.Name, Title: p.Title}
		for _, w := range p.Widgets {
			wv := WidgetView{
				ID: w.ID, Type: w.Type, Title: w.Title, At: w.At, Accent: w.Accent,
				Unit: w.Unit, Decimals: w.Decimals, Format: w.Format, Warn: w.Warn, Crit: w.Crit,
				Invert: w.Invert, Trend: w.Trend, Size: w.Size, Layout: w.Layout,
				Actions: w.Actions, ItemActions: w.ItemActions, Source: w.Source,
			}
			if w.Trend || w.Type == "timeseries" {
				wv.Window = w.Window.Or(time.Hour).Seconds()
			}
			if w.Type == "image" {
				wv.Refresh = w.Refresh.Or(time.Minute).Seconds()
			}
			pv.Widgets = append(pv.Widgets, wv)
		}
		l.Pages = append(l.Pages, pv)
	}
	b, _ := json.Marshal(l)
	sum := sha256.Sum256(b)
	l.Version = hex.EncodeToString(sum[:6])
	return l
}

// Layout returns the layout; canAct tells the page whether to offer actions.
func (d *Dash) Layout(canAct bool) Layout {
	l := d.layout
	l.CanAct = canAct
	return l
}

// Version identifies the current layout.
func (d *Dash) Version() string { return d.layout.Version }

// Response is GET /api/v1/dashboard.
type Response struct {
	Version string                        `json:"version"`
	Now     time.Time                     `json:"now"`
	Layout  *Layout                       `json:"layout,omitempty"` // omitted when the client already has Version
	Data    map[string]any                `json:"data"`             // widget id → payload
	Actions map[string]state.ActionResult `json:"actions"`          // "name" or "name:param" → latest result
}

// Build answers a dashboard request. have is the layout version the client
// holds; page limits data to one page ("" = all).
func (d *Dash) Build(snap state.Snapshot, have, page string, canAct bool) Response {
	r := Response{
		Version: d.layout.Version,
		Now:     snap.Now,
		Data:    map[string]any{},
		Actions: snap.Actions,
	}
	if have != d.layout.Version {
		l := d.Layout(canAct)
		r.Layout = &l
	}
	v := newViews(d, snap)
	for _, p := range d.pages {
		if page != "" && p.Name != page {
			continue
		}
		for _, w := range p.Widgets {
			r.Data[w.ID] = d.resolve(w, v)
		}
	}
	return r
}

// Widget finds a widget by id.
func (d *Dash) Widget(id string) (config.Widget, bool) {
	for _, p := range d.pages {
		for _, w := range p.Widgets {
			if w.ID == id {
				return w, true
			}
		}
	}
	return config.Widget{}, false
}
