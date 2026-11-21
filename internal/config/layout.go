package config

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Template is a preset arrangement of named slots on the 12×8 grid. On a
// larger grid that is a multiple of 12×8 the slots are scaled up.
type Template struct {
	Name  string
	Title string // what it looks like, for `mantel layout -templates`
	Slots []Slot
}

// Slot is one named rectangle of a template: [col, row, w, h].
type Slot struct {
	Name string
	At   [4]int
}

const tplCols, tplRows = 12, 8

// Templates are the presets a page can name in "template".
var Templates = []Template{
	{"sidebar", "左边窄栏三格，右边一大块 + 底部两格（经典布局）", []Slot{
		{"side1", [4]int{0, 0, 4, 2}}, {"side2", [4]int{0, 2, 4, 2}}, {"side3", [4]int{0, 4, 4, 4}},
		{"main", [4]int{4, 0, 8, 5}}, {"foot1", [4]int{4, 5, 4, 3}}, {"foot2", [4]int{8, 5, 4, 3}},
	}},
	{"kpi", "顶部四个数字卡，下面一大一小", []Slot{
		{"k1", [4]int{0, 0, 3, 2}}, {"k2", [4]int{3, 0, 3, 2}}, {"k3", [4]int{6, 0, 3, 2}}, {"k4", [4]int{9, 0, 3, 2}},
		{"main", [4]int{0, 2, 8, 6}}, {"side", [4]int{8, 2, 4, 6}},
	}},
	{"kpi-grid", "顶部四个数字卡，下面田字四格", []Slot{
		{"k1", [4]int{0, 0, 3, 2}}, {"k2", [4]int{3, 0, 3, 2}}, {"k3", [4]int{6, 0, 3, 2}}, {"k4", [4]int{9, 0, 3, 2}},
		{"a", [4]int{0, 2, 6, 3}}, {"b", [4]int{6, 2, 6, 3}}, {"c", [4]int{0, 5, 6, 3}}, {"d", [4]int{6, 5, 6, 3}},
	}},
	{"focus", "上面一大块，下面三格", []Slot{
		{"top", [4]int{0, 0, 12, 5}},
		{"b1", [4]int{0, 5, 4, 3}}, {"b2", [4]int{4, 5, 4, 3}}, {"b3", [4]int{8, 5, 4, 3}},
	}},
	{"hero", "左边一大块，右边上下两格", []Slot{
		{"hero", [4]int{0, 0, 8, 8}}, {"top", [4]int{8, 0, 4, 4}}, {"bottom", [4]int{8, 4, 4, 4}},
	}},
	{"halves", "左右两半", []Slot{
		{"left", [4]int{0, 0, 6, 8}}, {"right", [4]int{6, 0, 6, 8}},
	}},
	{"thirds", "三竖栏", []Slot{
		{"left", [4]int{0, 0, 4, 8}}, {"center", [4]int{4, 0, 4, 8}}, {"right", [4]int{8, 0, 4, 8}},
	}},
	{"quad", "田字四格", []Slot{
		{"tl", [4]int{0, 0, 6, 4}}, {"tr", [4]int{6, 0, 6, 4}}, {"bl", [4]int{0, 4, 6, 4}}, {"br", [4]int{6, 4, 6, 4}},
	}},
	{"grid6", "三列两行六格", []Slot{
		{"a", [4]int{0, 0, 4, 4}}, {"b", [4]int{4, 0, 4, 4}}, {"c", [4]int{8, 0, 4, 4}},
		{"d", [4]int{0, 4, 4, 4}}, {"e", [4]int{4, 4, 4, 4}}, {"f", [4]int{8, 4, 4, 4}},
	}},
	{"full", "整屏一个", []Slot{
		{"full", [4]int{0, 0, 12, 8}},
	}},
}

// TemplateByName finds a preset.
func TemplateByName(name string) (Template, bool) {
	for _, t := range Templates {
		if t.Name == name {
			return t, true
		}
	}
	return Template{}, false
}

