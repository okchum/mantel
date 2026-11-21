package dash

import (
	"context"
	"math"
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/jpath"
	"github.com/okchum/mantel/internal/state"
)

// HistKey names the history kept for one number of one source.
func HistKey(source, path string) string { return source + "|" + path }

// track is one number the historian samples.
type track struct {
	source, path string
	window       time.Duration
}

// tracksFor collects every number a trend or timeseries needs history for,
// keeping the longest window when several widgets chart the same number.
func tracksFor(pages []config.Page) []track {
	idx := map[string]int{}
	var out []track
	add := func(src, path string, window time.Duration) {
		k := HistKey(src, path)
		if i, ok := idx[k]; ok {
			out[i].window = max(out[i].window, window)
			return
		}
		idx[k] = len(out)
		out = append(out, track{src, path, window})
	}
	for _, p := range pages {
		for _, w := range p.Widgets {
			window := w.Window.Or(time.Hour)
			switch {
			case (w.Type == "stat" || w.Type == "gauge") && w.Trend:
				add(w.Source, or(w.Value, "value"), window)
			case w.Type == "timeseries":
				if len(w.Series) == 0 {
					add(w.Source, or(w.Value, "value"), window)
				}
				for _, s := range w.Series {
					if s.Points == "" {
						add(or(s.Source, w.Source), or(s.Value, "value"), window)
					}
				}
			}
		}
	}
	return out
}

// Historian samples tracked numbers into st.Hist whenever their source has
// new data, at most about 360 samples per window, and trims old ones. Start
// it after Restore: its first pass drops restored histories nothing charts
// any more, also when nothing is charted at all.
func (d *Dash) Historian(ctx context.Context, st *state.Store, tick time.Duration) {
	if len(d.tracks) == 0 {
		d.sample(st, time.Now()) // only prunes
		return
	}
	// Sources report in during the first seconds; look often until then so
	// charts fill without waiting a whole tick.
	for i := 0; ; i++ {
		d.sample(st, time.Now())
		wait := tick
		if i < 10 {
			wait = min(tick, time.Second)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (d *Dash) sample(st *state.Store, now time.Time) {
	snap := st.Read()
	snap.Now = now
	v := newViews(d, snap)
	type add struct {
		key string
		s   state.Sample
		tr  track
	}
	var adds []add
	for _, tr := range d.tracks {
		doc, m := v.doc(tr.source)
		if doc == nil || m.Error != "" && m.Updated.IsZero() {
			continue
		}
		at := m.Updated
		if at.IsZero() {
			at = now
		}
		key := HistKey(tr.source, tr.path)
		h := snap.Hist[key]
		minStep := tr.window / 360
		if len(h) > 0 {
			last := h[len(h)-1].At
			if !at.After(last) || at.Sub(last) < minStep {
				continue
			}
		}
		val, err := jpath.Num(doc, tr.path)
		if err != nil {
			continue
		}
		if len(h) == 0 && d.Backfill {
			for _, b := range backfill(at, tr.window, val) {
				adds = append(adds, add{key, b, tr})
			}
		}
		adds = append(adds, add{key, state.Sample{At: at, V: val}, tr})
	}
	orphans := false
	for k := range snap.Hist {
		if !d.histKeys[k] {
			orphans = true
			break
		}
	}
	if len(adds) == 0 && !orphans {
		return // an Update would only bump the store generation
	}
	st.Update(func(s *state.Snapshot) {
		if s.Hist == nil {
			s.Hist = map[string][]state.Sample{}
		}
		for _, a := range adds {
			h := append(s.Hist[a.key], a.s)
			s.Hist[a.key] = state.TrimSeries(h, now.Add(-a.tr.window))
		}
		// Drop histories nothing charts any more.
		for k := range s.Hist {
			if !d.histKeys[k] {
				delete(s.Hist, k)
			}
		}
	})
}

// backfill invents a plausible past for a demo number so charts are not
// empty on first start: a slow wander that ends at the current value.
func backfill(end time.Time, window time.Duration, v float64) []state.Sample {
	const n = 120
	step := window / n
	amp := math.Abs(v) * 0.06
	if amp == 0 {
		amp = 1
	}
	phase := float64(int64(v*1000)%97) / 10
	out := make([]state.Sample, 0, n)
	for i := n; i > 0; i-- {
		x := float64(i) / n
		d := amp * x * (math.Sin(x*7+phase) + 0.45*math.Sin(x*19+phase*2))
		out = append(out, state.Sample{At: end.Add(-time.Duration(i) * step), V: v + d})
	}
	return out
}

// history returns [unix ms, value] points of one tracked number within window.
func (d *Dash) history(snap state.Snapshot, source, path string, window time.Duration, w config.Widget) [][2]float64 {
	h := snap.Hist[HistKey(source, path)]
	since := snap.Now.Add(-window)
	out := make([][2]float64, 0, len(h))
	for _, s := range h {
		if s.At.Before(since) {
			continue
		}
		out = append(out, [2]float64{float64(s.At.UnixMilli()), scale(w, s.V)})
	}
	return out
}
