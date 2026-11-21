//go:build unix

package collect

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunCommandTimeoutKillsGrandchildren(t *testing.T) {
	// The background sleep is a grandchild: killing only sh used to leave it
	// running (and holding stdout) after the timeout.
	start := time.Now()
	out, err := RunCommand(context.Background(), []string{"sh", "-c", "sleep 30 & echo $!; sleep 30"}, "", nil, 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want a timeout, got %v", err)
	}
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("RunCommand took %s: the grandchild kept stdout open", d)
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(string(out)))
	if perr != nil {
		t.Fatalf("no grandchild pid in %q", out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild %d still alive after the timeout (kill(0) = %v)", pid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
