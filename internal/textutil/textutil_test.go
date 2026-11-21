package textutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClip(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"  short  ", 10, "short"},
		{"abcdef", 6, "abcdef"},
		{"abcdefg", 6, "abcdef…"},
		{strings.Repeat("é", 200), 140, strings.Repeat("é", 140) + "…"},
		{"日本語のテキスト", 3, "日本語…"},
		{"ab cd", 3, "ab…"},
		{"anything", 0, ""},
		{"", 5, ""},
	}
	for _, c := range cases {
		got := Clip(c.in, c.n)
		if got != c.want {
			t.Errorf("Clip(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("Clip(%q, %d) produced invalid UTF-8", c.in, c.n)
		}
	}
}

func TestTitle(t *testing.T) {
	for in, want := range map[string]string{"": "", "plus": "Plus", "élite": "Élite", "日本": "日本"} {
		if got := Title(in); got != want {
			t.Errorf("Title(%q) = %q, want %q", in, got, want)
		}
	}
}
