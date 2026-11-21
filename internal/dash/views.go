package dash

import (
	"cmp"
	"strings"
	"time"

	"github.com/okchum/mantel/internal/state"
)

// views exposes every source of a snapshot as a decoded JSON document.
type views struct {
	d    *Dash
	snap state.Snapshot
	docs map[string]any
	meta map[string]srcMeta
}

type srcMeta struct {
	Updated time.Time
	Error   string
	Stale   bool
}

func newViews(d *Dash, snap state.Snapshot) *views {
	return &views{d: d, snap: snap, docs: map[string]any{}, meta: map[string]srcMeta{}}
}

func (v *views) doc(name string) (any, srcMeta) {
	if doc, ok := v.docs[name]; ok {
		return doc, v.meta[name]
	}
	var doc any
	var m srcMeta
	kind, item, _ := strings.Cut(name, ":")
	switch {
	case kind == "server" && item != "":
		for _, s := range v.snap.Servers {
			if s.Name == item {
				doc, m = v.d.normalize(v.snap, name, s), srcMeta{Updated: s.Updated, Error: s.Error}
			}
		}
	case kind == "probe" && item != "":
		for _, p := range v.snap.Probes {
			if p.Name == item {
				doc, m = v.d.normalize(v.snap, name, p), srcMeta{Updated: p.At, Error: p.Error}
			}
		}
	case kind == "quota" && item != "":
		for _, q := range v.snap.Quotas {
			if q.Name == item {
				doc, m = v.d.normalize(v.snap, name, q), srcMeta{Updated: q.Updated, Error: q.Error}
			}
		}
	case name == "weather":
		if w := v.snap.Weather; w != nil {
			doc, m = v.d.normalize(v.snap, name, w), srcMeta{Updated: w.Updated, Error: w.Error}
		}
	case name == "calendar":
		doc = v.d.normalize(v.snap, name, map[string]any{"events": v.snap.Events, "count": len(v.snap.Events)})
		m.Error = v.snap.EventsErr
	default:
		for _, s := range v.snap.Sources {
			if s.Name == name {
				doc, m = s.Data, srcMeta{Updated: s.Updated, Error: s.Error}
				if c, ok := v.d.srcCfg[name]; ok && c.StaleAfter.Duration > 0 && !s.Updated.IsZero() {
					m.Stale = v.snap.Now.Sub(s.Updated) > c.StaleAfter.Duration
				}
			}
		}
	}
	v.docs[name], v.meta[name] = doc, m
	return doc, m
}

// payload starts every widget's data with its source's freshness.
func payload(m srcMeta, doc any) map[string]any {
	p := map[string]any{}
	if m.Error != "" {
		p["error"] = m.Error
	}
	if !m.Updated.IsZero() {
		p["updated"] = m.Updated
	}
	if m.Stale {
		p["stale"] = true
	}
	if doc == nil && m.Error == "" {
		p["waiting"] = true
	}
	return p
}

func or(s, def string) string { return cmp.Or(s, def) }
