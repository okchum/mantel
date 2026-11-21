package dash

import (
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/jpath"
)

func (d *Dash) resolve(w config.Widget, v *views) any {
	switch w.Type {
	case "clock":
		return map[string]any{}
	case "weather":
		if v.snap.Weather == nil {
			return map[string]any{"waiting": true}
		}
		return v.snap.Weather
	case "agenda":
		return map[string]any{"events": v.snap.Events, "error": v.snap.EventsErr}
	case "servers":
		return d.servers(w, v)
	case "probes":
		return d.probes(w, v)
	case "quotas":
		return d.quotas(w, v)
	case "plans":
		return d.plans(w, v)
	case "countdown":
		return d.countdown(w)
	case "image":
		return map[string]any{"src": "api/v1/image/" + w.ID}
	case "buttons":
		return map[string]any{}
	case "text":
		if w.Source == "" {
			return map[string]any{"text": w.Text}
		}
	case "timeseries":
		return d.timeseries(w, v)
	case "status":
		if w.Source == "" {
			return d.probeStatus(w, v)
		}
	}

	doc, m := v.doc(w.Source)
	p := payload(m, doc)
	if doc == nil {
		return p
	}
	switch w.Type {
	case "stat", "gauge":
		valPath := or(w.Value, "value")
		val, err := jpath.Num(doc, valPath)
		if err != nil {
			if _, ok := p["error"]; !ok {
				p["error"] = err.Error()
			}
			return p
		}
		val = scale(w, val)
		p["value"] = val
		if s := threshold(w, val); s != "" {
			p["state"] = s
		}
		if w.Type == "gauge" {
			max := 100.0
			if w.Format == "percent" && w.Scale == 0 {
				max = 1
			}
			if w.MaxValue != nil {
				max = *w.MaxValue
			} else if mv, err := jpath.Num(doc, or(w.Max, "max")); err == nil {
				max = scale(w, mv)
			}
			p["max"] = max
		}
		if w.Trend {
			pts := downsample(d.history(v.snap, w.Source, valPath, w.Window.Or(time.Hour), w), 60)
			spark := make([]float64, len(pts))
			for i, pt := range pts {
				spark[i] = pt[1]
			}
			p["spark"] = spark
			if len(pts) > 1 {
				p["delta"] = val - pts[0][1]
			}
		}
	case "text":
		s, err := jpath.Str(doc, or(w.Text, "text"))
		if err != nil {
			p["error"] = err.Error()
		}
		p["text"] = s
	case "bars", "list", "progress", "status":
		items, err := d.items(w, doc)
		if err != nil {
			p["error"] = err.Error()
		}
		p["total"] = len(items)
		if w.Limit > 0 && len(items) > w.Limit {
			items = items[:w.Limit]
		}
		p["items"] = items
	}
	return p
}

func scale(w config.Widget, v float64) float64 {
	if w.Scale != 0 {
		return v * w.Scale
	}
	return v
}

// threshold maps a value onto ok/warn/crit using the widget's lines.
func threshold(w config.Widget, v float64) string {
	past := func(line *float64) bool {
		if line == nil {
			return false
		}
		if w.Invert {
			return v <= *line
		}
		return v >= *line
	}
	switch {
	case past(w.Crit):
		return "crit"
	case past(w.Warn):
		return "warn"
	case w.Warn != nil || w.Crit != nil:
		return "ok"
	}
	return ""
}

func (d *Dash) countdown(w config.Widget) any {
	loc := d.loc
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, w.To, loc); err == nil {
			return map[string]any{"to": t, "all_day": layout == "2006-01-02"}
		}
	}
	return map[string]any{"error": "无法识别的日期 " + w.To}
}
