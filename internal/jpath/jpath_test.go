package jpath

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

const doc = `{
	"name": "nas",
	"n": 3,
	"ok": true,
	"none": null,
	"num_s": " 12.5 ",
	"data": {"items": [{"name": "a", "v": 1}, {"name": "b", "v": 2}, {"name": "c", "v": 3}], "empty": []},
	"obj": {"x": 1, "y": 2},
	"uni": "héllo",
	"mat": [[1, 2], [3, 4]]
}`

func TestGet(t *testing.T) {
	v := decode(t, doc)
	cases := []struct {
		path    string
		want    any
		wantErr string
	}{
		{"name", "nas", ""},
		{"n", 3.0, ""},
		{"ok", true, ""},
		{"none", nil, ""},
		{"data.items.0.name", "a", ""},
		{"data.items.2.v", 3.0, ""},
		{"data.items.-1.name", "c", ""},
		{"data.items.-3.name", "a", ""},
		{"data.items.#", 3.0, ""},
		{"data.empty.#", 0.0, ""},
		{"obj.#", 2.0, ""},
		{"uni.#", 5.0, ""}, // runes, not bytes
		{"mat.1.0", 3.0, ""},
		{"mat.-1.-1", 4.0, ""},
		{"data.items.3", nil, "out of range"},
		{"data.items.-4", nil, "out of range"},
		{"data.items.x", nil, "is not an index"},
		{"missing", nil, `no "missing"`},
		{"data.nope.x", nil, `no "nope"`},
		{"name.x", nil, "cannot descend"},
		{"n.#", nil, "# needs an array"},
	}
	for _, c := range cases {
		got, err := Get(v, c.path)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("Get(%q) err = %v, want %q", c.path, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("Get(%q): %v", c.path, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Get(%q) = %#v, want %#v", c.path, got, c.want)
		}
	}
}

func TestGetWholeDocument(t *testing.T) {
	v := decode(t, doc)
	for _, p := range []string{"", "."} {
		got, err := Get(v, p)
		if err != nil || !reflect.DeepEqual(got, v) {
			t.Errorf("Get(%q) = %v, %v; want whole doc", p, got, err)
		}
	}
	// A top-level array and scalar work too.
	arr := decode(t, `[10, 20]`)
	if got, err := Get(arr, "-1"); err != nil || got != 20.0 {
		t.Errorf("Get(arr, -1) = %v, %v", got, err)
	}
	if got, err := Get(arr, "#"); err != nil || got != 2.0 {
		t.Errorf("Get(arr, #) = %v, %v", got, err)
	}
}

func TestToNum(t *testing.T) {
	cases := []struct {
		in   any
		want float64
		ok   bool
	}{
		{1.5, 1.5, true},
		{7, 7, true},
		{int64(-3), -3, true},
		{json.Number("2.25"), 2.25, true},
		{json.Number("x"), 0, false},
		{"12.5", 12.5, true},
		{"  -4 ", -4, true},
		{"1e3", 1000, true},
		{"abc", 0, false},
		{"", 0, false},
		{"NaN", 0, false}, // not finite: would break JSON encoding downstream
		{"inf", 0, false},
		{"-Infinity", 0, false},
		{"1e400", 0, false},
		{true, 1, true},
		{false, 0, true},
		{nil, 0, false},
		{[]any{1.0}, 0, false},
		{map[string]any{}, 0, false},
	}
	for _, c := range cases {
		got, ok := ToNum(c.in)
		if ok != c.ok || ok && got != c.want {
			t.Errorf("ToNum(%#v) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestNum(t *testing.T) {
	v := decode(t, doc)
	cases := []struct {
		path    string
		want    float64
		wantErr string
	}{
		{"n", 3, ""},
		{"num_s", 12.5, ""},
		{"ok", 1, ""},
		{"data.items.#", 3, ""},
		{"data.items.-1.v", 3, ""},
		{"name", 0, "is not a number"},
		{"none", 0, "is not a number"},
		{"obj", 0, "is not a number"},
		{"missing", 0, `no "missing"`},
	}
	for _, c := range cases {
		got, err := Num(v, c.path)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("Num(%q) err = %v, want %q", c.path, err, c.wantErr)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("Num(%q) = %v, %v; want %v", c.path, got, err, c.want)
		}
	}
}

func TestToStrAndStr(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"hi", "hi"},
		{3.0, "3"},
		{0.1, "0.1"},
		{1e21, "1000000000000000000000"},
		{-2.5, "-2.5"},
		{true, "true"},
		{false, "false"},
		{[]any{1.0, "a"}, `[1,"a"]`},
		{map[string]any{"b": 1.0, "a": 2.0}, `{"a":2,"b":1}`},
	}
	for _, c := range cases {
		if got := ToStr(c.in); got != c.want {
			t.Errorf("ToStr(%#v) = %q, want %q", c.in, got, c.want)
		}
	}

	v := decode(t, doc)
	if s, err := Str(v, "data.items.1.name"); err != nil || s != "b" {
		t.Errorf("Str = %q, %v", s, err)
	}
	if s, err := Str(v, "n"); err != nil || s != "3" {
		t.Errorf("Str(n) = %q, %v", s, err)
	}
	if s, err := Str(v, "none"); err != nil || s != "" {
		t.Errorf("Str(none) = %q, %v", s, err)
	}
	if _, err := Str(v, "zzz"); err == nil {
		t.Error("Str(zzz): want error")
	}
}

func TestNormalize(t *testing.T) {
	type inner struct {
		V float64 `json:"v"`
	}
	type outer struct {
		Name  string  `json:"name"`
		Items []inner `json:"items"`
		Skip  string  `json:"-"`
		Ptr   *inner  `json:"ptr,omitempty"`
	}
	n := Normalize(outer{Name: "x", Items: []inner{{1}, {2}}, Skip: "s"})
	m, ok := n.(map[string]any)
	if !ok {
		t.Fatalf("Normalize = %T, want map", n)
	}
	if _, ok := m["Skip"]; ok {
		t.Error("json:\"-\" field leaked")
	}
	if _, ok := m["ptr"]; ok {
		t.Error("omitempty nil pointer present")
	}
	if got, err := Num(n, "items.-1.v"); err != nil || got != 2 {
		t.Errorf("walk normalized: %v, %v", got, err)
	}
	if got, err := Str(n, "name"); err != nil || got != "x" {
		t.Errorf("name: %v, %v", got, err)
	}
	// Numbers become float64; nil stays nil; unmarshalable values give nil.
	if got := Normalize(map[string]int{"a": 1}); !reflect.DeepEqual(got, map[string]any{"a": 1.0}) {
		t.Errorf("Normalize(map[string]int) = %#v", got)
	}
	if got := Normalize(nil); got != nil {
		t.Errorf("Normalize(nil) = %#v", got)
	}
	if got := Normalize(make(chan int)); got != nil {
		t.Errorf("Normalize(chan) = %#v, want nil", got)
	}
}
