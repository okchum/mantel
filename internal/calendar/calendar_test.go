package calendar

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func mustLoad(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

var (
	akl = mustLoad("Pacific/Auckland")
	nyc = mustLoad("America/New_York")
)

// vcal wraps VEVENT (etc.) lines in a minimal VCALENDAR.
func vcal(body string) string {
	return "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//test//EN\n" + strings.TrimSpace(body) + "\nEND:VCALENDAR\n"
}

// day returns midnight of a YYYY-MM-DD date in loc.
func day(loc *time.Location, s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil {
		panic(err)
	}
	return t
}

const layoutOut = "2006-01-02T15:04-07"

func render(evs []Event) []string {
	out := []string{}
	for _, e := range evs {
		s := e.Title + " " + e.Start.Format(layoutOut) + "/" + e.End.Format(layoutOut)
		if e.AllDay {
			s += " allday"
		}
		out = append(out, s)
	}
	return out
}

func checkEvents(t *testing.T, got []Event, want []string) {
	t.Helper()
	g := render(got)
	if want == nil {
		want = []string{}
	}
	if !slices.Equal(g, want) {
		t.Errorf("events mismatch\n got: %s\nwant: %s", strings.Join(g, "\n      "), strings.Join(want, "\n      "))
	}
}

func TestParseOccurrences(t *testing.T) {
	tests := []struct {
		name     string
		ics      string
		loc      *time.Location // default akl
		from, to string         // YYYY-MM-DD in loc
		want     []string
	}{
		// ---- DTSTART / DTEND forms ----
		{
			name: "utc converted to loc",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:UTC
DTSTART:20261119T200000Z
DTEND:20261119T210000Z
END:VEVENT`),
			from: "2026-11-20", to: "2026-11-21",
			want: []string{"UTC 2026-11-20T09:00+13/2026-11-20T10:00+13"},
		},
		{
			name: "floating time is loc wall clock",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Floating
DTSTART:20261120T083000
DTEND:20261120T090000
END:VEVENT`),
			from: "2026-11-20", to: "2026-11-21",
			want: []string{"Floating 2026-11-20T08:30+13/2026-11-20T09:00+13"},
		},
		{
			name: "floating time with UTC loc",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Floating
DTSTART:20261120T083000
DTEND:20261120T090000
END:VEVENT`),
			loc:  time.UTC,
			from: "2026-11-20", to: "2026-11-21",
			want: []string{"Floating 2026-11-20T08:30+00/2026-11-20T09:00+00"},
		},
		{
			name: "TZID plain, quoted, prefixed and Windows fallback",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:A New York
DTSTART;TZID=America/New_York:20261119T150000
DTEND;TZID=America/New_York:20261119T160000
END:VEVENT
BEGIN:VEVENT
UID:2
SUMMARY:B London quoted
DTSTART;TZID="Europe/London":20261119T200000
DTEND;TZID="Europe/London":20261119T210000
END:VEVENT
BEGIN:VEVENT
UID:3
SUMMARY:C Mozilla prefix
DTSTART;TZID=/mozilla.org/20050126_1/America/New_York:20261119T150000
DTEND;TZID=/mozilla.org/20050126_1/America/New_York:20261119T153000
END:VEVENT
BEGIN:VEVENT
UID:4
SUMMARY:D Windows zone
DTSTART;TZID=New Zealand Standard Time:20261120T090000
DTEND;TZID=New Zealand Standard Time:20261120T093000
END:VEVENT`),
			from: "2026-11-20", to: "2026-11-21",
			want: []string{
				"A New York 2026-11-20T09:00+13/2026-11-20T10:00+13",
				"B London quoted 2026-11-20T09:00+13/2026-11-20T10:00+13",
				"C Mozilla prefix 2026-11-20T09:00+13/2026-11-20T09:30+13",
				"D Windows zone 2026-11-20T09:00+13/2026-11-20T09:30+13",
			},
		},
		{
			name: "all-day single, multi-day spanning into window, missing DTEND",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Single
DTSTART;VALUE=DATE:20261120
DTEND;VALUE=DATE:20261121
END:VEVENT
BEGIN:VEVENT
UID:2
SUMMARY:Trip
DTSTART;VALUE=DATE:20261118
DTEND;VALUE=DATE:20261122
END:VEVENT
BEGIN:VEVENT
UID:3
SUMMARY:No end
DTSTART:20261121
END:VEVENT
BEGIN:VEVENT
UID:4
SUMMARY:Ended yesterday
DTSTART;VALUE=DATE:20261118
DTEND;VALUE=DATE:20261120
END:VEVENT`),
			from: "2026-11-20", to: "2026-11-23",
			want: []string{
				"Trip 2026-11-18T00:00+13/2026-11-22T00:00+13 allday",
				"Single 2026-11-20T00:00+13/2026-11-21T00:00+13 allday",
				"No end 2026-11-21T00:00+13/2026-11-22T00:00+13 allday",
			},
		},
		{
			name: "timed without DTEND is zero length; window edges",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Point
DTSTART;TZID=Pacific/Auckland:20261120T090000
END:VEVENT
BEGIN:VEVENT
UID:2
SUMMARY:At from
DTSTART;TZID=Pacific/Auckland:20261120T000000
END:VEVENT
BEGIN:VEVENT
UID:3
SUMMARY:At to
DTSTART;TZID=Pacific/Auckland:20261121T000000
END:VEVENT
BEGIN:VEVENT
UID:4
SUMMARY:Ends at from
DTSTART;TZID=Pacific/Auckland:20261119T230000
DTEND;TZID=Pacific/Auckland:20261120T000000
END:VEVENT`),
			from: "2026-11-20", to: "2026-11-21",
			want: []string{
				"At from 2026-11-20T00:00+13/2026-11-20T00:00+13",
				"Point 2026-11-20T09:00+13/2026-11-20T09:00+13",
			},
		},
		{
			name: "DURATION forms",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Ninety minutes
DTSTART;TZID=Pacific/Auckland:20261120T090000
DURATION:PT1H30M
END:VEVENT
BEGIN:VEVENT
UID:2
SUMMARY:One day timed
DTSTART;TZID=Pacific/Auckland:20261120T120000
DURATION:P1D
END:VEVENT
BEGIN:VEVENT
UID:3
SUMMARY:One week
DTSTART;VALUE=DATE:20261120
DURATION:P1W
END:VEVENT`),
			from: "2026-11-20", to: "2026-11-21",
			want: []string{
				"One week 2026-11-20T00:00+13/2026-11-27T00:00+13 allday",
				"Ninety minutes 2026-11-20T09:00+13/2026-11-20T10:30+13",
				"One day timed 2026-11-20T12:00+13/2026-11-21T12:00+13",
			},
		},
		{
			name: "P1D duration keeps wall clock across DST",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Over DST
DTSTART;TZID=Pacific/Auckland:20260926T120000
DURATION:P1D
END:VEVENT`),
			from: "2026-09-26", to: "2026-09-27",
			want: []string{"Over DST 2026-09-26T12:00+12/2026-09-27T12:00+13"},
		},
		// ---- filtering of components ----
		{
			name: "cancelled, VTODO, VJOURNAL skipped; VALARM does not leak",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Cancelled
STATUS:CANCELLED
DTSTART:20261120T000000Z
END:VEVENT
BEGIN:VTODO
UID:2
SUMMARY:Todo
DTSTART:20261120T000000Z
DUE:20261120T010000Z
END:VTODO
BEGIN:VJOURNAL
UID:3
SUMMARY:Journal
DTSTART:20261120T000000Z
END:VJOURNAL
BEGIN:VEVENT
UID:4
DTSTART;TZID=Pacific/Auckland:20261120T090000
BEGIN:VALARM
ACTION:DISPLAY
SUMMARY:Alarm summary
DESCRIPTION:Reminder
STATUS:CANCELLED
DTSTART:20261225T000000Z
DURATION:PT5M
TRIGGER:-PT15M
END:VALARM
SUMMARY:Real
DTEND;TZID=Pacific/Auckland:20261120T100000
END:VEVENT`),
			from: "2026-11-20", to: "2026-11-21",
			want: []string{"Real 2026-11-20T09:00+13/2026-11-20T10:00+13"},
		},
		// ---- RRULE: WEEKLY ----
		{
			name: "weekly BYDAY=MO,WE,FR with COUNT",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Gym
DTSTART;TZID=Pacific/Auckland:20261116T073000
DTEND;TZID=Pacific/Auckland:20261116T080000
RRULE:FREQ=WEEKLY;BYDAY=MO,WE,FR;COUNT=5
END:VEVENT`),
			from: "2026-11-01", to: "2026-12-31",
			want: []string{
				"Gym 2026-11-16T07:30+13/2026-11-16T08:00+13",
				"Gym 2026-11-18T07:30+13/2026-11-18T08:00+13",
				"Gym 2026-11-20T07:30+13/2026-11-20T08:00+13",
				"Gym 2026-11-23T07:30+13/2026-11-23T08:00+13",
				"Gym 2026-11-25T07:30+13/2026-11-25T08:00+13",
			},
		},
		{
			name: "COUNT counts from DTSTART, not from window start",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Gym
DTSTART;TZID=Pacific/Auckland:20261116T073000
DTEND;TZID=Pacific/Auckland:20261116T080000
RRULE:FREQ=WEEKLY;BYDAY=MO,WE,FR;COUNT=5
END:VEVENT`),
			from: "2026-11-21", to: "2026-12-31",
			want: []string{
				"Gym 2026-11-23T07:30+13/2026-11-23T08:00+13",
				"Gym 2026-11-25T07:30+13/2026-11-25T08:00+13",
			},
		},
		{
			name: "weekly INTERVAL=2 with WKST=MO vs WKST=SU (RFC 5545 example)",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:MO
DTSTART:19970805T090000
RRULE:FREQ=WEEKLY;INTERVAL=2;COUNT=4;BYDAY=TU,SU;WKST=MO
END:VEVENT
BEGIN:VEVENT
UID:2
SUMMARY:SU
DTSTART:19970805T090000
RRULE:FREQ=WEEKLY;INTERVAL=2;COUNT=4;BYDAY=TU,SU;WKST=SU
END:VEVENT`),
			loc:  time.UTC,
			from: "1997-08-01", to: "1997-10-01",
			want: []string{
				"MO 1997-08-05T09:00+00/1997-08-05T09:00+00",
				"SU 1997-08-05T09:00+00/1997-08-05T09:00+00",
				"MO 1997-08-10T09:00+00/1997-08-10T09:00+00",
				"SU 1997-08-17T09:00+00/1997-08-17T09:00+00",
				"MO 1997-08-19T09:00+00/1997-08-19T09:00+00",
				"SU 1997-08-19T09:00+00/1997-08-19T09:00+00",
				"MO 1997-08-24T09:00+00/1997-08-24T09:00+00",
				"SU 1997-08-31T09:00+00/1997-08-31T09:00+00",
			},
		},
		// ---- RRULE: DAILY ----
		{
			name: "daily across both Auckland DST changes keeps 09:00",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Spring
DTSTART;TZID=Pacific/Auckland:20260925T090000
DTEND;TZID=Pacific/Auckland:20260925T093000
RRULE:FREQ=DAILY;COUNT=4
END:VEVENT
BEGIN:VEVENT
UID:2
SUMMARY:Autumn
DTSTART;TZID=Pacific/Auckland:20270402T090000
DTEND;TZID=Pacific/Auckland:20270402T093000
RRULE:FREQ=DAILY;COUNT=4
END:VEVENT`),
			from: "2026-09-01", to: "2027-05-01",
			want: []string{
				"Spring 2026-09-25T09:00+12/2026-09-25T09:30+12",
				"Spring 2026-09-26T09:00+12/2026-09-26T09:30+12",
				"Spring 2026-09-27T09:00+13/2026-09-27T09:30+13",
				"Spring 2026-09-28T09:00+13/2026-09-28T09:30+13",
				"Autumn 2027-04-02T09:00+13/2027-04-02T09:30+13",
				"Autumn 2027-04-03T09:00+13/2027-04-03T09:30+13",
				"Autumn 2027-04-04T09:00+12/2027-04-04T09:30+12",
				"Autumn 2027-04-05T09:00+12/2027-04-05T09:30+12",
			},
		},
		{
			name: "daily Auckland DST seen from UTC loc",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Spring
DTSTART;TZID=Pacific/Auckland:20260926T090000
RRULE:FREQ=DAILY;COUNT=3
END:VEVENT`),
			loc:  time.UTC,
			from: "2026-09-25", to: "2026-10-01",
			want: []string{
				"Spring 2026-09-25T21:00+00/2026-09-25T21:00+00",
				"Spring 2026-09-26T20:00+00/2026-09-26T20:00+00",
				"Spring 2026-09-27T20:00+00/2026-09-27T20:00+00",
			},
		},
		{
			name: "daily INTERVAL=2 and daily BYDAY filter",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Every other day
DTSTART:20261119T070000
RRULE:FREQ=DAILY;INTERVAL=2;COUNT=3
END:VEVENT
BEGIN:VEVENT
UID:2
SUMMARY:Mon Tue
DTSTART:20261116T070000
RRULE:FREQ=DAILY;BYDAY=MO,TU;COUNT=4
END:VEVENT`),
			from: "2026-11-01", to: "2026-12-31",
			want: []string{
				"Mon Tue 2026-11-16T07:00+13/2026-11-16T07:00+13",
				"Mon Tue 2026-11-17T07:00+13/2026-11-17T07:00+13",
				"Every other day 2026-11-19T07:00+13/2026-11-19T07:00+13",
				"Every other day 2026-11-21T07:00+13/2026-11-21T07:00+13",
				"Every other day 2026-11-23T07:00+13/2026-11-23T07:00+13",
				"Mon Tue 2026-11-23T07:00+13/2026-11-23T07:00+13",
				"Mon Tue 2026-11-24T07:00+13/2026-11-24T07:00+13",
			},
		},
		// ---- RRULE: MONTHLY ----
		{
			name: "monthly last Friday (-1FR)",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Drinks
DTSTART;TZID=Pacific/Auckland:20261127T160000
DTEND;TZID=Pacific/Auckland:20261127T170000
RRULE:FREQ=MONTHLY;BYDAY=-1FR
END:VEVENT`),
			from: "2026-11-01", to: "2027-02-01",
			want: []string{
				"Drinks 2026-11-27T16:00+13/2026-11-27T17:00+13",
				"Drinks 2026-12-25T16:00+13/2026-12-25T17:00+13",
				"Drinks 2027-01-29T16:00+13/2027-01-29T17:00+13",
			},
		},
		{
			name: "monthly 2MO; DTSTART off-pattern is still first and counted",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Board
DTSTART:20261104T100000
RRULE:FREQ=MONTHLY;BYDAY=2MO;COUNT=3
END:VEVENT`),
			from: "2026-11-01", to: "2027-03-01",
			want: []string{
				"Board 2026-11-04T10:00+13/2026-11-04T10:00+13",
				"Board 2026-11-09T10:00+13/2026-11-09T10:00+13",
				"Board 2026-12-14T10:00+13/2026-12-14T10:00+13",
			},
		},
		{
			name: "monthly BYMONTHDAY=-1 (last day)",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Rent
DTSTART:20261130T120000
RRULE:FREQ=MONTHLY;BYMONTHDAY=-1
END:VEVENT`),
			from: "2026-11-01", to: "2027-03-01",
			want: []string{
				"Rent 2026-11-30T12:00+13/2026-11-30T12:00+13",
				"Rent 2026-12-31T12:00+13/2026-12-31T12:00+13",
				"Rent 2027-01-31T12:00+13/2027-01-31T12:00+13",
				"Rent 2027-02-28T12:00+13/2027-02-28T12:00+13",
			},
		},
		{
			name: "monthly on the 31st skips short months",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:31st
DTSTART:20261031T090000
RRULE:FREQ=MONTHLY;COUNT=4
END:VEVENT`),
			from: "2026-10-01", to: "2027-05-01",
			want: []string{
				"31st 2026-10-31T09:00+13/2026-10-31T09:00+13",
				"31st 2026-12-31T09:00+13/2026-12-31T09:00+13",
				"31st 2027-01-31T09:00+13/2027-01-31T09:00+13",
				"31st 2027-03-31T09:00+13/2027-03-31T09:00+13",
			},
		},
		{
			name: "monthly plain BYDAY=TU is every Tuesday",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Choir
DTSTART:20261201T180000
RRULE:FREQ=MONTHLY;BYDAY=TU;COUNT=6
END:VEVENT`),
			from: "2026-11-01", to: "2027-03-01",
			want: []string{
				"Choir 2026-12-01T18:00+13/2026-12-01T18:00+13",
				"Choir 2026-12-08T18:00+13/2026-12-08T18:00+13",
				"Choir 2026-12-15T18:00+13/2026-12-15T18:00+13",
				"Choir 2026-12-22T18:00+13/2026-12-22T18:00+13",
				"Choir 2026-12-29T18:00+13/2026-12-29T18:00+13",
				"Choir 2027-01-05T18:00+13/2027-01-05T18:00+13",
			},
		},
		{
			name: "monthly last weekday via BYSETPOS=-1",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Payroll
DTSTART:20261130T170000
RRULE:FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1
END:VEVENT`),
			from: "2026-11-01", to: "2027-03-01",
			want: []string{
				"Payroll 2026-11-30T17:00+13/2026-11-30T17:00+13",
				"Payroll 2026-12-31T17:00+13/2026-12-31T17:00+13",
				"Payroll 2027-01-29T17:00+13/2027-01-29T17:00+13",
				"Payroll 2027-02-26T17:00+13/2027-02-26T17:00+13",
			},
		},
		{
			name: "monthly Friday the 13th (BYDAY ∩ BYMONTHDAY), BYMONTH filter",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Spooky
DTSTART;VALUE=DATE:20261113
RRULE:FREQ=MONTHLY;BYDAY=FR;BYMONTHDAY=13
END:VEVENT
BEGIN:VEVENT
UID:2
SUMMARY:Summer 1st
DTSTART;VALUE=DATE:20261201
RRULE:FREQ=MONTHLY;BYMONTH=12,1,2
END:VEVENT`),
			from: "2026-11-01", to: "2027-09-01",
			want: []string{
				"Spooky 2026-11-13T00:00+13/2026-11-14T00:00+13 allday",
				"Summer 1st 2026-12-01T00:00+13/2026-12-02T00:00+13 allday",
				"Summer 1st 2027-01-01T00:00+13/2027-01-02T00:00+13 allday",
				"Summer 1st 2027-02-01T00:00+13/2027-02-02T00:00+13 allday",
				"Spooky 2027-08-13T00:00+12/2027-08-14T00:00+12 allday",
			},
		},
		// ---- RRULE: YEARLY ----
		{
			name: "yearly all-day birthday",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Mum's birthday
DTSTART;VALUE=DATE:19850312
DTEND;VALUE=DATE:19850313
RRULE:FREQ=YEARLY
END:VEVENT`),
			from: "2027-01-01", to: "2028-01-01",
			want: []string{"Mum's birthday 2027-03-12T00:00+13/2027-03-13T00:00+13 allday"},
		},
		{
			name: "yearly on Feb 29 only in leap years",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Leapling
DTSTART;VALUE=DATE:20240229
RRULE:FREQ=YEARLY
END:VEVENT`),
			from: "2026-01-01", to: "2029-01-01",
			want: []string{"Leapling 2028-02-29T00:00+13/2028-03-01T00:00+13 allday"},
		},
		{
			name: "yearly BYMONTH=11;BYDAY=4TH (Thanksgiving) in New York",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Thanksgiving
DTSTART;TZID=America/New_York:20251127T120000
DTEND;TZID=America/New_York:20251127T150000
RRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=4TH
END:VEVENT`),
			loc:  nyc,
			from: "2026-01-01", to: "2028-01-01",
			want: []string{
				"Thanksgiving 2026-11-26T12:00-05/2026-11-26T15:00-05",
				"Thanksgiving 2027-11-25T12:00-05/2027-11-25T15:00-05",
			},
		},
		{
			name: "yearly BYDAY=1MO without BYMONTH counts within the year",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:First Monday
DTSTART;VALUE=DATE:20260105
RRULE:FREQ=YEARLY;BYDAY=1MO
END:VEVENT`),
			from: "2026-06-01", to: "2028-01-01",
			want: []string{"First Monday 2027-01-04T00:00+13/2027-01-05T00:00+13 allday"},
		},
		// ---- UNTIL ----
		{
			name: "UNTIL in UTC is inclusive",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Daily
DTSTART;TZID=Pacific/Auckland:20261120T090000
DTEND;TZID=Pacific/Auckland:20261120T091500
RRULE:FREQ=DAILY;UNTIL=20261122T200000Z
END:VEVENT`),
			from: "2026-11-19", to: "2026-12-01",
			want: []string{
				"Daily 2026-11-20T09:00+13/2026-11-20T09:15+13",
				"Daily 2026-11-21T09:00+13/2026-11-21T09:15+13",
				"Daily 2026-11-22T09:00+13/2026-11-22T09:15+13",
				"Daily 2026-11-23T09:00+13/2026-11-23T09:15+13",
			},
		},
		{
			name: "UNTIL as DATE for all-day and timed events",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:A all-day
DTSTART;VALUE=DATE:20261120
RRULE:FREQ=DAILY;UNTIL=20261122
END:VEVENT
BEGIN:VEVENT
UID:2
SUMMARY:B timed
DTSTART:20261120T090000
RRULE:FREQ=DAILY;UNTIL=20261122
END:VEVENT`),
			from: "2026-11-19", to: "2026-12-01",
			want: []string{
				"A all-day 2026-11-20T00:00+13/2026-11-21T00:00+13 allday",
				"B timed 2026-11-20T09:00+13/2026-11-20T09:00+13",
				"A all-day 2026-11-21T00:00+13/2026-11-22T00:00+13 allday",
				"B timed 2026-11-21T09:00+13/2026-11-21T09:00+13",
				"A all-day 2026-11-22T00:00+13/2026-11-23T00:00+13 allday",
				"B timed 2026-11-22T09:00+13/2026-11-22T09:00+13",
			},
		},
		// ---- EXDATE / RDATE ----
		{
			name: "EXDATE: multiple lines, lists, TZID, UTC and VALUE=DATE",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Weekly
DTSTART;TZID=Pacific/Auckland:20261102T090000
DTEND;TZID=Pacific/Auckland:20261102T100000
RRULE:FREQ=WEEKLY
EXDATE;TZID=Pacific/Auckland:20261109T090000,20261116T090000
EXDATE:20261122T200000Z
EXDATE;VALUE=DATE:20261207
END:VEVENT
BEGIN:VEVENT
UID:2
SUMMARY:Bins
DTSTART;VALUE=DATE:20261103
RRULE:FREQ=WEEKLY;COUNT=3
EXDATE;VALUE=DATE:20261110
END:VEVENT`),
			from: "2026-11-01", to: "2027-01-01",
			want: []string{
				"Weekly 2026-11-02T09:00+13/2026-11-02T10:00+13",
				"Bins 2026-11-03T00:00+13/2026-11-04T00:00+13 allday",
				"Bins 2026-11-17T00:00+13/2026-11-18T00:00+13 allday",
				"Weekly 2026-11-30T09:00+13/2026-11-30T10:00+13",
				"Weekly 2026-12-14T09:00+13/2026-12-14T10:00+13",
				"Weekly 2026-12-21T09:00+13/2026-12-21T10:00+13",
				"Weekly 2026-12-28T09:00+13/2026-12-28T10:00+13",
			},
		},
		{
			name: "RDATE adds occurrences (deduplicated, PERIOD start)",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Class
DTSTART;TZID=Pacific/Auckland:20261120T090000
DTEND;TZID=Pacific/Auckland:20261120T100000
RRULE:FREQ=WEEKLY;COUNT=2
RDATE;TZID=Pacific/Auckland:20261122T090000,20261120T090000
RDATE;VALUE=PERIOD:20261124T000000Z/PT2H
END:VEVENT`),
			from: "2026-11-01", to: "2027-01-01",
			want: []string{
				"Class 2026-11-20T09:00+13/2026-11-20T10:00+13",
				"Class 2026-11-22T09:00+13/2026-11-22T10:00+13",
				"Class 2026-11-24T13:00+13/2026-11-24T14:00+13",
				"Class 2026-11-27T09:00+13/2026-11-27T10:00+13",
			},
		},
		{
			name: "RDATE on non-recurring event",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Twice
DTSTART;VALUE=DATE:20261120
RDATE;VALUE=DATE:20261125
END:VEVENT`),
			from: "2026-11-01", to: "2027-01-01",
			want: []string{
				"Twice 2026-11-20T00:00+13/2026-11-21T00:00+13 allday",
				"Twice 2026-11-25T00:00+13/2026-11-26T00:00+13 allday",
			},
		},
		// ---- RECURRENCE-ID overrides ----
		{
			name: "RECURRENCE-ID moves one instance (override listed first)",
			ics: vcal(`
BEGIN:VEVENT
UID:standup
RECURRENCE-ID;TZID=Pacific/Auckland:20261121T090000
SUMMARY:Standup (moved)
DTSTART;TZID=Pacific/Auckland:20261121T140000
DTEND;TZID=Pacific/Auckland:20261121T150000
END:VEVENT
BEGIN:VEVENT
UID:standup
SUMMARY:Standup
DTSTART;TZID=Pacific/Auckland:20261119T090000
DTEND;TZID=Pacific/Auckland:20261119T091500
RRULE:FREQ=DAILY;COUNT=5
END:VEVENT`),
			from: "2026-11-19", to: "2026-12-01",
			want: []string{
				"Standup 2026-11-19T09:00+13/2026-11-19T09:15+13",
				"Standup 2026-11-20T09:00+13/2026-11-20T09:15+13",
				"Standup (moved) 2026-11-21T14:00+13/2026-11-21T15:00+13",
				"Standup 2026-11-22T09:00+13/2026-11-22T09:15+13",
				"Standup 2026-11-23T09:00+13/2026-11-23T09:15+13",
			},
		},
		{
			name: "override moved out of and into the window",
			ics: vcal(`
BEGIN:VEVENT
UID:w
SUMMARY:Weekly
DTSTART;TZID=Pacific/Auckland:20261102T090000
DTEND;TZID=Pacific/Auckland:20261102T100000
RRULE:FREQ=WEEKLY
END:VEVENT
BEGIN:VEVENT
UID:w
RECURRENCE-ID:20261115T200000Z
SUMMARY:Moved out
DTSTART;TZID=Pacific/Auckland:20261125T090000
DTEND;TZID=Pacific/Auckland:20261125T100000
END:VEVENT
BEGIN:VEVENT
UID:w
RECURRENCE-ID;TZID=Pacific/Auckland:20261109T090000
SUMMARY:Moved in
DTSTART;TZID=Pacific/Auckland:20261118T100000
DTEND;TZID=Pacific/Auckland:20261118T110000
END:VEVENT`),
			from: "2026-11-16", to: "2026-11-23",
			want: []string{"Moved in 2026-11-18T10:00+13/2026-11-18T11:00+13"},
		},
		{
			name: "cancelled override drops the instance; higher SEQUENCE wins",
			ics: vcal(`
BEGIN:VEVENT
UID:d
SUMMARY:Daily
DTSTART;TZID=Pacific/Auckland:20261119T090000
DTEND;TZID=Pacific/Auckland:20261119T100000
RRULE:FREQ=DAILY;COUNT=4
END:VEVENT
BEGIN:VEVENT
UID:d
RECURRENCE-ID;TZID=Pacific/Auckland:20261120T090000
STATUS:CANCELLED
SEQUENCE:1
DTSTART;TZID=Pacific/Auckland:20261120T090000
END:VEVENT
BEGIN:VEVENT
UID:d
RECURRENCE-ID;TZID=Pacific/Auckland:20261121T090000
SEQUENCE:3
SUMMARY:Daily v3
DTSTART;TZID=Pacific/Auckland:20261121T110000
DTEND;TZID=Pacific/Auckland:20261121T120000
END:VEVENT
BEGIN:VEVENT
UID:d
RECURRENCE-ID;TZID=Pacific/Auckland:20261121T090000
SEQUENCE:2
SUMMARY:Daily v2
DTSTART;TZID=Pacific/Auckland:20261121T100000
DTEND;TZID=Pacific/Auckland:20261121T110000
END:VEVENT`),
			from: "2026-11-19", to: "2026-12-01",
			want: []string{
				"Daily 2026-11-19T09:00+13/2026-11-19T10:00+13",
				"Daily v3 2026-11-21T11:00+13/2026-11-21T12:00+13",
				"Daily 2026-11-22T09:00+13/2026-11-22T10:00+13",
			},
		},
		{
			name: "all-day override inherits title and duration",
			ics: vcal(`
BEGIN:VEVENT
UID:m
SUMMARY:Market
LOCATION:Square
DTSTART;VALUE=DATE:20261121
DTEND;VALUE=DATE:20261122
RRULE:FREQ=WEEKLY;COUNT=3
END:VEVENT
BEGIN:VEVENT
UID:m
RECURRENCE-ID;VALUE=DATE:20261128
DTSTART;VALUE=DATE:20261129
END:VEVENT`),
			from: "2026-11-01", to: "2027-01-01",
			want: []string{
				"Market 2026-11-21T00:00+13/2026-11-22T00:00+13 allday",
				"Market 2026-11-29T00:00+13/2026-11-30T00:00+13 allday",
				"Market 2026-12-05T00:00+13/2026-12-06T00:00+13 allday",
			},
		},
		// ---- unsupported / degenerate rules ----
		{
			name: "unsupported FREQ / BYWEEKNO fall back to DTSTART only",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Hourly
DTSTART:20261120T090000
RRULE:FREQ=HOURLY;COUNT=5
END:VEVENT
BEGIN:VEVENT
UID:2
SUMMARY:Weekno
DTSTART:20261120T100000
RRULE:FREQ=YEARLY;BYWEEKNO=20;BYDAY=MO
END:VEVENT`),
			from: "2026-11-01", to: "2028-01-01",
			want: []string{
				"Hourly 2026-11-20T09:00+13/2026-11-20T09:00+13",
				"Weekno 2026-11-20T10:00+13/2026-11-20T10:00+13",
			},
		},
		{
			name: "rule that never matches terminates",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Never
DTSTART:20260228T090000
RRULE:FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=30
END:VEVENT`),
			from: "2026-01-01", to: "9000-01-01",
			want: []string{"Never 2026-02-28T09:00+13/2026-02-28T09:00+13"},
		},
		{
			name: "sorted by start then title",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:Beta
DTSTART:20261120T090000
END:VEVENT
BEGIN:VEVENT
UID:2
SUMMARY:Alpha
DTSTART:20261120T090000
END:VEVENT
BEGIN:VEVENT
UID:3
SUMMARY:Early
DTSTART:20261120T080000
END:VEVENT`),
			from: "2026-11-20", to: "2026-11-21",
			want: []string{
				"Early 2026-11-20T08:00+13/2026-11-20T08:00+13",
				"Alpha 2026-11-20T09:00+13/2026-11-20T09:00+13",
				"Beta 2026-11-20T09:00+13/2026-11-20T09:00+13",
			},
		},
		{
			name: "empty window",
			ics: vcal(`
BEGIN:VEVENT
UID:1
SUMMARY:X
DTSTART:20261120T090000
END:VEVENT`),
			from: "2026-11-21", to: "2026-11-20",
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			loc := tc.loc
			if loc == nil {
				loc = akl
			}
			got, err := Parse(strings.NewReader(tc.ics), loc, day(loc, tc.from), day(loc, tc.to))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			checkEvents(t, got, tc.want)
			for _, e := range got {
				if e.Start.Location() != loc || e.End.Location() != loc {
					t.Errorf("%s: times not in loc: %v / %v", e.Title, e.Start.Location(), e.End.Location())
				}
			}
		})
	}
}

