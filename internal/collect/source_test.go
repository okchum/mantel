package collect

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/okchum/mantel/internal/state"
)

func TestParseOutput(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{`{"a":1,"b":[true,"x"]}`, map[string]any{"a": 1.0, "b": []any{true, "x"}}},
		{"  [1, 2]\n", []any{1.0, 2.0}},
		{"42", map[string]any{"value": 42.0}},
		{" -3.5\n", map[string]any{"value": -3.5}},
		{"1e3", map[string]any{"value": 1000.0}},
		{`"quoted"`, "quoted"},
		{"true", true},
		{"null", nil},
		{"all good\n", map[string]any{"text": "all good"}},
		{"line1\nline2", map[string]any{"text": "line1\nline2"}},
		{"", map[string]any{"text": ""}},
		// Non-finite numbers stay text: a NaN value would break JSON encoding of the state.
		{"NaN", map[string]any{"text": "NaN"}},
		{"-inf", map[string]any{"text": "-inf"}},
		{`{"broken":`, map[string]any{"text": `{"broken":`}},
	}
	for _, c := range cases {
		got := ParseOutput([]byte(c.in))
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseOutput(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestPush(t *testing.T) {
	st := state.NewStore("t", "", nil, nil, nil, []string{"a", "b"})
	read := func(name string) state.Source {
		s := st.Read()
		return s.Sources[s.SourceIndex(name)]
	}

	if err := Push(st, "nope", map[string]any{"x": 1.0}, false); err == nil || !strings.Contains(err.Error(), `no source "nope"`) {
		t.Errorf("unknown source err = %v", err)
	}

	st.Update(func(s *state.Snapshot) { s.Sources[0].Error = "old failure" })
	before := time.Now()
	if err := Push(st, "a", map[string]any{"x": 1.0, "y": 2.0}, false); err != nil {
		t.Fatal(err)
	}
	src := read("a")
	if !reflect.DeepEqual(src.Data, map[string]any{"x": 1.0, "y": 2.0}) || src.Error != "" || src.Updated.Before(before) {
		t.Errorf("after push: %+v", src)
	}

	// Merge keeps old keys, overwrites given ones, and does not mutate the old map.
	old := src.Data.(map[string]any)
	if err := Push(st, "a", map[string]any{"y": 3.0, "z": 4.0}, true); err != nil {
		t.Fatal(err)
	}
	if got := read("a").Data; !reflect.DeepEqual(got, map[string]any{"x": 1.0, "y": 3.0, "z": 4.0}) {
		t.Errorf("merged = %#v", got)
	}
	if !reflect.DeepEqual(old, map[string]any{"x": 1.0, "y": 2.0}) {
		t.Errorf("merge mutated the previous document: %#v", old)
	}

	// Replace drops old keys.
	if err := Push(st, "a", map[string]any{"only": true}, false); err != nil {
		t.Fatal(err)
	}
	if got := read("a").Data; !reflect.DeepEqual(got, map[string]any{"only": true}) {
		t.Errorf("replaced = %#v", got)
	}

	// Merge onto a non-object, or with a non-object, replaces.
	if err := Push(st, "b", []any{1.0}, true); err != nil {
		t.Fatal(err)
	}
	if err := Push(st, "b", map[string]any{"k": "v"}, true); err != nil {
		t.Fatal(err)
	}
	if got := read("b").Data; !reflect.DeepEqual(got, map[string]any{"k": "v"}) {
		t.Errorf("merge onto array = %#v", got)
	}
	if err := Push(st, "b", "text", true); err != nil {
		t.Fatal(err)
	}
	if got := read("b").Data; got != "text" {
		t.Errorf("merge with scalar = %#v", got)
	}
	// Other slots untouched.
	if got := read("a").Data; !reflect.DeepEqual(got, map[string]any{"only": true}) {
		t.Errorf("slot a changed: %#v", got)
	}
}
