package calendar

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// property is one unfolded content line: NAME;PARAM=VALUE;...:value.
// Names and parameter names are upper-cased; parameter values are unquoted
// and multi-valued parameters are joined with ",".
type property struct {
	name   string
	params map[string]string
	value  string
}

// component is a BEGIN/END block with its own properties and nested blocks.
type component struct {
	name     string
	props    []property
	children []*component
}

// first returns the first property called name, ignoring nested components.
func (c *component) first(name string) (property, bool) {
	for _, p := range c.props {
		if p.name == name {
			return p, true
		}
	}
	return property{}, false
}

// all returns every property called name, ignoring nested components.
func (c *component) all(name string) []property {
	var out []property
	for _, p := range c.props {
		if p.name == name {
			out = append(out, p)
		}
	}
	return out
}

// text returns the unescaped TEXT value of the first property called name.
func (c *component) text(name string) string {
	if p, ok := c.first(name); ok {
		return unescapeText(p.value)
	}
	return ""
}

// unfoldLines reads r and returns its logical content lines (RFC 5545 §3.1):
// CRLF or LF endings, continuation lines start with a space or tab.
func unfoldLines(r io.Reader) ([]string, error) {
	br := bufio.NewReader(r)
	var (
		lines []string
		cur   strings.Builder
		have  bool
		first = true
	)
	flush := func() {
		if have {
			lines = append(lines, cur.String())
			cur.Reset()
			have = false
		}
	}
	for {
		raw, err := br.ReadString('\n')
		if raw != "" {
			line := strings.TrimRight(raw, "\r\n")
			if first {
				line = strings.TrimPrefix(line, "\ufeff")
				first = false
			}
			switch {
			case line == "":
			case (line[0] == ' ' || line[0] == '\t') && have:
				cur.WriteString(line[1:])
			default:
				flush()
				cur.WriteString(line)
				have = true
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	flush()
	return lines, nil
}

// parseContentLine splits a content line into name, parameters and value.
// It reports false for lines that are not well-formed content lines.
func parseContentLine(line string) (property, bool) {
	i := strings.IndexAny(line, ";:")
	if i <= 0 || !validName(line[:i]) {
		return property{}, false
	}
	p := property{name: strings.ToUpper(line[:i])}
	rest := line[i:]
	for rest != "" && rest[0] == ';' {
		rest = rest[1:]
		eq := strings.IndexAny(rest, "=;:")
		if eq < 0 {
			return property{}, false
		}
		if rest[eq] != '=' { // parameter without a value: skip it
			rest = rest[eq:]
			continue
		}
		pname := strings.ToUpper(strings.TrimSpace(rest[:eq]))
		rest = rest[eq+1:]
		var vals []string
		for {
			if strings.HasPrefix(rest, `"`) {
				end := strings.IndexByte(rest[1:], '"')
				if end < 0 {
					return property{}, false
				}
				vals = append(vals, rest[1:1+end])
				rest = rest[end+2:]
			} else {
				end := strings.IndexAny(rest, ",;:")
				if end < 0 {
					return property{}, false
				}
				vals = append(vals, rest[:end])
				rest = rest[end:]
			}
			if strings.HasPrefix(rest, ",") {
				rest = rest[1:]
				continue
			}
			break
		}
		if p.params == nil {
			p.params = make(map[string]string)
		}
		p.params[pname] = strings.Join(vals, ",")
	}
	if rest == "" || rest[0] != ':' {
		return property{}, false
	}
	p.value = rest[1:]
	return p, true
}

func validName(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return s != ""
}

// parseCalendars builds the component tree and returns the VCALENDAR roots.
// It is lenient: malformed lines are skipped, unmatched END lines ignored and
// unclosed components closed at EOF. A BEGIN:VCALENDAR always starts a new
// root, so a truncated feed followed by another one still parses.
func parseCalendars(lines []string) []*component {
	var roots, stack []*component
	for _, line := range lines {
		p, ok := parseContentLine(line)
		if !ok {
			continue
		}
		switch p.name {
		case "BEGIN":
			c := &component{name: strings.ToUpper(strings.TrimSpace(p.value))}
			switch {
			case c.name == "VCALENDAR":
				roots = append(roots, c)
				stack = stack[:0]
			case len(stack) > 0:
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, c)
			default:
				// Component outside any VCALENDAR: track it so its END
				// matches, but it is never returned.
			}
			stack = append(stack, c)
		case "END":
			name := strings.ToUpper(strings.TrimSpace(p.value))
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].name == name {
					stack = stack[:i]
					break
				}
			}
		default:
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				top.props = append(top.props, p)
			}
		}
	}
	return roots
}