func TestParseFieldsUnfoldingAndEscapes(t *testing.T) {
	src := strings.Join([]string{
		"\ufeffBEGIN:VCALENDAR",
		"VERSION:2.0",
		`X-WR-CALNAME:Home\, sweet home`,
		"BEGIN:VEVENT",
		"UID:fields-1@example.com",
		`SUMMARY:Team sync\, plan`,
		` ning\; Q4 \\ rev`,
		"\tiew\\nline two",
		`LOCATION;LANGUAGE=en;ALTREP="http://example.com/a:b;c,d":Room 1\, Le`,
		` vel 2`,
		`X-APPLE-STRUCTURED-LOCATION;VALUE=URI;X-TITLE="Queen St: HQ";X-ADDRESS="1 Qu`,
		` een St":geo:-36.84,174.76`,
		"DTSTART;TZID=Pacific/Auckland:20261120T09",
		" 0000",
		"END:VEVENT",
		"END:VCALENDAR",
	}, "\r\n")
	got, err := Parse(strings.NewReader(src), akl, day(akl, "2026-11-20"), day(akl, "2026-11-21"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	e := got[0]
	want := Event{
		UID:      "fields-1@example.com",
		Title:    "Team sync, planning; Q4 \\ review\nline two",
		Location: "Room 1, Level 2",
		Calendar: "Home, sweet home",
		Start:    time.Date(2026, 11, 20, 9, 0, 0, 0, akl),
		End:      time.Date(2026, 11, 20, 9, 0, 0, 0, akl),
	}
	if e != want {
		t.Errorf("got  %+v\nwant %+v", e, want)
	}
}

func TestParseContentLine(t *testing.T) {
	tests := []struct {
		line   string
		ok     bool
		name   string
		params map[string]string
		value  string
	}{
		{line: "SUMMARY:Hello: world", ok: true, name: "SUMMARY", value: "Hello: world"},
		{line: "dtstart;tzid=Pacific/Auckland:20261119T090000", ok: true, name: "DTSTART",
			params: map[string]string{"TZID": "Pacific/Auckland"}, value: "20261119T090000"},
		{line: `X;A="x:y;z";B=1,"2,3",4:v`, ok: true, name: "X",
			params: map[string]string{"A": "x:y;z", "B": "1,2,3,4"}, value: "v"},
		{line: "DTSTART;VALUE=DATE:20261119", ok: true, name: "DTSTART",
			params: map[string]string{"VALUE": "DATE"}, value: "20261119"},
		{line: "EMPTY:", ok: true, name: "EMPTY", value: ""},
		{line: "no colon here", ok: false},
		{line: ":novalue", ok: false},
		{line: "bad name:x", ok: false},
		{line: `X;A="unterminated:v`, ok: false},
		{line: "X;A=1", ok: false},
	}
	for _, tc := range tests {
		p, ok := parseContentLine(tc.line)
		if ok != tc.ok {
			t.Errorf("%q: ok=%v want %v", tc.line, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if p.name != tc.name || p.value != tc.value || len(p.params) != len(tc.params) {
			t.Errorf("%q: got %+v", tc.line, p)
			continue
		}
		for k, v := range tc.params {
			if p.params[k] != v {
				t.Errorf("%q: param %s=%q want %q", tc.line, k, p.params[k], v)
			}
		}
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		in    string
		days  int
		exact time.Duration
		err   bool
	}{
		{in: "PT1H30M", exact: 90 * time.Minute},
		{in: "P1D", days: 1},
		{in: "P1W", days: 7},
		{in: "P1DT2H3M4S", days: 1, exact: 2*time.Hour + 3*time.Minute + 4*time.Second},
		{in: "-PT15M", exact: -15 * time.Minute},
		{in: "+PT10S", exact: 10 * time.Second},
		{in: "P", err: true},
		{in: "PT", err: true},
		{in: "1H", err: true},
		{in: "P1M", err: true}, // months are not valid in DURATION
		{in: "PTH", err: true},
	}
	for _, tc := range tests {
		d, e, err := parseDuration(tc.in)
		if (err != nil) != tc.err {
			t.Errorf("%q: err=%v want err=%v", tc.in, err, tc.err)
			continue
		}
		if !tc.err && (d != tc.days || e != tc.exact) {
			t.Errorf("%q: got %d days %v, want %d days %v", tc.in, d, e, tc.days, tc.exact)
		}
	}
}

func TestUnescapeText(t *testing.T) {
	tests := map[string]string{
		`plain`:            "plain",
		`a\,b\;c\\d\ne\Nf`: "a,b;c\\d\ne\nf",
		`trailing\`:        `trailing\`,
		`\x`:               "x",
	}
	for in, want := range tests {
		if got := unescapeText(in); got != want {
			t.Errorf("unescapeText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseNotICalendar(t *testing.T) {
	for name, src := range map[string]string{
		"empty":           "",
		"html":            "<!DOCTYPE html>\n<html><body>Sign in</body></html>\n",
		"json":            `{"error":"not found"}`,
		"vevent no vcal":  "BEGIN:VEVENT\nUID:1\nDTSTART:20261120T090000Z\nEND:VEVENT\n",
		"only whitespace": "\r\n \r\n\t\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(src), akl, day(akl, "2026-01-01"), day(akl, "2027-01-01"))
			if !errors.Is(err, errNotICalendar) {
				t.Errorf("err = %v, want errNotICalendar", err)
			}
		})
	}
	t.Run("reader error", func(t *testing.T) {
		boom := errors.New("boom")
		_, err := Parse(iotest.ErrReader(boom), akl, time.Time{}, time.Now())
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want wrapped boom", err)
		}
	})
	t.Run("empty calendar is not an error", func(t *testing.T) {
		got, err := Parse(strings.NewReader("BEGIN:VCALENDAR\nEND:VCALENDAR\n"), akl, time.Time{}, time.Now())
		if err != nil || len(got) != 0 {
			t.Errorf("got %v, %v; want no events, nil", got, err)
		}
	})
}

func TestParseGarbageAndMultipleCalendars(t *testing.T) {
	src := `garbage before the calendar
BEGIN:VCALENDAR
X-WR-CALNAME:First
this line is garbage
;also garbage
END:VTODO
BEGIN:VEVENT
UID:a
SUMMARY:From first
DTSTART:20261120T090000
END:VEVENT
BEGIN:VEVENT
UID:bad-start
SUMMARY:Bad start is skipped
DTSTART:not-a-date
END:VEVENT
BEGIN:VEVENT
UID:no-start
SUMMARY:No start is skipped
END:VEVENT
BEGIN:VEVENT
UID:bad-rule
SUMMARY:Bad rule is single
DTSTART:20261120T100000
RRULE:garbage
END:VEVENT
BEGIN:VEVENT
UID:truncated
SUMMARY:Truncated feed
DTSTART:20261120T110000
BEGIN:VCALENDAR
X-WR-CALNAME:Second
BEGIN:VEVENT
UID:b
SUMMARY:From second
DTSTART:20261120T120000
END:VEVENT
END:VCALENDAR
trailing garbage
BEGIN:VCALENDAR
X-WR-CALNAME:Third (no END)
BEGIN:VEVENT
UID:c
SUMMARY:From third
DTSTART:20261120T130000
`
	got, err := Parse(strings.NewReader(src), akl, day(akl, "2026-11-20"), day(akl, "2026-11-21"))
	if err != nil {
		t.Fatal(err)
	}
	var cals []string
	for _, e := range got {
		cals = append(cals, e.Title+"@"+e.Calendar)
	}
	want := []string{
		"From first@First",
		"Bad rule is single@First",
		"Truncated feed@First",
		"From second@Second",
		"From third@Third (no END)",
	}
	if !slices.Equal(cals, want) {
		t.Errorf("got %q\nwant %q", cals, want)
	}
}

const googleFeed = `BEGIN:VCALENDAR
PRODID:-//Google Inc//Google Calendar 70.9054//EN
VERSION:2.0
CALSCALE:GREGORIAN
METHOD:PUBLISH
X-WR-CALNAME:Family
X-WR-TIMEZONE:Pacific/Auckland
X-WR-CALDESC:Shared family calendar
BEGIN:VTIMEZONE
TZID:Pacific/Auckland
X-LIC-LOCATION:Pacific/Auckland
BEGIN:DAYLIGHT
TZOFFSETFROM:+1200
TZOFFSETTO:+1300
TZNAME:NZDT
DTSTART:19700927T020000
RRULE:FREQ=YEARLY;BYMONTH=9;BYDAY=-1SU
END:DAYLIGHT
BEGIN:STANDARD
TZOFFSETFROM:+1300
TZOFFSETTO:+1200
TZNAME:NZST
DTSTART:19700405T030000
RRULE:FREQ=YEARLY;BYMONTH=4;BYDAY=1SU
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
DTSTART;TZID=Pacific/Auckland:20261102T073000
DTEND;TZID=Pacific/Auckland:20261102T080000
RRULE:FREQ=WEEKLY;BYDAY=MO,TH
EXDATE;TZID=Pacific/Auckland:20261123T073000
DTSTAMP:20261118T000000Z
UID:7kq1m2a3b4c5d6e7f8g9h0@google.com
CREATED:20261001T000000Z
DESCRIPTION:Bring the <b>bins</b> out\, recycling week alternates.\nSee htt
 ps://example.com/bins
LAST-MODIFIED:20261001T000000Z
LOCATION:
SEQUENCE:0
STATUS:CONFIRMED
SUMMARY:School run
TRANSP:OPAQUE
END:VEVENT
BEGIN:VEVENT
DTSTART;TZID=Pacific/Auckland:20261126T090000
DTEND;TZID=Pacific/Auckland:20261126T093000
DTSTAMP:20261118T000000Z
UID:7kq1m2a3b4c5d6e7f8g9h0@google.com
RECURRENCE-ID;TZID=Pacific/Auckland:20261126T073000
CREATED:20261001T000000Z
LAST-MODIFIED:20261117T000000Z
SEQUENCE:1
STATUS:CONFIRMED
SUMMARY:School run (late start)
TRANSP:OPAQUE
END:VEVENT
BEGIN:VEVENT
DTSTART;VALUE=DATE:20261121
DTEND;VALUE=DATE:20261123
DTSTAMP:20261118T000000Z
UID:0a1b2c3d4e5f@google.com
SUMMARY:Camping\, Coromandel
LOCATION:Cathedral Cove\, Hahei
STATUS:CONFIRMED
TRANSP:TRANSPARENT
END:VEVENT
BEGIN:VEVENT
DTSTART:20261124T013000Z
DTEND:20261124T023000Z
DTSTAMP:20261118T000000Z
UID:dentist@google.com
SUMMARY:Dentist
STATUS:CANCELLED
END:VEVENT
BEGIN:VEVENT
DTSTART:20261124T220000Z
DTEND:20261124T230000Z
DTSTAMP:20261118T000000Z
UID:call@google.com
SUMMARY:Call with Mum
BEGIN:VALARM
ACTION:EMAIL
DESCRIPTION:This is an event reminder
SUMMARY:Alarm notification
ATTENDEE:mailto:someone@example.com
TRIGGER:-P0DT0H30M0S
END:VALARM
END:VEVENT
END:VCALENDAR
`

func TestGoogleStyleFeed(t *testing.T) {
	src := strings.ReplaceAll(googleFeed, "\n", "\r\n")
	from := day(akl, "2026-11-19")
	got, err := Parse(strings.NewReader(src), akl, from, from.AddDate(0, 0, 8))
	if err != nil {
		t.Fatal(err)
	}
	checkEvents(t, got, []string{
		"School run 2026-11-19T07:30+13/2026-11-19T08:00+13",
		"Camping, Coromandel 2026-11-21T00:00+13/2026-11-23T00:00+13 allday",
		"Call with Mum 2026-11-25T11:00+13/2026-11-25T12:00+13",
		"School run (late start) 2026-11-26T09:00+13/2026-11-26T09:30+13",
	})
	for _, e := range got {
		if e.Calendar != "Family" {
			t.Errorf("%s: Calendar = %q, want Family", e.Title, e.Calendar)
		}
	}
	if len(got) > 1 && got[1].Location != "Cathedral Cove, Hahei" {
		t.Errorf("Location = %q", got[1].Location)
	}
	if len(got) > 0 && got[0].UID != "7kq1m2a3b4c5d6e7f8g9h0@google.com" {
		t.Errorf("UID = %q", got[0].UID)
	}
}

const icloudFeed = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Apple Inc.//macOS 15.1//EN
CALSCALE:GREGORIAN
X-WR-CALNAME:Work
X-APPLE-CALENDAR-COLOR:#1BADF8
X-WR-TIMEZONE:Pacific/Auckland
BEGIN:VTIMEZONE
TZID:Pacific/Auckland
BEGIN:DAYLIGHT
DTSTART:20070930T020000
RRULE:FREQ=YEARLY;BYMONTH=9;BYDAY=-1SU
TZNAME:NZDT
TZOFFSETFROM:+1200
TZOFFSETTO:+1300
END:DAYLIGHT
BEGIN:STANDARD
DTSTART:20080406T030000
RRULE:FREQ=YEARLY;BYMONTH=4;BYDAY=1SU
TZNAME:NZST
TZOFFSETFROM:+1300
TZOFFSETTO:+1200
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
CREATED:20260101T000000Z
DTEND;TZID=Pacific/Auckland:20261120T113000
DTSTAMP:20261118T010203Z
DTSTART;TZID=Pacific/Auckland:20261120T100000
LAST-MODIFIED:20261101T000000Z
LOCATION:Level 3\, 1 Queen Street\nAuckland 1010\, New Zealand
RRULE:FREQ=WEEKLY;INTERVAL=2;BYDAY=FR;WKST=SU
SEQUENCE:0
SUMMARY:Fortnightly 1:1
TRANSP:OPAQUE
UID:6A3B5C1E-0F4E-4C2D-9E1F-2B7A9D3C4E5F
X-APPLE-STRUCTURED-LOCATION;VALUE=URI;X-ADDRESS="Level 3, 1 Queen Street\\n
 Auckland 1010";X-APPLE-RADIUS=70;X-TITLE="Queen St: HQ":geo:-36.846,174.7
 66
X-APPLE-TRAVEL-ADVISORY-BEHAVIOR:AUTOMATIC
BEGIN:VALARM
ACTION:DISPLAY
DESCRIPTION:Reminder
TRIGGER:-PT15M
UID:8F1C2D3E-4A5B-4C6D-8E9F-0A1B2C3D4E5F
X-WR-ALARMUID:8F1C2D3E-4A5B-4C6D-8E9F-0A1B2C3D4E5F
END:VALARM
END:VEVENT
BEGIN:VEVENT
CREATED:20260101T000000Z
DTSTAMP:20261118T010203Z
DTSTART;VALUE=DATE:20261103
EXDATE;VALUE=DATE:20261201
RRULE:FREQ=WEEKLY;BYDAY=TU
SEQUENCE:0
SUMMARY:Recycling
UID:B1C2D3E4-F5A6-4B7C-8D9E-0F1A2B3C4D5E
END:VEVENT
BEGIN:VEVENT
CREATED:20260101T000000Z
DTSTAMP:20261118T010203Z
DTSTART:20261122T123000
DURATION:PT1H
SUMMARY:Lunch
UID:C1D2E3F4-A5B6-4C7D-8E9F-1A2B3C4D5E6F
END:VEVENT
END:VCALENDAR
`

func TestICloudStyleFeed(t *testing.T) {
	src := strings.ReplaceAll(icloudFeed, "\n", "\r\n")
	got, err := Parse(strings.NewReader(src), akl, day(akl, "2026-11-19"), day(akl, "2026-12-10"))
	if err != nil {
		t.Fatal(err)
	}
	checkEvents(t, got, []string{
		"Fortnightly 1:1 2026-11-20T10:00+13/2026-11-20T11:30+13",
		"Lunch 2026-11-22T12:30+13/2026-11-22T13:30+13",
		"Recycling 2026-11-24T00:00+13/2026-11-25T00:00+13 allday",
		"Fortnightly 1:1 2026-12-04T10:00+13/2026-12-04T11:30+13",
		"Recycling 2026-12-08T00:00+13/2026-12-09T00:00+13 allday",
	})
	for _, e := range got {
		if e.Calendar != "Work" {
			t.Errorf("%s: Calendar = %q, want Work", e.Title, e.Calendar)
		}
	}
	if len(got) > 0 && got[0].Location != "Level 3, 1 Queen Street\nAuckland 1010, New Zealand" {
		t.Errorf("Location = %q", got[0].Location)
	}
}

func TestLongRunningDailySeries(t *testing.T) {
	src := vcal(`
BEGIN:VEVENT
UID:old
SUMMARY:Since 1990
DTSTART;TZID=Pacific/Auckland:19900101T063000
RRULE:FREQ=DAILY
END:VEVENT`)
	from := day(akl, "2026-11-19")
	got, err := Parse(strings.NewReader(src), akl, from, from.AddDate(0, 0, 3))
	if err != nil {
		t.Fatal(err)
	}
	checkEvents(t, got, []string{
		"Since 1990 2026-11-19T06:30+13/2026-11-19T06:30+13",
		"Since 1990 2026-11-20T06:30+13/2026-11-20T06:30+13",
		"Since 1990 2026-11-21T06:30+13/2026-11-21T06:30+13",
	})
}

func TestAncientRecurrenceStillShows(t *testing.T) {
	// Without COUNT the walk starts near the window: a daily event from 1700
	// (over 100 000 periods ago) and a 3-weekly, monthly and yearly one still
	// show today's occurrence.
	for _, rule := range []string{"FREQ=DAILY", "FREQ=WEEKLY;INTERVAL=3", "FREQ=MONTHLY;BYDAY=-1FR", "FREQ=YEARLY;INTERVAL=4"} {
		src := vcal(`BEGIN:VEVENT
UID:old
DTSTART;TZID=Pacific/Auckland:17001126T090000
DTEND;TZID=Pacific/Auckland:17001126T100000
SUMMARY:Old
RRULE:` + rule + `
END:VEVENT`)
		got, err := Parse(strings.NewReader(src), akl, day(akl, "1700-01-01"), day(akl, "1700-01-02"))
		if err != nil || len(got) != 0 {
			t.Fatalf("%s: before the start: %v %v", rule, got, err)
		}
		got, err = Parse(strings.NewReader(src), akl, day(akl, "2026-01-01"), day(akl, "2030-12-31"))
		if err != nil || len(got) == 0 {
			t.Fatalf("%s: no occurrences in 2026-2030: %v", rule, err)
		}
		want := 2026
		if rule == "FREQ=YEARLY;INTERVAL=4" {
			want = 2028 // 1700 + 4k
		}
		if f := got[0].Start; f.Year() != want {
			t.Errorf("%s: first occurrence %v", rule, f)
		}
	}
	// A huge INTERVAL makes the rule unusable: the event shows once.
	src := vcal(`BEGIN:VEVENT
UID:huge
DTSTART:20260101T090000Z
RRULE:FREQ=DAILY;INTERVAL=99999999999
END:VEVENT`)
	got, err := Parse(strings.NewReader(src), akl, day(akl, "2025-12-01"), day(akl, "2027-01-01"))
	if err != nil || len(got) != 1 {
		t.Fatalf("huge interval: %v %v", got, err)
	}
}
