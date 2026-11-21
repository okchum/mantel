package collect

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/okchum/mantel/internal/calendar"
	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/httpx"
	"github.com/okchum/mantel/internal/state"
)

// maxICS bounds one calendar download; real feeds are a few hundred KiB.
const maxICS = 8 << 20

// Calendars polls every ICS feed and publishes the merged agenda for the next week.
//
// Until some feed has been fetched successfully the agenda restored from the
// state file is left alone (only the error is published): at boot the network
// is often not up yet, and an empty agenda would replace a good one. Named
// feeds start from their restored events so one failing feed does not drop
// its events when another succeeds.
func Calendars(ctx context.Context, st *state.Store, cals []config.Calendar, loc *time.Location, interval time.Duration) {
	client := NewClient(ClientOpts{Timeout: 20 * time.Second})
	last := seedCalendars(cals, st.Read().Events) // last good result per feed
	anyOK := false
	every(ctx, "calendar", interval, func() {
		now := time.Now().In(loc)
		from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		to := from.AddDate(0, 0, 8)
		var errs []string
		for i, c := range cals {
			evs, err := fetchCalendar(ctx, client, c, loc, from, to)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", calName(c), err))
				continue
			}
			last[i] = evs
			anyOK = true
		}
		if ctx.Err() != nil {
			return
		}
		if !anyOK {
			st.Update(func(s *state.Snapshot) { s.EventsErr = strings.Join(errs, "; ") })
			return
		}
		merged := []state.Event{}
		for _, evs := range last {
			for _, e := range evs {
				if e.End.After(from) || (e.End.Equal(e.Start) && !e.Start.Before(from)) {
					merged = append(merged, e)
				}
			}
		}
		sort.SliceStable(merged, func(a, b int) bool {
			if !merged[a].Start.Equal(merged[b].Start) {
				return merged[a].Start.Before(merged[b].Start)
			}
			return merged[a].AllDay && !merged[b].AllDay
		})
		st.Update(func(s *state.Snapshot) {
			s.Events = merged
			s.EventsErr = strings.Join(errs, "; ")
		})
	})
}

// seedCalendars attributes restored events to feeds by their configured name;
// unnamed feeds and names shared by several feeds start empty.
func seedCalendars(cals []config.Calendar, restored []state.Event) [][]state.Event {
	last := make([][]state.Event, len(cals))
	uses := map[string]int{}
	for _, c := range cals {
		uses[c.Name]++
	}
	for i, c := range cals {
		if c.Name == "" || uses[c.Name] > 1 {
			continue
		}
		for _, e := range restored {
			if e.Calendar == c.Name {
				last[i] = append(last[i], e)
			}
		}
	}
	return last
}

func calName(c config.Calendar) string {
	if c.Name != "" {
		return c.Name
	}
	return "calendar"
}

func fetchCalendar(ctx context.Context, client *http.Client, c config.Calendar, loc *time.Location, from, to time.Time) ([]state.Event, error) {
	u := c.URL
	if strings.HasPrefix(u, "webcal://") {
		u = "https://" + strings.TrimPrefix(u, "webcal://")
	}
	resp, err := httpx.Do(ctx, client, httpx.Request{URL: u})
	if err != nil {
		return nil, shortErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
	evs, err := calendar.Parse(httpx.CapReader(resp.Body, maxICS), loc, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]state.Event, 0, len(evs))
	for _, e := range evs {
		name := e.Calendar
		if c.Name != "" {
			name = c.Name
		}
		out = append(out, state.Event{
			Title: e.Title, Location: e.Location, Calendar: name, Color: c.Color,
			Start: e.Start, End: e.End, AllDay: e.AllDay,
		})
	}
	return out, nil
}
