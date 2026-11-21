package calendar

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// maxIterations caps the number of RRULE periods walked per event so a
// pathological rule cannot loop forever.
const maxIterations = 100_000

// maxInterval bounds INTERVAL so period arithmetic cannot overflow; a rule
// with a larger one is treated as unusable (the event shows once).
const maxInterval = 10_000

// weekdayNum is one BYDAY entry: an optional ordinal (0 = every) and a day.
type weekdayNum struct {
	n  int
	wd time.Weekday
}

// rrule is a parsed recurrence rule restricted to the supported subset.
type rrule struct {
	freq       string // DAILY, WEEKLY, MONTHLY or YEARLY
	interval   int
	count      int // 0 = unbounded
	until      time.Time
	hasUntil   bool
	byDay      []weekdayNum
	byMonthDay []int
	byMonth    []time.Month
	bySetPos   []int
	wkst       time.Weekday
}

var weekdays = map[string]time.Weekday{
	"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday,
	"TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday,
}

// parseRRule parses an RRULE value for an event whose DTSTART is in zone.
// It reports false when the rule is unusable or uses unsupported parts
// (FREQ other than DAILY..YEARLY, BYYEARDAY, BYWEEKNO); the caller then
// treats the event as a single occurrence.
func parseRRule(value string, z *zones, zone *time.Location, allDay bool) (*rrule, bool) {
	r := &rrule{interval: 1, wkst: time.Monday}
	for _, part := range strings.Split(value, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		k = strings.ToUpper(strings.TrimSpace(k))
		v = strings.ToUpper(strings.TrimSpace(v))
		switch k {
		case "FREQ":
			r.freq = v
		case "INTERVAL":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				if n > maxInterval {
					return nil, false
				}
				r.interval = n
			}
		case "COUNT":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				r.count = n
			}
		case "UNTIL":
			dt, err := z.parse(v, nil, zone)
			if err != nil {
				continue
			}
			r.until, r.hasUntil = dt.t, true
			if dt.date && !allDay {
				// A DATE UNTIL on a timed event includes that whole day.
				y, m, d := dt.t.Date()
				r.until = time.Date(y, m, d, 23, 59, 59, 0, zone)
			}
		case "BYDAY":
			for _, s := range strings.Split(v, ",") {
				if wn, ok := parseWeekdayNum(strings.TrimSpace(s)); ok {
					r.byDay = append(r.byDay, wn)
				}
			}
		case "BYMONTHDAY":
			r.byMonthDay = parseInts(v, -31, 31)
		case "BYMONTH":
			for _, n := range parseInts(v, 1, 12) {
				r.byMonth = append(r.byMonth, time.Month(n))
			}
		case "BYSETPOS":
			r.bySetPos = parseInts(v, -366, 366)
		case "WKST":
			if wd, ok := weekdays[v]; ok {
				r.wkst = wd
			}
		case "BYYEARDAY", "BYWEEKNO":
			return nil, false
		}
	}
	switch r.freq {
	case "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
		return r, true
	}
	return nil, false
}

func parseWeekdayNum(s string) (weekdayNum, bool) {
	if len(s) < 2 {
		return weekdayNum{}, false
	}
	wd, ok := weekdays[s[len(s)-2:]]
	if !ok {
		return weekdayNum{}, false
	}
	n := 0
	if num := s[:len(s)-2]; num != "" {
		var err error
		if n, err = strconv.Atoi(num); err != nil || n == 0 || n < -53 || n > 53 {
			return weekdayNum{}, false
		}
	}
	return weekdayNum{n: n, wd: wd}, true
}

// parseInts parses a comma list of non-zero integers within [lo, hi].
func parseInts(v string, lo, hi int) []int {
	var out []int
	for _, s := range strings.Split(v, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err == nil && n != 0 && n >= lo && n <= hi {
			out = append(out, n)
		}
	}
	return out
}

