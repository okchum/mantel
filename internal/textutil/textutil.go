// Package textutil holds small string helpers shared across mantel.
package textutil

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Clip trims surrounding whitespace and shortens s to at most n runes,
// appending "…" when something was cut. It never splits a UTF-8 sequence.
func Clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i, count := 0, 0
	for i = range s {
		if count == n {
			break
		}
		count++
	}
	return strings.TrimRightFunc(s[:i], unicode.IsSpace) + "…"
}

// Title upper-cases the first rune of s.
func Title(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
