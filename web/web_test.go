package web

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/okchum/mantel/internal/config"
)

// The page and the config check must agree on widget types, themes and text
// sizes.
func TestPageKnowsConfigNames(t *testing.T) {
	read := func(name string) string {
		b, err := fs.ReadFile(FS, name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	var widgets strings.Builder
	mods, _ := fs.Glob(FS, "js/widgets/*.js")
	for _, m := range mods {
		widgets.WriteString(read(m))
	}
	defined := regexp.MustCompile(`(?m)^W\.([a-z_]+) = \{`).FindAllStringSubmatch(widgets.String(), -1)
	var types []string
	for _, d := range defined {
		types = append(types, d[1])
	}
	for _, w := range config.WidgetTypes {
		if !slices.Contains(types, w) {
			t.Errorf("widget type %q not drawn by web/js/widgets", w)
		}
	}

	app := read("app.js")
	for _, th := range config.Themes {
		if !strings.Contains(app, `"`+th+`"`) || !strings.Contains(read("app.css"), `[data-theme="`+th+`"]`) && th != "petrol" {
			t.Errorf("theme %q missing from app.js or app.css", th)
		}
	}
	for _, sz := range config.TextSizes {
		if !regexp.MustCompile(`\b` + sz + `: [0-9.]+`).MatchString(app) {
			t.Errorf("text size %q missing from app.js", sz)
		}
	}

}

var (
	// importRe matches one static import statement (possibly spanning lines):
	// its clause (empty for a bare `import "./x.js";`) and its specifier.
	importRe = regexp.MustCompile(`(?m)^import\s+(?:([^;"]*?)\s+from\s+)?"([^"]+)";`)
	// exportRe matches declarations exported by name.
	exportRe = regexp.MustCompile(`(?m)^export\s+(?:async\s+)?(?:function\s*\*?|class|const|let|var)\s+([A-Za-z_$][\w$]*)`)
	// exportListRe matches `export { a, b as c };` lists.
	exportListRe = regexp.MustCompile(`(?m)^export\s*\{([^}]*)\}`)
	ident        = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)
)

// moduleExports lists the names a module exports ("default" included).
func moduleExports(src string) map[string]bool {
	out := map[string]bool{}
	for _, m := range exportRe.FindAllStringSubmatch(src, -1) {
		out[m[1]] = true
	}
	for _, m := range exportListRe.FindAllStringSubmatch(src, -1) {
		for _, part := range strings.Split(m[1], ",") {
			f := strings.Fields(part)
			if len(f) > 0 {
				out[f[len(f)-1]] = true // `a` or `a as b`: exported as the last word
			}
		}
	}
	if regexp.MustCompile(`(?m)^export\s+default\b`).MatchString(src) {
		out["default"] = true
	}
	return out
}

// importedNames lists the names an import clause takes from its module:
// "default" for a default import, nothing for a namespace import.
func importedNames(clause string) ([]string, error) {
	clause = strings.TrimSpace(clause)
	var names []string
	if i := strings.Index(clause, "{"); i >= 0 {
		j := strings.LastIndex(clause, "}")
		if j < i {
			return nil, fmt.Errorf("unbalanced braces in %q", clause)
		}
		for _, part := range strings.Split(clause[i+1:j], ",") {
			f := strings.Fields(part)
			if len(f) == 0 {
				continue
			}
			if !ident.MatchString(f[0]) || len(f) != 1 && (len(f) != 3 || f[1] != "as") {
				return nil, fmt.Errorf("cannot read import %q in %q", part, clause)
			}
			names = append(names, f[0])
		}
		clause = strings.TrimSpace(clause[:i] + clause[j+1:])
	}
	clause = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(clause), ","))
	switch {
	case clause == "":
	case strings.HasPrefix(clause, "*"): // `* as ns`: no names to check
	case ident.MatchString(clause):
		names = append(names, "default")
	default:
		if d, rest, ok := strings.Cut(clause, ","); ok && ident.MatchString(strings.TrimSpace(d)) && strings.HasPrefix(strings.TrimSpace(rest), "*") {
			names = append(names, "default")
		} else {
			return nil, fmt.Errorf("cannot read import clause %q", clause)
		}
	}
	return names, nil
}

// Every module the page imports, from the script index.html loads down, must
// be embedded and must export each name imported from it: the browser
// rejects the whole module graph otherwise, and eslint (no-undef) cannot see
// across files. Every embedded module must also be reachable.
func TestModuleGraph(t *testing.T) {
	read := func(name string) (string, bool) {
		b, err := fs.ReadFile(FS, name)
		return string(b), err == nil
	}
	seen := map[string]bool{}
	var walk func(name string)
	walk = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		src, _ := read(name)
		stmts := importRe.FindAllStringSubmatch(src, -1)
		if n := len(regexp.MustCompile(`(?m)^import\b`).FindAllString(src, -1)); n != len(stmts) {
			t.Errorf("%s: %d import statements, only %d understood by this test", name, n, len(stmts))
		}
		for _, m := range stmts {
			spec := m[2]
			if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
				t.Errorf("%s: import %q is not a relative path (the page has no bundler)", name, spec)
				continue
			}
			target := path.Join(path.Dir(name), spec)
			tsrc, ok := read(target)
			if !ok || strings.HasPrefix(target, "../") {
				t.Errorf("%s: imports %q, but %s is not embedded", name, spec, target)
				continue
			}
			names, err := importedNames(m[1])
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			exp := moduleExports(tsrc)
			for _, n := range names {
				if !exp[n] {
					t.Errorf("%s: imports %q from %q, which %s does not export", name, n, spec, target)
				}
			}
			walk(target)
		}
	}
	html, _ := read("index.html")
	scripts := regexp.MustCompile(`<script\b[^>]*\bsrc="([^"]+)"`).FindAllStringSubmatch(html, -1)
	if len(scripts) == 0 {
		t.Fatal("index.html loads no script")
	}
	for _, sc := range scripts {
		if local(sc[1]) {
			walk(path.Clean(sc[1]))
		}
	}
	mods, _ := fs.Glob(FS, "*.js")
	for _, dir := range []string{"js", "js/widgets"} {
		more, _ := fs.Glob(FS, dir+"/*.js")
		mods = append(mods, more...)
	}
	for _, m := range mods {
		if !seen[m] {
			t.Errorf("%s is embedded but no page script imports it", m)
		}
	}
}

// local reports whether a page reference is a path on this server (not
// another origin, a fragment or inline data).
func local(ref string) bool {
	return ref != "" && !strings.HasPrefix(ref, "#") && !strings.HasPrefix(ref, "//") && !strings.HasPrefix(ref, "/") &&
		!regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`).MatchString(ref)
}

// Every file index.html references (stylesheet, preloaded font, script) is
// embedded: a missing one is a silent 404 on the device.
func TestPageReferencesExist(t *testing.T) {
	b, err := fs.ReadFile(FS, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	refs := regexp.MustCompile(`\b(?:src|href)="([^"]*)"`).FindAllStringSubmatch(string(b), -1)
	n := 0
	for _, r := range refs {
		if !local(r[1]) {
			continue
		}
		n++
		p, _, _ := strings.Cut(r[1], "?")
		p = path.Clean(p)
		if st, err := fs.Stat(FS, p); err != nil || st.IsDir() || strings.HasPrefix(p, "../") {
			t.Errorf("index.html references %q, which is not an embedded file", r[1])
		}
	}
	if n < 3 {
		t.Errorf("only %d local references found in index.html: the pattern no longer matches", n)
	}
}