// DefaultSpan is the size an auto page gives a widget without "span", on the
// 12×8 grid: big enough for the widget to look like itself.
var DefaultSpan = map[string][2]int{
	"clock": {4, 2}, "weather": {4, 2}, "agenda": {4, 4},
	"servers": {8, 5}, "probes": {4, 3}, "quotas": {4, 3}, "plans": {6, 4},
	"stat": {3, 2}, "gauge": {3, 2}, "countdown": {3, 2},
	"timeseries": {6, 3}, "bars": {6, 3}, "status": {6, 3}, "progress": {6, 3}, "text": {6, 3},
	"list": {4, 4}, "image": {6, 4}, "buttons": {4, 2},
}

// MinSpan is how small an auto page may shrink a widget without "span" to
// fit it into a gap instead of starting another page.
var MinSpan = map[string][2]int{
	"clock": {3, 2}, "weather": {4, 2}, "agenda": {3, 3},
	"servers": {6, 3}, "probes": {3, 2}, "quotas": {3, 2}, "plans": {4, 3},
	"stat": {2, 2}, "gauge": {2, 2}, "countdown": {2, 2},
	"timeseries": {4, 2}, "bars": {4, 2}, "status": {3, 2}, "progress": {4, 2}, "text": {3, 2},
	"list": {3, 3}, "image": {4, 3}, "buttons": {2, 2},
}

// arrangePages turns template slots and auto placement into plain At
// rectangles, so everything after this only deals with At. Auto pages whose
// widgets do not fit continue on extra pages named "<name>-2", "<name>-3"….
func arrangePages(in []Page, cols, rows int, bad func(string, ...any)) []Page {
	fx, fy := cols/tplCols, rows/tplRows
	scalable := cols%tplCols == 0 && rows%tplRows == 0
	var out []Page
	for pi, p := range in {
		if p.Name == "" {
			p.Name = fmt.Sprintf("page%d", pi+1)
		}
		if p.Title == "" {
			p.Title = p.Name
		}
		where := "pages " + p.Name
		ws := slices.Clone(p.Widgets)

		if p.Template != "" {
			t, ok := TemplateByName(p.Template)
			switch {
			case !ok:
				names := make([]string, len(Templates))
				for i, t := range Templates {
					names[i] = t.Name
				}
				bad("%s: unknown template %q; have %s", where, p.Template, strings.Join(names, ", "))
			case !scalable:
				bad("%s: templates need a grid of 12×8 or a multiple of it, display is %d×%d", where, cols, rows)
			default:
				used := map[string]int{}
				for wi := range ws {
					w := &ws[wi]
					if w.Slot == "" {
						if len(w.At) == 0 && !p.Auto {
							bad("%s widgets[%d] (%s): needs a slot (%s) or at", where, wi, w.Type, slotNames(t))
						}
						continue
					}
					if len(w.At) != 0 {
						bad("%s widgets[%d] (%s): give slot or at, not both", where, wi, w.Type)
						continue
					}
					i := slices.IndexFunc(t.Slots, func(s Slot) bool { return s.Name == w.Slot })
					if i < 0 {
						bad("%s widgets[%d] (%s): template %s has no slot %q; have %s", where, wi, w.Type, t.Name, w.Slot, slotNames(t))
						continue
					}
					if prev, ok := used[w.Slot]; ok {
						bad("%s widgets[%d] (%s): slot %q already taken by widgets[%d]", where, wi, w.Type, w.Slot, prev)
						continue
					}
					used[w.Slot] = wi
					a := t.Slots[i].At
					w.At = []int{a[0] * fx, a[1] * fy, a[2] * fx, a[3] * fy}
				}
			}
		} else {
			for wi, w := range ws {
				if w.Slot != "" {
					bad("%s widgets[%d] (%s): slot %q but the page has no template", where, wi, w.Type, w.Slot)
				}
			}
		}

		if !p.Auto {
			for wi, w := range ws {
				if len(w.Span) != 0 && len(w.At) == 0 {
					bad("%s widgets[%d] (%s): span needs \"auto\": true on the page", where, wi, w.Type)
				}
			}
			p.Widgets = ws
			out = append(out, p)
			continue
		}
		out = append(out, autoPlace(p, ws, cols, rows, fx, fy, scalable, bad)...)
	}
	return out
}

