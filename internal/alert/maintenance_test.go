package alert

import (
	"testing"
	"time"

	"github.com/okchum/mantel/internal/state"
)

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("no tzdata for %s: %v", name, err)
	}
	return loc
}

// An outage that lasts: the "offline" message goes out After (2m) past the
// end of the maintenance window occurrence it started in, also across
// midnight and on the days DST moves the clock.
func TestMaintenanceWindowOccurrences(t *testing.T) {
	nz := mustLoad(t, "Pacific/Auckland")
	utc := func(mo time.Month, d, h, m int) time.Time { return time.Date(2026, mo, d, h, m, 0, 0, time.UTC) }
	cases := []struct {
		name     string
		window   string
		loc      *time.Location
		down     time.Time
		wantSent time.Time
	}{
		{"normal day", "02:30-03:30", nz, utc(4, 11, 14, 35), utc(4, 11, 15, 32)}, // 02:35 NZST → 03:32 NZST
		// Fall-back day: 03:00 NZDT becomes 02:00 NZST, so the clock reads
		// 02:00-03:00 twice. The window runs from the first 02:30 (NZDT) to
		// 03:30 NZST; it used to report at 02:00 NZST, mid-window.
		{"fall-back day", "02:30-03:30", nz, utc(4, 4, 13, 35), utc(4, 4, 15, 32)}, // 02:35 NZDT → 03:32 NZST
		{"window in the repeated hour", "02:10-02:40", nz, utc(4, 4, 13, 15), utc(4, 4, 14, 42)},
		// Spring-forward day: 02:00 NZST jumps to 03:00 NZDT, so 02:30 never
		// comes; the window ends at the jump. It used to report at once.
		{"spring-forward day", "01:30-02:30", nz, utc(9, 26, 13, 40), utc(9, 26, 14, 2)}, // 01:40 NZST → 03:02 NZDT
		// A window lost wholly to the gap holds nothing back.
		{"window inside the gap", "02:10-02:40", nz, utc(9, 26, 13, 50), utc(9, 26, 13, 52)},
		{"across midnight, before it", "23:00-01:00", time.UTC, utc(11, 19, 23, 30), utc(11, 20, 1, 2)},
		{"across midnight, after it", "23:00-01:00", time.UTC, utc(11, 20, 0, 30), utc(11, 20, 1, 2)},
		// Down just before the window: the window still counts as expected.
		{"into the window", "23:00-01:00", time.UTC, utc(11, 19, 22, 59), utc(11, 20, 1, 2)},
		{"before the window", "23:00-01:00", time.UTC, utc(11, 19, 22, 50), utc(11, 19, 22, 52)},
		{"after the window", "23:00-01:00", time.UTC, utc(11, 20, 1, 30), utc(11, 20, 1, 32)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig()
			r.now = c.down
			r.e.Maintenance, r.e.Loc = []string{c.window}, c.loc
			r.step(0, func(s *state.Snapshot) { s.Servers[0].Up, s.Servers[0].Error = false, "unreachable" })
			for limit := c.down.Add(6 * time.Hour); len(r.n.got) == 0 && r.now.Before(limit); {
				r.step(15*time.Second, nil)
			}
			if len(r.n.got) != 1 || !r.now.Equal(c.wantSent) {
				t.Fatalf("sent %+v at %s, want one at %s", r.n.got, r.now.In(c.loc).Format("01-02 15:04:05 MST"), c.wantSent.In(c.loc).Format("01-02 15:04:05 MST"))
			}
		})
	}
}

func TestClockAt(t *testing.T) {
	nz := mustLoad(t, "Pacific/Auckland")
	ny := mustLoad(t, "America/New_York")
	cases := []struct {
		name   string
		loc    *time.Location
		mo     time.Month
		d, hm  int
		latest bool
		want   time.Time
	}{
		{"plain", nz, 11, 21, 9*60 + 15, false, time.Date(2026, 11, 20, 20, 15, 0, 0, time.UTC)},
		{"plain latest", nz, 11, 21, 9*60 + 15, true, time.Date(2026, 11, 20, 20, 15, 0, 0, time.UTC)},
		{"day overflow", time.UTC, 11, 31, 30, false, time.Date(2026, 12, 1, 0, 30, 0, 0, time.UTC)},
		{"repeated, first", nz, 4, 5, 2*60 + 30, false, time.Date(2026, 4, 4, 13, 30, 0, 0, time.UTC)},
		{"repeated, last", nz, 4, 5, 2*60 + 30, true, time.Date(2026, 4, 4, 14, 30, 0, 0, time.UTC)},
		{"skipped", nz, 9, 27, 2*60 + 30, false, time.Date(2026, 9, 26, 14, 0, 0, 0, time.UTC)},
		{"skipped latest", nz, 9, 27, 2*60 + 30, true, time.Date(2026, 9, 26, 14, 0, 0, 0, time.UTC)},
		{"skipped, west", ny, 3, 8, 2*60 + 30, false, time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)},
		{"repeated, west", ny, 11, 1, 60 + 30, true, time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		if got := clockAt(2026, c.mo, c.d, c.hm, c.loc, c.latest); !got.Equal(c.want) {
			t.Errorf("%s: clockAt = %s, want %s", c.name, got.UTC(), c.want)
		}
	}
}
