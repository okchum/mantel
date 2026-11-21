package dash

import (
	"fmt"
	"slices"
	"strings"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/jpath"
)

// Item is one row of a list/status/progress/bars widget.
type Item struct {
	Label  string   `json:"label"`
	Value  any      `json:"value,omitempty"`
	Num    *float64 `json:"num,omitempty"`
	Detail string   `json:"detail,omitempty"`
	Badge  string   `json:"badge,omitempty"`
	State  string   `json:"state,omitempty"`
	Param  string   `json:"param,omitempty"`
}

func (d *Dash) items(w config.Widget, doc any) ([]Item, error) {
	raw, err := jpath.Get(doc, or(w.Items, "items"))
	if err != nil {
		return nil, err
	}
	var out []Item
	add := func(label string, it any) {
		var x Item
		x.Label = label
		if m, ok := it.(map[string]any); ok {
			if s, err := jpath.Str(m, or(w.Label, "label")); err == nil && label == "" {
				x.Label = s
			}
			if val, err := jpath.Get(m, or(w.ItemV, "value")); err == nil {
				x.Value = val
			}
			x.Detail, _ = jpath.Str(m, or(w.Detail, "detail"))
			x.Badge, _ = jpath.Str(m, or(w.Badge, "badge"))
			if st, err := jpath.Get(m, or(w.State, "state")); err == nil {
				x.State = NormState(st)
			}
			if w.Param != "" {
				x.Param, _ = jpath.Str(m, w.Param)
			}
		} else {
			x.Value = it
			if w.Type == "status" {
				x.State = NormState(it)
			}
		}
		if f, ok := jpath.ToNum(x.Value); ok && x.Value != nil {
			if _, isBool := x.Value.(bool); !isBool {
				f = scale(w, f)
				x.Num = &f
				if x.State == "" {
					x.State = threshold(w, f)
				}
			}
		}
		if x.Param == "" && w.Param == "" && len(w.ItemActions) > 0 {
			x.Param = x.Label
		}
		out = append(out, x)
	}
	switch r := raw.(type) {
	case []any:
		for _, it := range r {
			add("", it)
		}
	case map[string]any:
		keys := make([]string, 0, len(r))
		for k := range r {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			add(k, r[k])
		}
	default:
		return nil, fmt.Errorf("%s is not a list", or(w.Items, "items"))
	}
	return out, nil
}

// NormState maps common words for health onto ok / warn / crit / off.
func NormState(v any) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "ok"
		}
		return "crit"
	case nil:
		return ""
	}
	s := strings.ToLower(strings.TrimSpace(jpath.ToStr(v)))
	switch s {
	case "ok", "up", "true", "running", "healthy", "success", "succeeded", "passed", "pass", "online",
		"active", "done", "completed", "complete", "green", "on", "1", "open":
		return "ok"
	case "warn", "warning", "degraded", "pending", "starting", "restarting", "queued", "in_progress",
		"running_late", "yellow", "partial", "stalled", "paused", "unknown":
		return "warn"
	case "crit", "critical", "down", "false", "failed", "failure", "error", "exited", "dead", "unhealthy",
		"offline", "red", "fail", "0", "cancelled", "timed_out":
		return "crit"
	case "off", "stopped", "idle", "disabled", "closed", "skipped", "inactive":
		return "off"
	}
	return s
}