// cells marks which grid cells of one page are taken.
type cells [][]bool

func newCells(cols, rows int) cells {
	g := make(cells, rows)
	for i := range g {
		g[i] = make([]bool, cols)
	}
	return g
}

func (g cells) free(x, y, w, h int) bool {
	if x < 0 || y < 0 || y+h > len(g) || x+w > len(g[0]) {
		return false
	}
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			if g[yy][xx] {
				return false
			}
		}
	}
	return true
}

func (g cells) take(x, y, w, h int) {
	for yy := max(y, 0); yy < y+h && yy < len(g); yy++ {
		for xx := max(x, 0); xx < x+w && xx < len(g[0]); xx++ {
			g[yy][xx] = true
		}
	}
}

// find returns the first spot, row by row from the top left, where w×h fits.
func (g cells) find(w, h int) ([]int, bool) {
	for y := range g {
		for x := range g[0] {
			if g.free(x, y, w, h) {
				return []int{x, y, w, h}, true
			}
		}
	}
	return nil, false
}

// autoPlace fills each page row by row, every widget at the first free spot
// from the top left where it fits (so small tiles fill holes left by big
// ones). A widget without "span" may shrink down to MinSpan to use a gap on an
// earlier page; what still does not fit starts a new page. Afterwards placed
// widgets grow into free space on their right and below (up to twice their
// size), so pages have few holes. Widgets that already have At (or a slot) stay put, on the first page.
func autoPlace(p Page, ws []Widget, cols, rows, fx, fy int, scalable bool, bad func(string, ...any)) []Page {
	type placed struct {
		page, idx int
	}
	pages := []Page{p}
	pages[0].Widgets = nil
	grids := []cells{newCells(cols, rows)}
	for _, w := range ws {
		if len(w.At) != 0 {
			if len(w.At) == 4 {
				grids[0].take(w.At[0], w.At[1], w.At[2], w.At[3])
			}
			pages[0].Widgets = append(pages[0].Widgets, w)
		}
	}
	var flowed []placed
	where := "pages " + p.Name
	for wi, w := range ws {
		if len(w.At) != 0 {
			continue
		}
		var sw, sh, minW, minH int
		switch {
		case len(w.Span) == 2:
			sw, sh = w.Span[0], w.Span[1]
			minW, minH = sw, sh // an explicit span is exact
		case len(w.Span) != 0:
			bad("%s widgets[%d] (%s): span must be [width, height]", where, wi, w.Type)
			continue
		default:
			d, ok := DefaultSpan[w.Type]
			if !ok {
				d = [2]int{4, 3}
			}
			m, ok := MinSpan[w.Type]
			if !ok {
				m = d
			}
			sw, sh, minW, minH = d[0], d[1], m[0], m[1]
			if scalable {
				sw, sh, minW, minH = sw*fx, sh*fy, minW*fx, minH*fy
			}
		}
		if minW < 1 || minH < 1 || minW > cols || minH > rows {
			what := "span"
			if minW != sw || minH != sh {
				what = "smallest size" // the default span would be cut down to this
			}
			bad("%s widgets[%d] (%s): %s [%d,%d] does not fit the %d×%d grid", where, wi, w.Type, what, minW, minH, cols, rows)
			continue
		}
		// A default span larger than the grid shrinks towards the minimum.
		sw, sh = min(sw, cols), min(sh, rows)
		// Sizes to try, biggest area first.
		var sizes [][2]int
		for ww := sw; ww >= minW; ww-- {
			for hh := sh; hh >= minH; hh-- {
				sizes = append(sizes, [2]int{ww, hh})
			}
		}
		sort.SliceStable(sizes, func(i, j int) bool { return sizes[i][0]*sizes[i][1] > sizes[j][0]*sizes[j][1] })
		k := 0
		for ; k < len(pages); k++ {
			for _, sz := range sizes {
				if at, ok := grids[k].find(sz[0], sz[1]); ok {
					w.At = at
					break
				}
			}
			if w.At != nil {
				break
			}
		}
		if w.At == nil {
			n := len(pages) + 1
			pages = append(pages, Page{Name: fmt.Sprintf("%s-%d", p.Name, n), Title: fmt.Sprintf("%s %d", p.Title, n), Auto: true})
			grids = append(grids, newCells(cols, rows))
			w.At = []int{0, 0, sw, sh}
		}
		grids[k].take(w.At[0], w.At[1], w.At[2], w.At[3])
		w.Span = nil
		pages[k].Widgets = append(pages[k].Widgets, w)
		flowed = append(flowed, placed{k, len(pages[k].Widgets) - 1})
	}
	// Grow flowed widgets into free cells next to them, one column or row at a
	// time, each towards its natural shape (its starting width:height), and to
	// at most twice its starting size each way: a lone number stretched over a
	// whole column looks worse than a little empty space.
	type grow struct {
		maxW, maxH int
		aspect     float64
	}
	limits := make([]grow, len(flowed))
	for i, f := range flowed {
		at := pages[f.page].Widgets[f.idx].At
		limits[i] = grow{at[2] * 2, at[3] * 2, float64(at[2]) / float64(at[3])}
	}
	for changed := true; changed; {
		changed = false
		for i, f := range flowed {
			at := pages[f.page].Widgets[f.idx].At
			g, lim := grids[f.page], limits[i]
			right := func() bool {
				if at[2] < lim.maxW && g.free(at[0]+at[2], at[1], 1, at[3]) {
					g.take(at[0]+at[2], at[1], 1, at[3])
					at[2]++
					return true
				}
				return false
			}
			down := func() bool {
				if at[3] < lim.maxH && g.free(at[0], at[1]+at[3], at[2], 1) {
					g.take(at[0], at[1]+at[3], at[2], 1)
					at[3]++
					return true
				}
				return false
			}
			if float64(at[2])/float64(at[3]) < lim.aspect {
				changed = right() || down() || changed
			} else {
				changed = down() || right() || changed
			}
		}
	}
	// Reading order inside each page (top to bottom, left to right), which is
	// also the order the tiles animate in.
	for k := range pages {
		sort.SliceStable(pages[k].Widgets, func(i, j int) bool {
			a, b := pages[k].Widgets[i].At, pages[k].Widgets[j].At
			if len(a) != 4 || len(b) != 4 {
				return false
			}
			if a[1] != b[1] {
				return a[1] < b[1]
			}
			return a[0] < b[0]
		})
	}
	return pages
}