// unescapeText decodes RFC 5545 TEXT escapes: \n \N \, \; \\.
func unescapeText(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n', 'N':
				b.WriteByte('\n')
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

const (
	layoutDate     = "20060102"
	layoutDateTime = "20060102T150405"
)

// zones resolves TZID parameters with caching. Unknown zones (for example
// Windows names such as "New Zealand Standard Time") fall back to loc.
type zones struct {
	loc   *time.Location
	cache map[string]*time.Location
}

func newZones(loc *time.Location) *zones {
	return &zones{loc: loc, cache: make(map[string]*time.Location)}
}

func (z *zones) lookup(tzid string) *time.Location {
	tzid = strings.TrimSpace(tzid)
	if l, ok := z.cache[tzid]; ok {
		return l
	}
	l := z.loc
	// Try the name as-is, then drop leading path segments so prefixed
	// names like "/mozilla.org/20050126_1/America/New_York" resolve.
	for cand := strings.TrimPrefix(tzid, "/"); cand != ""; {
		if cand != "Local" {
			if found, err := time.LoadLocation(cand); err == nil {
				l = found
				break
			}
		}
		i := strings.IndexByte(cand, '/')
		if i < 0 {
			break
		}
		cand = cand[i+1:]
	}
	z.cache[tzid] = l
	return l
}

// dateTime is a parsed DATE or DATE-TIME value.
type dateTime struct {
	t    time.Time // instant; DATE values are midnight in the output location
	date bool      // true for DATE values
}

// parse decodes a DATE or DATE-TIME value. UTC ("Z") values are returned in
// UTC, TZID values in that zone and floating values in floating.
func (z *zones) parse(value string, params map[string]string, floating *time.Location) (dateTime, error) {
	value = strings.TrimSpace(value)
	if strings.EqualFold(params["VALUE"], "DATE") || (len(value) == 8 && !strings.Contains(value, "T")) {
		if len(value) < 8 {
			return dateTime{}, fmt.Errorf("bad date %q", value)
		}
		d, err := time.Parse(layoutDate, value[:8])
		if err != nil {
			return dateTime{}, err
		}
		y, m, day := d.Date()
		return dateTime{t: time.Date(y, m, day, 0, 0, 0, 0, z.loc), date: true}, nil
	}
	if strings.HasSuffix(value, "Z") || strings.HasSuffix(value, "z") {
		t, err := time.Parse(layoutDateTime, value[:len(value)-1])
		if err != nil {
			return dateTime{}, err
		}
		return dateTime{t: t.UTC()}, nil
	}
	l := floating
	if tzid := params["TZID"]; tzid != "" {
		l = z.lookup(tzid)
	}
	t, err := time.ParseInLocation(layoutDateTime, value, l)
	if err != nil {
		return dateTime{}, err
	}
	return dateTime{t: t}, nil
}

// parseDuration decodes an RFC 5545 DURATION (e.g. "PT1H30M", "P1D", "P1W",
// "-PT15M") into nominal days (wall-clock days) and an exact remainder.
func parseDuration(s string) (days int, exact time.Duration, err error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	neg := false
	switch {
	case strings.HasPrefix(s, "-"):
		neg = true
		s = s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	if !strings.HasPrefix(s, "P") || len(s) < 3 {
		return 0, 0, fmt.Errorf("bad duration %q", s)
	}
	s = s[1:]
	inTime := false
	for s != "" {
		if s[0] == 'T' {
			inTime = true
			s = s[1:]
			continue
		}
		j := 0
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == 0 || j == len(s) {
			return 0, 0, fmt.Errorf("bad duration %q", s)
		}
		n, err := strconv.Atoi(s[:j])
		if err != nil {
			return 0, 0, err
		}
		switch unit := s[j]; {
		case unit == 'W' && !inTime:
			days += 7 * n
		case unit == 'D' && !inTime:
			days += n
		case unit == 'H' && inTime:
			exact += time.Duration(n) * time.Hour
		case unit == 'M' && inTime:
			exact += time.Duration(n) * time.Minute
		case unit == 'S' && inTime:
			exact += time.Duration(n) * time.Second
		default:
			return 0, 0, fmt.Errorf("bad duration unit %q", unit)
		}
		s = s[j+1:]
	}
	if neg {
		days, exact = -days, -exact
	}
	return days, exact, nil
}
