package dash

import (
	"cmp"
	"math"
	"slices"
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/jpath"
)

func (d *Dash) timeseries(w config.Widget, v *views) any {
	series := w.Series
	if len(series) == 0 {
		series = []config.Series{{Label: w.Title, Value: w.Value}}
	}
	window := w.Window.Or(time.Hour)
	var outs []map[string]any
	var first srcMeta
	for i, s := range series {
		src := or(s.Source, w.Source)
		doc, m := v.doc(src)
		if i == 0 {
			first = m
		}
		o := map[string]any{"label": or(s.Label, src), "color": s.Color}
		var pts [][2]float64
		if s.Points != "" {
			if doc != nil {
				raw, err := jpath.Get(doc, s.Points)
				if err != nil {
					o["error"] = err.Error()
				}
				pts = parsePoints(raw, v.snap.Now)
				for j := range pts {
					pts[j][1] = scale(w, pts[j][1])
				}
			}
		} else {
			pts = d.history(v.snap, src, or(s.Value, "value"), window, w)
		}
		o["points"] = downsample(pts, 240)
		if len(pts) > 0 {
			o["last"] = pts[len(pts)-1][1]
		}
		outs = append(outs, o)
	}
	p := payload(first, true)
	p["series"] = outs
	p["window"] = window.Seconds()
	return p
}

// parsePoints accepts [v...], [[t,v]...] or [{t|time|x, v|value|y}...];
// t may be unix seconds, unix ms or an RFC 3339 string. Bare values are spread
// one minute apart ending now.
func parsePoints(raw any, now time.Time) [][2]float64 {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out [][2]float64
	toMS := func(t any) (float64, bool) {
		if s, ok := t.(string); ok {
			if tt, err := time.Parse(time.RFC3339, s); err == nil {
				return float64(tt.UnixMilli()), true
			}
		}
		f, ok := jpath.ToNum(t)
		if !ok {
			return 0, false
		}
		if f < 1e11 { // seconds
			f *= 1000
		}
		return f, true
	}
	for i, it := range arr {
		switch x := it.(type) {
		case []any:
			if len(x) >= 2 {
				t, ok1 := toMS(x[0])
				v, ok2 := jpath.ToNum(x[1])
				if ok1 && ok2 {
					out = append(out, [2]float64{t, v})
				}
			}
		case map[string]any:
			var t, v any
			for _, k := range []string{"t", "time", "ts", "x", "at"} {
				if t == nil {
					t = x[k]
				}
			}
			for _, k := range []string{"v", "value", "y"} {
				if v == nil {
					v = x[k]
				}
			}
			tm, ok1 := toMS(t)
			vv, ok2 := jpath.ToNum(v)
			if ok1 && ok2 {
				out = append(out, [2]float64{tm, vv})
			}
		default:
			if v, ok := jpath.ToNum(x); ok {
				t := now.Add(-time.Duration(len(arr)-1-i) * time.Minute)
				out = append(out, [2]float64{float64(t.UnixMilli()), v})
			}
		}
	}
	slices.SortFunc(out, func(a, b [2]float64) int { return cmp.Compare(a[0], b[0]) })
	return out
}

// downsample keeps at most n points by averaging buckets.
func downsample(pts [][2]float64, n int) [][2]float64 {
	if len(pts) <= n {
		if pts == nil {
			return [][2]float64{}
		}
		return pts
	}
	out := make([][2]float64, 0, n)
	step := float64(len(pts)) / float64(n)
	for i := 0; i < n; i++ {
		a, b := int(float64(i)*step), int(float64(i+1)*step)
		if b > len(pts) {
			b = len(pts)
		}
		var t, v float64
		for _, p := range pts[a:b] {
			t += p[0]
			v += p[1]
		}
		k := float64(b - a)
		out = append(out, [2]float64{math.Round(t / k), v / k})
	}
	return out
}