func slotNames(t Template) string {
	n := make([]string, len(t.Slots))
	for i, s := range t.Slots {
		n[i] = s.Name
	}
	return strings.Join(n, ", ")
}

// Sketch draws a page's widgets as a character grid, one letter per widget,
// for `mantel layout`. Labels are returned in letter order.
func Sketch(ws []Widget, cols, rows int) (lines []string, labels []string) {
	cells := make([][]rune, rows)
	for i := range cells {
		cells[i] = []rune(strings.Repeat("·", cols))
	}
	letters := []rune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789")
	for i, w := range ws {
		r := '?'
		if i < len(letters) {
			r = letters[i]
		}
		if len(w.At) == 4 {
			// Clamped: Sketch also draws layouts that failed validation.
			for y := max(w.At[1], 0); y < w.At[1]+w.At[3] && y < rows; y++ {
				for x := max(w.At[0], 0); x < w.At[0]+w.At[2] && x < cols; x++ {
					cells[y][x] = r
				}
			}
		}
		name := w.Type
		if w.Title != "" {
			name += " " + w.Title
		}
		if w.Slot != "" {
			name += " [" + w.Slot + "]"
		}
		labels = append(labels, fmt.Sprintf("%c %s %v", r, name, w.At))
	}
	for _, c := range cells {
		lines = append(lines, string(c))
	}
	return lines, labels
}
