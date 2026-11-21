package alert

import (
	"testing"
	"time"

	"github.com/okchum/mantel/internal/state"
)

// One failing message must not make the others go out again.
func TestPartialFailureResendsOnlyFailed(t *testing.T) {
	r := newRig()
	r.n.failT = "额度"
	r.step(0, func(s *state.Snapshot) {
		s.Servers[0].Error = "x"
		q := &s.Quotas[0]
		q.Value, q.Unit, q.Mode, q.Updated = 5, "¥", "remaining", r.now
	})
	r.step(3*time.Minute, nil)
	r.n.failT = ""
	for range 20 {
		r.step(15*time.Second, nil)
	}
	count := map[string]int{}
	for _, m := range r.n.got {
		count[m.Title]++
	}
	if count["nas 离线"] != 1 || count["deepseek 额度不足"] != 1 || len(r.n.got) != 2 {
		t.Fatalf("each alert exactly once, got %+v", r.n.got)
	}
}

func TestFailingNotifierBacksOff(t *testing.T) {
	r := newRig()
	r.n.fail = true
	r.step(0, func(s *state.Snapshot) { s.Servers[0].Error = "x" })
	r.step(3*time.Minute, nil) // first attempt
	for range 40 {             // 10 minutes of 15s checks
		r.step(15*time.Second, nil)
	}
	// Without backoff that is 41 attempts; with 15s, 30s, 1m, 2m, 4m, 8m… far fewer.
	if r.n.calls < 3 || r.n.calls > 8 {
		t.Fatalf("attempts = %d, want a backed-off handful", r.n.calls)
	}
	r.n.fail = false
	for range 50 {
		r.step(15*time.Second, nil)
	}
	if len(r.n.got) != 1 {
		t.Fatalf("delivered once the notifier is back: %+v", r.n.got)
	}
}

// A recovery that could not be sent is dropped if the target is down again by
// the time the notifier works, rather than announcing "recovered" while down.
func TestQueuedRecoveryDroppedWhenDownAgain(t *testing.T) {
	r := newRig()
	r.step(0, func(s *state.Snapshot) { s.Servers[0].Error = "x" })
	r.step(3*time.Minute, nil)
	if len(r.n.got) != 1 {
		t.Fatalf("want the down alert, got %+v", r.n.got)
	}
	r.n.failT = "恢复"
	r.step(15*time.Second, func(s *state.Snapshot) { s.Servers[0].Up, s.Servers[0].Error = true, "" })
	if len(r.e.pendingUp) != 1 {
		t.Fatalf("recovery should be queued: %+v", r.e.pendingUp)
	}
	r.step(15*time.Second, func(s *state.Snapshot) { s.Servers[0].Up, s.Servers[0].Error = false, "y" })
	r.n.failT = ""
	for range 20 {
		r.step(15*time.Second, nil)
	}
	for _, m := range r.n.got {
		if m.Title == "恢复" {
			t.Fatalf("sent a stale recovery while down: %+v", r.n.got)
		}
	}
	if len(r.e.pendingUp) != 0 {
		t.Fatalf("stale recovery still queued: %+v", r.e.pendingUp)
	}
}

// The earlier down alert still stands when a queued recovery is dropped, so a
// short relapse that ends inside the grace period still gets its recovery.
func TestRecoveryAfterBriefRelapseIsSent(t *testing.T) {
	r := newRig()
	r.step(0, func(s *state.Snapshot) { s.Servers[0].Error = "x" })
	r.step(3*time.Minute, nil)
	r.n.failT = "恢复"
	r.step(15*time.Second, func(s *state.Snapshot) { s.Servers[0].Up, s.Servers[0].Error = true, "" })
	r.step(15*time.Second, func(s *state.Snapshot) { s.Servers[0].Up, s.Servers[0].Error = false, "y" })
	r.n.failT = ""
	r.step(30*time.Second, func(s *state.Snapshot) { s.Servers[0].Up, s.Servers[0].Error = true, "" })
	for range 4 {
		r.step(15*time.Minute, nil) // past any send backoff
	}
	var titles []string
	for _, m := range r.n.got {
		titles = append(titles, m.Title)
	}
	if len(titles) != 2 || titles[0] != "nas 离线" || titles[1] != "恢复" {
		t.Fatalf("want down then recovery, got %q", titles)
	}
}

// A scheduled router reboot: an outage inside the maintenance window is not
// reported, and one that outlasts it is reported After past the window's end.
func TestMaintenanceWindowHoldsOutageReports(t *testing.T) {
	r := newRig() // 03:00 UTC
	r.e.Maintenance, r.e.Loc = []string{"03:05-03:15"}, time.UTC
	up := func(s *state.Snapshot) { s.Servers[0].Up, s.Servers[0].Error = true, "" }
	down := func(s *state.Snapshot) { s.Servers[0].Up, s.Servers[0].Error = false, "unreachable" }
	r.step(0, up)
	r.step(6*time.Minute, down) // 03:06, inside the window
	for range 30 {              // to 03:13:30
		r.step(15*time.Second, nil)
	}
	r.step(4*time.Minute, up) // 03:17:30: back within After of the window's end
	if len(r.n.got) != 0 {
		t.Fatalf("outage inside the window was reported: %+v", r.n.got)
	}

	r.step(24*time.Hour-17*time.Minute-30*time.Second+6*time.Minute, down) // next day 03:06
	r.step(10*time.Minute, nil)                                            // 03:16
	if len(r.n.got) != 0 {
		t.Fatalf("reported before After past the window: %+v", r.n.got)
	}
	r.step(time.Minute+15*time.Second, nil) // 03:17:15
	if len(r.n.got) != 1 || r.n.got[0].Title != "nas 离线" {
		t.Fatalf("outage that outlasts the window: %+v", r.n.got)
	}

	// Outside any window, After applies as usual.
	r.step(time.Hour, up)
	r.step(15*time.Second, down)
	r.step(2*time.Minute+15*time.Second, nil)
	if len(r.n.got) != 3 { // + recovery, + the new outage
		t.Fatalf("outside the window: %+v", r.n.got)
	}
}
