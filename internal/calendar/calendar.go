// Package calendar parses iCalendar (RFC 5545) feeds such as Google Calendar
// and iCloud secret/public ICS URLs and expands recurring events into
// concrete occurrences within a time window. Standard library only.
//
// Supported: line unfolding, parameters (incl. quoted values), TEXT
// unescaping; DTSTART/DTEND as UTC, floating, TZID (IANA names; unknown
// names fall back to the output location) or VALUE=DATE; DURATION;
// STATUS:CANCELLED; RRULE with FREQ=DAILY/WEEKLY/MONTHLY/YEARLY, INTERVAL,
// COUNT, UNTIL, BYDAY (with ordinals for MONTHLY/YEARLY), BYMONTHDAY,
// BYMONTH, BYSETPOS and WKST; EXDATE; RDATE; RECURRENCE-ID overrides.
//
// Not supported:
//   - FREQ=SECONDLY/MINUTELY/HOURLY, BYYEARDAY and BYWEEKNO: the event is
//     treated as a single occurrence at DTSTART.
//   - BYHOUR/BYMINUTE/BYSECOND are ignored; every occurrence uses DTSTART's
//     time of day.
//   - BYDAY ordinals in DAILY/WEEKLY rules are ignored (plain weekdays).
//   - VTIMEZONE definitions are ignored; TZIDs must be IANA zone names
//     (a path prefix such as "/mozilla.org/.../Europe/Berlin" is stripped).
//   - RECURRENCE-ID;RANGE=THISANDFUTURE only replaces the single instance.
//   - RDATE PERIOD values use the event's own duration, not the period end.
//   - Expansion walks at most 100,000 FREQ periods per event.
package calendar

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	_ "time/tzdata" // TZID lookups on hosts without a zoneinfo database
)

// Event is one concrete occurrence of a calendar event.
type Event struct {
	UID      string
	Title    string    // SUMMARY, unescaped
	Location string    // LOCATION, unescaped
	Calendar string    // X-WR-CALNAME of the feed (may be empty)
	Start    time.Time // converted to loc
	End      time.Time // converted to loc
	AllDay   bool
}

var errNotICalendar = errors.New("calendar: input is not an iCalendar stream")

