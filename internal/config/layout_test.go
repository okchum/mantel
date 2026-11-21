package config

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func arrange(t *testing.T, pages []Page, cols, rows int) ([]Page, []string) {
	t.Helper()
	var errs []string
	out := arrangePages(pages, cols, rows, func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) })
	return out, errs
}

// noOverlap checks every page's widgets sit inside the grid without sharing cells.
func noOverlap(t *testing.T, pages []Page, cols, rows int) {
	t.Helper()
	for _, p := range pages {
		g := newCells(cols, rows)
		for _, w := range p.Widgets {
			if len(w.At) != 4 || !g.free(w.At[0], w.At[1], w.At[2], w.At[3]) {
				t.Fatalf("page %s: widget %s at %v overlaps or is outside", p.Name, w.Type, w.At)
			}
			g.take(w.At[0], w.At[1], w.At[2], w.At[3])
		}
	}
}

func TestTemplatesAreValid(t *testing.T) {
	for _, tp := range Templates {
		ws := make([]Widget, len(tp.Slots))
		names := map[string]bool{}
		for i, s := range tp.Slots {
			if names[s.Name] {
				t.Fatalf("%s: duplicate slot %s", tp.Name, s.Name)
			}
			names[s.Name] = true
			ws[i] = Widget{Type: "text", At: s.At[:]}
		}
		noOverlap(t, []Page{{Name: tp.Name, Widgets: ws}}, tplCols, tplRows)
		// Every template covers the whole screen.
		area := 0
		for _, s := range tp.Slots {
			area += s.At[2] * s.At[3]
		}
		if area != tplCols*tplRows {
			t.Errorf("%s covers %d of %d cells", tp.Name, area, tplCols*tplRows)
		}
	}
}

func TestTemplateSlots(t *testing.T) {
	pages, errs := arrange(t, []Page{{Name: "p", Template: "kpi", Widgets: []Widget{
		{Type: "stat", Slot: "k2"},
		{Type: "servers", Slot: "main"},
		{Type: "clock", At: []int{8, 2, 4, 2}},
	}}}, 12, 8)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	got := pages[0].Widgets
	if !slices.Equal(got[0].At, []int{3, 0, 3, 2}) || !slices.Equal(got[1].At, []int{0, 2, 8, 6}) || !slices.Equal(got[2].At, []int{8, 2, 4, 2}) {
		t.Fatalf("got %v %v %v", got[0].At, got[1].At, got[2].At)
	}
}

func TestTemplateScales(t *testing.T) {
	pages, errs := arrange(t, []Page{{Name: "p", Template: "halves", Widgets: []Widget{{Type: "text", Slot: "right"}}}}, 24, 16)
	if len(errs) != 0 || !slices.Equal(pages[0].Widgets[0].At, []int{12, 0, 12, 16}) {
		t.Fatalf("%v %v", errs, pages[0].Widgets[0].At)
	}
}

func TestTemplateErrors(t *testing.T) {
	cases := []struct {
		name string
		page Page
		cols int
		want string
	}{
		{"unknown template", Page{Template: "nope"}, 12, "unknown template"},
		{"unknown slot", Page{Template: "halves", Widgets: []Widget{{Type: "text", Slot: "top"}}}, 12, "has no slot"},
		{"slot taken", Page{Template: "halves", Widgets: []Widget{{Type: "text", Slot: "left"}, {Type: "text", Slot: "left"}}}, 12, "already taken"},
		{"no slot", Page{Template: "halves", Widgets: []Widget{{Type: "text"}}}, 12, "needs a slot"},
		{"both", Page{Template: "halves", Widgets: []Widget{{Type: "text", Slot: "left", At: []int{0, 0, 1, 1}}}}, 12, "not both"},
		{"slot without template", Page{Widgets: []Widget{{Type: "text", Slot: "left"}}}, 12, "no template"},
		{"bad grid", Page{Template: "halves", Widgets: []Widget{{Type: "text", Slot: "left"}}}, 10, "multiple"},
		{"span without auto", Page{Widgets: []Widget{{Type: "text", Span: []int{2, 2}}}}, 12, "span needs"},
		{"span too big", Page{Auto: true, Widgets: []Widget{{Type: "text", Span: []int{13, 2}}}}, 12, "does not fit"},
		{"span shape", Page{Auto: true, Widgets: []Widget{{Type: "text", Span: []int{2}}}}, 12, "[width, height]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, errs := arrange(t, []Page{c.page}, c.cols, 8)
			if len(errs) == 0 || !strings.Contains(strings.Join(errs, "\n"), c.want) {
				t.Fatalf("want %q, got %v", c.want, errs)
			}
		})
	}
}

