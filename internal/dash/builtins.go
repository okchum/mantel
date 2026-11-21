package dash

import (
	"fmt"
	"slices"
	"strings"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/jpath"
)

func (d *Dash) servers(w config.Widget, v *views) any {
	var out []any
	acts := map[string][]string{}
	for _, s := range d.cfg.Servers {
		acts[s.Name] = s.Actions
	}
	for _, s := range v.snap.Servers {
		if len(w.Names) > 0 && !slices.Contains(w.Names, s.Name) {
			continue
		}
		out = append(out, d.withActions(v.snap, "server:"+s.Name, s, acts[s.Name]))
	}
	return map[string]any{"servers": out}
}

func (d *Dash) probes(w config.Widget, v *views) any {
	var out []any
	acts := map[string][]string{}
	for _, p := range d.cfg.Probes {
		acts[p.Name] = p.Actions
	}
	for _, p := range v.snap.Probes {
		if len(w.Names) > 0 && !slices.Contains(w.Names, p.Name) {
			continue
		}
		out = append(out, d.withActions(v.snap, "probe:"+p.Name, p, acts[p.Name]))
	}
	return map[string]any{"probes": out}
}

func (d *Dash) quotas(w config.Widget, v *views) any {
	var out []any
	acts := map[string][]string{}
	for _, q := range d.cfg.Quotas {
		acts[q.Name] = q.Actions
	}
	if d.cfg.Demo {
		acts["DeepSeek"] = []string{"demo-topup"}
	}
	for _, q := range v.snap.Quotas {
		if len(w.Names) > 0 && !slices.Contains(w.Names, q.Name) {
			continue
		}
		out = append(out, d.withActions(v.snap, "quota:"+q.Name, q, acts[q.Name]))
	}
	return map[string]any{"quotas": out}
}

// probeStatus is a status widget over the built-in probes (names filter).
func (d *Dash) probeStatus(w config.Widget, v *views) any {
	var items []Item
	for _, p := range v.snap.Probes {
		if len(w.Names) > 0 && !slices.Contains(w.Names, p.Name) {
			continue
		}
		it := Item{Label: p.Name, State: "warn", Param: p.Name}
		if p.Checked {
			it.State = NormState(p.Up)
			if p.Up {
				ms := p.LatencyMS
				it.Num = &ms
				it.Detail = fmt.Sprintf("%.0f ms", ms)
			} else {
				it.Detail = p.Error
			}
		}
		items = append(items, it)
	}
	return map[string]any{"items": items, "total": len(items)}
}

// plans lists subscription accounts from the plans collector. names filters by
// a substring of the key, email or plan label.
func (d *Dash) plans(w config.Widget, v *views) any {
	doc, m := v.doc(config.PlanSource)
	p := payload(m, doc)
	if _, ok := d.actions[config.PlanResetAction]; ok {
		p["reset"] = config.PlanResetAction
	}
	accts, _ := jpath.Get(doc, "accounts")
	list, _ := accts.([]any)
	out := []any{}
	for _, a := range list {
		am, ok := a.(map[string]any)
		if !ok {
			continue
		}
		if len(w.Names) > 0 {
			hay := strings.ToLower(fmt.Sprint(am["key"], " ", am["email"], " ", am["label"], " ", am["plan"]))
			if !slices.ContainsFunc(w.Names, func(n string) bool { return strings.Contains(hay, strings.ToLower(n)) }) {
				continue
			}
		}
		out = append(out, am)
	}
	p["accounts"] = out
	return p
}