// Parse reads an iCalendar stream and returns all event occurrences that overlap
// [from, to), with recurring events expanded, sorted by Start then Title.
// Times are returned in loc. All-day (VALUE=DATE) events are midnight-to-midnight in loc.
//
// A zero-length event overlaps when from <= Start < to. Malformed events are
// skipped; an error is returned only if reading fails or the stream contains
// no VCALENDAR at all. A nil loc means time.Local.
func Parse(r io.Reader, loc *time.Location, from, to time.Time) ([]Event, error) {
	if loc == nil {
		loc = time.Local
	}
	lines, err := unfoldLines(r)
	if err != nil {
		return nil, fmt.Errorf("calendar: reading feed: %w", err)
	}
	cals := parseCalendars(lines)
	if len(cals) == 0 {
		return nil, errNotICalendar
	}
	z := newZones(loc)

	var masters []*vevent
	byUID := make(map[string][]*vevent)
	overrides := make(map[string]*vevent) // key: UID + RECURRENCE-ID
	var overrideKeys []string
	for _, cal := range cals {
		calName := cal.text("X-WR-CALNAME")
		for _, c := range cal.children {
			if c.name != "VEVENT" {
				continue
			}
			ev, ok := buildEvent(c, z, calName)
			if !ok {
				continue
			}
			if ev.recurID == nil {
				masters = append(masters, ev)
				byUID[ev.uid] = append(byUID[ev.uid], ev)
				continue
			}
			key := ev.uid + "|" + instanceKey(*ev.recurID)
			prev, dup := overrides[key]
			if dup && prev.sequence > ev.sequence {
				continue
			}
			if !dup {
				overrideKeys = append(overrideKeys, key)
			}
			overrides[key] = ev
		}
	}

	var out []Event
	for _, key := range overrideKeys {
		ov := overrides[key]
		ms := byUID[ov.uid]
		for _, m := range ms {
			m.exclude(*ov.recurID)
		}
		if ov.cancelled {
			continue
		}
		if len(ms) > 0 {
			ov.inherit(ms[0])
		}
		out = ov.appendIfOverlaps(out, ov.start, loc, from, to)
	}
	for _, m := range masters {
		if m.cancelled {
			continue
		}
		for _, s := range m.starts(from, to) {
			if !m.excluded(s) {
				out = m.appendIfOverlaps(out, s, loc, from, to)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b Event) int {
		if c := a.Start.Compare(b.Start); c != 0 {
			return c
		}
		if c := strings.Compare(a.Title, b.Title); c != 0 {
			return c
		}
		if c := a.End.Compare(b.End); c != 0 {
			return c
		}
		return strings.Compare(a.UID, b.UID)
	})
	return out, nil
}

// vevent is one parsed VEVENT: a master (possibly recurring) or an override.
type vevent struct {
	uid, title, location, calendar string
	cancelled                      bool
	sequence                       int

	start    time.Time // in the event's own zone (loc for all-day/floating)
	allDay   bool
	hasEnd   bool // DTEND or DURATION present
	durDays  int  // nominal (wall-clock) days
	durExact time.Duration

	rule    *rrule
	rdates  []time.Time
	exInst  map[int64]bool // excluded starts (Unix seconds)
	exDates map[civil]bool // excluded dates (in the event's zone)
	recurID *dateTime      // set on overrides
}

// buildEvent converts a VEVENT component. It reports false when the event
// has no usable start.
func buildEvent(c *component, z *zones, calName string) (*vevent, bool) {
	ev := &vevent{
		uid:      strings.TrimSpace(c.text("UID")),
		title:    c.text("SUMMARY"),
		location: c.text("LOCATION"),
		calendar: calName,
	}
	ev.cancelled = strings.EqualFold(strings.TrimSpace(c.text("STATUS")), "CANCELLED")
	ev.sequence, _ = strconv.Atoi(strings.TrimSpace(c.text("SEQUENCE")))

	if p, ok := c.first("RECURRENCE-ID"); ok {
		if dt, err := z.parse(p.value, p.params, z.loc); err == nil {
			ev.recurID = &dt
		}
	}
	if p, ok := c.first("DTSTART"); ok {
		dt, err := z.parse(p.value, p.params, z.loc)
		if err != nil {
			return nil, false
		}
		ev.start, ev.allDay = dt.t, dt.date
	} else if ev.recurID != nil {
		ev.start, ev.allDay = ev.recurID.t, ev.recurID.date
	} else {
		return nil, false
	}
	zone := ev.start.Location()

	if p, ok := c.first("DTEND"); ok {
		if dt, err := z.parse(p.value, p.params, zone); err == nil {
			ev.hasEnd = true
			if ev.allDay {
				ev.durDays = daysBetween(civilOf(ev.start), civilOf(dt.t))
			} else {
				ev.durExact = max(dt.t.Sub(ev.start), 0)
			}
		}
	} else if p, ok := c.first("DURATION"); ok {
		if days, exact, err := parseDuration(p.value); err == nil {
			ev.hasEnd = true
			if days < 0 || exact < 0 {
				days, exact = 0, 0
			}
			ev.durDays, ev.durExact = days, exact
		}
	}
	if ev.allDay && ev.durDays <= 0 && ev.durExact <= 0 {
		ev.durDays = 1 // missing or empty all-day duration means one day
	}

	if ev.recurID != nil {
		return ev, true // overrides describe a single instance
	}
	if p, ok := c.first("RRULE"); ok {
		ev.rule, _ = parseRRule(p.value, z, zone, ev.allDay)
	}
	for _, p := range c.all("EXDATE") {
		for _, v := range strings.Split(p.value, ",") {
			if dt, err := z.parse(v, p.params, zone); err == nil {
				ev.exclude(dt)
			}
		}
	}
	for _, p := range c.all("RDATE") {
		for _, v := range strings.Split(p.value, ",") {
			v, _, _ = strings.Cut(v, "/") // PERIOD: keep the start only
			if dt, err := z.parse(v, p.params, zone); err == nil {
				ev.rdates = append(ev.rdates, ev.align(dt))
			}
		}
	}
	return ev, true
}

// align converts an RDATE value to the event's form: DATE values on a timed
// event take DTSTART's time of day, DATE-TIMEs on an all-day event its date.
func (ev *vevent) align(dt dateTime) time.Time {
	zone := ev.start.Location()
	switch {
	case dt.date && !ev.allDay:
		y, m, d := dt.t.Date()
		h, mi, s := ev.start.Clock()
		return time.Date(y, m, d, h, mi, s, 0, zone)
	case !dt.date && ev.allDay:
		y, m, d := dt.t.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, zone)
	}
	return dt.t
}