// expand returns the RRULE occurrence starts in [from, to); dtstart is
// always the first occurrence (counted by COUNT). Each occurrence is built
// with time.Date in dtstart's location, so wall-clock time is kept across
// DST. Without COUNT the walk starts near from, not at dtstart, so an event
// that began decades ago costs no more than a new one.
func (r *rrule) expand(dtstart, from, to time.Time) []time.Time {
	if !dtstart.Before(to) {
		return nil
	}
	var out []time.Time
	if !dtstart.Before(from) {
		out = append(out, dtstart)
	}
	if r.count == 1 {
		return out
	}
	zone := dtstart.Location()
	h, mi, s := dtstart.Clock()
	base := civilOf(dtstart)
	count := 1
	first := 0
	if r.count == 0 && dtstart.Before(from) {
		first = r.periodsBefore(base, civilOf(from.In(zone)))
	}
	for i := first; i < first+maxIterations; i++ {
		start, days := r.period(i, base)
		if !time.Date(start.y, start.m, start.d, 0, 0, 0, 0, zone).Before(to) {
			break
		}
		for _, c := range days {
			t := time.Date(c.y, c.m, c.d, h, mi, s, 0, zone)
			if !t.After(dtstart) {
				continue
			}
			if (r.hasUntil && t.After(r.until)) || !t.Before(to) {
				return out
			}
			if !t.Before(from) {
				out = append(out, t)
			}
			count++
			if r.count > 0 && count >= r.count {
				return out
			}
		}
	}
	return out
}

// periodsBefore returns a period index whose period starts on or before
// day (one period early, to be safe at period edges), counted from base.
func (r *rrule) periodsBefore(base, day civil) int {
	var n int
	switch r.freq {
	case "DAILY":
		n = daysBetween(base, day)
	case "WEEKLY":
		n = daysBetween(base, day) / 7
	case "MONTHLY":
		n = (day.y-base.y)*12 + int(day.m) - int(base.m)
	case "YEARLY":
		n = day.y - base.y
	}
	return max(n/r.interval-1, 0)
}