func TestAutoFlowFillsAndSpills(t *testing.T) {
	var ws []Widget
	for range 6 {
		ws = append(ws, Widget{Type: "servers"}) // 8×5 each, at least 6×3: one or two per page
	}
	pages, errs := arrange(t, []Page{{Name: "p", Title: "P", Auto: true, Widgets: ws}}, 12, 8)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if len(pages) < 2 || pages[1].Name != "p-2" || pages[1].Title != "P 2" {
		t.Fatalf("expected spill pages, got %d: %+v", len(pages), pages[1])
	}
	n := 0
	for _, p := range pages {
		n += len(p.Widgets)
	}
	if n != 6 {
		t.Fatalf("lost widgets: %d", n)
	}
	noOverlap(t, pages, 12, 8)
}

func TestAutoAroundFixedAndStretch(t *testing.T) {
	pages, errs := arrange(t, []Page{{Name: "p", Template: "focus", Auto: true, Widgets: []Widget{
		{Type: "timeseries", Slot: "top"},
		{Type: "stat", Title: "a"},
		{Type: "stat", Title: "b"},
		{Type: "buttons", Title: "c", Span: []int{2, 3}},
	}}}, 12, 8)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	p := pages[0]
	noOverlap(t, pages, 12, 8)
	if len(pages) != 1 || !slices.Equal(p.Widgets[0].At, []int{0, 0, 12, 5}) {
		t.Fatalf("fixed slot moved: %+v", p.Widgets)
	}
	// The stats grew into the free row under them, but no more than twice
	// their size.
	for _, w := range p.Widgets[1:3] {
		if w.At[3] != 3 || w.At[2] > 6 || w.At[3] > 4 {
			t.Fatalf("stat not stretched sensibly: %v", w.At)
		}
	}
	// Slot widgets are not stretched; reading order is kept.
	if p.Widgets[1].Title != "a" || p.Widgets[2].Title != "b" {
		t.Fatalf("order: %+v", p.Widgets)
	}
}

func TestAutoShrinksIntoGap(t *testing.T) {
	// A 12×5 fixed block leaves a 12×3 strip: an agenda (4×4, min 3×3) shrinks
	// into it instead of starting a second page.
	pages, errs := arrange(t, []Page{{Name: "p", Auto: true, Widgets: []Widget{
		{Type: "text", At: []int{0, 0, 12, 5}},
		{Type: "agenda"},
	}}}, 12, 8)
	if len(errs) != 0 || len(pages) != 1 {
		t.Fatalf("%v pages=%d", errs, len(pages))
	}
	if at := pages[0].Widgets[1].At; at[1] != 5 || at[3] != 3 {
		t.Fatalf("agenda at %v", at)
	}
}

func TestAutoNarrowGridUsesMinSpan(t *testing.T) {
	// servers default to 8×5 but can shrink to 6×3: a 6-column grid takes them.
	pages, errs := arrange(t, []Page{{Name: "p", Auto: true, Widgets: []Widget{{Type: "servers"}}}}, 6, 8)
	if len(errs) != 0 || len(pages) != 1 {
		t.Fatalf("%v pages=%d", errs, len(pages))
	}
	if at := pages[0].Widgets[0].At; at[2] != 6 || at[3] < 3 {
		t.Fatalf("servers at %v", at)
	}
	noOverlap(t, pages, 6, 8)
	// Below the minimum the error names the minimum, not the default span.
	_, errs = arrange(t, []Page{{Name: "p", Auto: true, Widgets: []Widget{{Type: "servers"}}}}, 4, 8)
	if msg := strings.Join(errs, "\n"); !strings.Contains(msg, "smallest size [6,3]") {
		t.Fatalf("errs = %v", errs)
	}
}

func TestLoadAutoPageValidates(t *testing.T) {
	err := load(t, `{"pages":[{"name":"a","auto":true,"widgets":[{"type":"clock"},{"type":"clock"}]},{"name":"b","template":"halves","widgets":[{"type":"clock","slot":"left"}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSketch(t *testing.T) {
	lines, labels := Sketch([]Widget{{Type: "clock", At: []int{0, 0, 2, 1}}, {Type: "text", Title: "x", Slot: "s", At: []int{2, 1, 1, 1}}}, 3, 2)
	if lines[0] != "AA·" || lines[1] != "··B" || !strings.Contains(labels[1], "text x [s]") {
		t.Fatalf("%q %q", lines, labels)
	}
}