// exclude removes the instance identified by dt (EXDATE or RECURRENCE-ID).
// DATE values match any occurrence on that date in the event's zone.
func (ev *vevent) exclude(dt dateTime) {
	if ev.exInst == nil {
		ev.exInst = make(map[int64]bool)
		ev.exDates = make(map[civil]bool)
	}
	if dt.date || ev.allDay {
		ev.exDates[civilOf(dt.t)] = true
	}
	if !dt.date {
		ev.exInst[dt.t.Unix()] = true
	}
}

func (ev *vevent) excluded(start time.Time) bool {
	return ev.exInst[start.Unix()] || ev.exDates[civilOf(start)]
}

// starts returns the recurrence set (RRULE + RDATE) starting before to.
// RRULE occurrences that end before from are left out (the caller still
// checks each for overlap).
func (ev *vevent) starts(from, to time.Time) []time.Time {
	var out []time.Time
	if ev.rule != nil {
		// An occurrence starting this early can still overlap from; a day
		// more covers DST shifts of the wall-clock duration.
		earliest := from.AddDate(0, 0, -ev.durDays-1).Add(-ev.durExact)
		out = ev.rule.expand(ev.start, earliest, to)
	} else if ev.start.Before(to) {
		out = []time.Time{ev.start}
	}
	if len(ev.rdates) == 0 {
		return out
	}
	seen := make(map[int64]bool, len(out))
	for _, t := range out {
		seen[t.Unix()] = true
	}
	for _, t := range ev.rdates {
		if t.Before(to) && !seen[t.Unix()] {
			seen[t.Unix()] = true
			out = append(out, t)
		}
	}
	slices.SortFunc(out, time.Time.Compare)
	return out
}

// inherit fills fields an override omitted from its master.
func (ev *vevent) inherit(m *vevent) {
	if ev.title == "" {
		ev.title = m.title
	}
	if ev.location == "" {
		ev.location = m.location
	}
	if ev.calendar == "" {
		ev.calendar = m.calendar
	}
	if !ev.hasEnd && ev.allDay == m.allDay {
		ev.durDays, ev.durExact = m.durDays, m.durExact
	}
}

// appendIfOverlaps appends the occurrence starting at start when it overlaps
// [from, to).
func (ev *vevent) appendIfOverlaps(out []Event, start time.Time, loc *time.Location, from, to time.Time) []Event {
	end := start.AddDate(0, 0, ev.durDays).Add(ev.durExact)
	if !start.Before(to) {
		return out
	}
	if !end.After(from) && (!end.Equal(start) || start.Before(from)) {
		return out
	}
	return append(out, Event{
		UID:      ev.uid,
		Title:    ev.title,
		Location: ev.location,
		Calendar: ev.calendar,
		Start:    start.In(loc),
		End:      end.In(loc),
		AllDay:   ev.allDay,
	})
}

// instanceKey identifies a RECURRENCE-ID independent of its zone.
func instanceKey(dt dateTime) string {
	if dt.date {
		return "D" + dt.t.Format(layoutDate)
	}
	return strconv.FormatInt(dt.t.Unix(), 10)
}