// period returns the first day of the i-th FREQ period after base's period
// and the sorted candidate days in it (after BYxxx and BYSETPOS).
func (r *rrule) period(i int, base civil) (civil, []civil) {
	n := i * r.interval
	var start civil
	var days []civil
	switch r.freq {
	case "DAILY":
		start = base.addDays(n)
		if r.matchesDay(start) {
			days = []civil{start}
		}
	case "WEEKLY":
		back := (7 + int(base.weekday()) - int(r.wkst)) % 7
		start = base.addDays(-back + 7*n)
		wds := []time.Weekday{base.weekday()}
		if len(r.byDay) > 0 {
			wds = wds[:0]
			for _, wn := range r.byDay {
				wds = append(wds, wn.wd)
			}
		}
		for _, wd := range wds {
			d := start.addDays((7 + int(wd) - int(r.wkst)) % 7)
			if r.inMonths(d.m) {
				days = append(days, d)
			}
		}
	case "MONTHLY":
		first := time.Date(base.y, base.m+time.Month(n), 1, 12, 0, 0, 0, time.UTC)
		start = civilOf(first)
		if r.inMonths(start.m) {
			for _, d := range r.monthDays(start.y, start.m, base.d) {
				days = append(days, civil{start.y, start.m, d})
			}
		}
	case "YEARLY":
		start = civil{base.y + n, time.January, 1}
		var months []time.Month
		switch {
		case len(r.byMonth) > 0:
			months = r.byMonth
		case len(r.byMonthDay) > 0:
			months = []time.Month{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
		case len(r.byDay) > 0:
			// BYDAY without BYMONTH/BYMONTHDAY: ordinals count within the year.
			length := 365
			if daysIn(start.y, time.February) == 29 {
				length = 366
			}
			for _, d := range weekdaysInRange(start.weekday(), length, r.byDay) {
				days = append(days, start.addDays(d-1))
			}
		default:
			months = []time.Month{base.m}
		}
		for _, m := range months {
			for _, d := range r.monthDays(start.y, m, base.d) {
				days = append(days, civil{start.y, m, d})
			}
		}
	}
	slices.SortFunc(days, func(a, b civil) int { return a.key() - b.key() })
	days = slices.Compact(days)
	return start, r.applySetPos(days)
}

// matchesDay applies BYMONTH, BYMONTHDAY and (plain) BYDAY as filters.
func (r *rrule) matchesDay(c civil) bool {
	if !r.inMonths(c.m) {
		return false
	}
	if len(r.byMonthDay) > 0 {
		n := daysIn(c.y, c.m)
		ok := false
		for _, md := range r.byMonthDay {
			if md == c.d || n+md+1 == c.d {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(r.byDay) > 0 {
		wd := c.weekday()
		for _, wn := range r.byDay {
			if wn.wd == wd {
				return true
			}
		}
		return false
	}
	return true
}

func (r *rrule) inMonths(m time.Month) bool {
	return len(r.byMonth) == 0 || slices.Contains(r.byMonth, m)
}

// monthDays returns the sorted days of month y/m selected by BYMONTHDAY and
// BYDAY (intersected when both are present), or defaultDay when neither is.
// Days that do not exist in the month (e.g. the 31st of April) are skipped.
func (r *rrule) monthDays(y int, m time.Month, defaultDay int) []int {
	n := daysIn(y, m)
	var out []int
	for _, md := range r.byMonthDay {
		d := md
		if md < 0 {
			d = n + md + 1
		}
		if d >= 1 && d <= n {
			out = append(out, d)
		}
	}
	switch {
	case len(r.byDay) > 0:
		wdays := weekdaysInRange(civil{y, m, 1}.weekday(), n, r.byDay)
		if len(r.byMonthDay) > 0 {
			out = slices.DeleteFunc(out, func(d int) bool { return !slices.Contains(wdays, d) })
		} else {
			out = wdays
		}
	case len(r.byMonthDay) == 0 && defaultDay <= n:
		out = []int{defaultDay}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// weekdaysInRange returns the 1-based day numbers within a span of length
// days starting on weekday firstWD that match specs (n=0: every such
// weekday; n>0: the n-th; n<0: the n-th from the end).
func weekdaysInRange(firstWD time.Weekday, length int, specs []weekdayNum) []int {
	var out []int
	for _, s := range specs {
		first := 1 + (7+int(s.wd)-int(firstWD))%7
		if s.n == 0 {
			for d := first; d <= length; d += 7 {
				out = append(out, d)
			}
			continue
		}
		total := (length-first)/7 + 1
		idx := s.n - 1
		if s.n < 0 {
			idx = total + s.n
		}
		if idx >= 0 && idx < total {
			out = append(out, first+7*idx)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// applySetPos keeps only the BYSETPOS positions of a period's sorted days.
func (r *rrule) applySetPos(days []civil) []civil {
	if len(r.bySetPos) == 0 || len(days) == 0 {
		return days
	}
	var out []civil
	for _, p := range r.bySetPos {
		idx := p - 1
		if p < 0 {
			idx = len(days) + p
		}
		if idx >= 0 && idx < len(days) {
			out = append(out, days[idx])
		}
	}
	slices.SortFunc(out, func(a, b civil) int { return a.key() - b.key() })
	return slices.Compact(out)
}

// civil is a calendar date without time or zone.
type civil struct {
	y int
	m time.Month
	d int
}

func civilOf(t time.Time) civil {
	y, m, d := t.Date()
	return civil{y, m, d}
}

func (c civil) noonUTC() time.Time { return time.Date(c.y, c.m, c.d, 12, 0, 0, 0, time.UTC) }

func (c civil) addDays(n int) civil { return civilOf(c.noonUTC().AddDate(0, 0, n)) }

func (c civil) weekday() time.Weekday { return c.noonUTC().Weekday() }

func (c civil) key() int { return c.y*10000 + int(c.m)*100 + c.d }

// daysBetween returns the number of calendar days from a to b.
func daysBetween(a, b civil) int {
	return int(b.noonUTC().Sub(a.noonUTC()).Round(time.Hour).Hours() / 24)
}

func daysIn(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 12, 0, 0, 0, time.UTC).Day()
}
