package action

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/state"
)

func TestRunHTTPSharesClients(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":"ok"}`))
	}))
	defer srv.Close()
	r := newRunner(
		config.Action{Name: "strict", Type: "http", Method: "POST", URL: srv.URL},
		config.Action{Name: "lax", Type: "http", Method: "POST", URL: srv.URL, Insecure: true},
	)
	if res, err := r.Run("strict", "", "t"); err != nil || res.OK {
		t.Fatalf("self-signed cert must fail verification: %+v %v", res, err)
	}
	for i := 0; i < 3; i++ {
		if res, err := r.Run("lax", "", "t"); err != nil || !res.OK {
			t.Fatalf("insecure run %d: %+v %v", i, res, err)
		}
		r.last = nil // skip the cooldown
	}
	if c := insecureClient.Transport; c == nil || c == http.DefaultTransport {
		t.Fatal("insecure client must have its own transport")
	}
}

func TestExecParamOptionInjection(t *testing.T) {
	if _, err := exec.LookPath("echo"); err != nil {
		t.Skip("no echo")
	}
	r := newRunner(config.Action{
		Name: "say", Type: "exec", Command: []string{"echo", "{param}"},
		Param: &config.Param{Values: []string{"-n"}, Pattern: `[a-z-]+`}, Cooldown: dur(time.Nanosecond),
	})
	if _, err := r.Run("say", "-rf", "t"); !errors.Is(err, ErrParam) {
		t.Fatalf("pattern-matched value starting with - must be refused, got %v", err)
	}
	if _, err := r.Run("say", "hi-there", "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run("say", "-n", "t"); err != nil {
		t.Fatalf("a value listed verbatim may look like a flag: %v", err)
	}
	// Other types are not argv: "-" is fine there.
	r2 := newRunner(config.Action{Name: "d", Type: "demo", Param: &config.Param{Pattern: `.+`}})
	r2.Demo = func(name, param string) (string, error) { return param, nil }
	if _, err := r2.Run("d", "-x", "t"); err != nil {
		t.Fatal(err)
	}
}

func TestParamResultsArePruned(t *testing.T) {
	r := newRunner(config.Action{Name: "d", Type: "demo", Param: &config.Param{Pattern: `p\d+`}, Cooldown: dur(time.Nanosecond)})
	r.Demo = func(name, param string) (string, error) { return "", nil }
	// Keep the test fast: the demo type waits 1.2s, so record directly.
	base := time.Now()
	for i := 0; i < MaxParams+10; i++ {
		r.record(fmt.Sprintf("d:p%d", i), state.ActionResult{At: base.Add(time.Duration(i) * time.Second)})
	}
	r.record("other", state.ActionResult{At: base})
	acts := r.Store.Read().Actions
	n := 0
	for k := range acts {
		if strings.HasPrefix(k, "d:") {
			n++
		}
	}
	if n != MaxParams {
		t.Fatalf("kept %d param results, want %d", n, MaxParams)
	}
	if _, ok := acts["d:p0"]; ok {
		t.Error("oldest result must go first")
	}
	if _, ok := acts[fmt.Sprintf("d:p%d", MaxParams+9)]; !ok {
		t.Error("newest result must stay")
	}
	if _, ok := acts["other"]; !ok {
		t.Error("other actions untouched")
	}

}

func TestShutdownCancelsAndWaits(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("no sleep")
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := newRunner(config.Action{Name: "slow", Type: "exec", Command: []string{"sleep", "30"}, Timeout: dur(time.Minute)})
	r.Context = ctx
	done := make(chan state.ActionResult, 1)
	go func() {
		res, _ := r.Run("slow", "", "t")
		done <- res
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		busy := r.running["slow"]
		r.mu.Unlock()
		if busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("action never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // let the command start
	start := time.Now()
	cancel()
	if !r.Shutdown(5 * time.Second) {
		t.Fatal("Shutdown timed out")
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("cancelled action took %s to stop", took)
	}
	if res := <-done; res.OK {
		t.Fatalf("cancelled action reported OK: %+v", res)
	}
	if _, err := r.Run("slow", "", "t"); !errors.Is(err, ErrClosed) {
		t.Fatalf("runs after Shutdown must be refused, got %v", err)
	}
}

func TestShutdownTimesOut(t *testing.T) {
	r := newRunner(config.Action{Name: "d", Type: "demo"})
	block := make(chan struct{})
	r.Demo = func(name, param string) (string, error) { <-block; return "", nil }
	go func() { _, _ = r.Run("d", "", "t") }()
	time.Sleep(50 * time.Millisecond)
	if r.Shutdown(10 * time.Millisecond) {
		t.Fatal("Shutdown must report actions still running")
	}
	close(block)
	if !r.Shutdown(5 * time.Second) {
		t.Fatal("finished action must let Shutdown return")
	}
}

func TestTimeoutDefault(t *testing.T) {
	r := newRunner(config.Action{Name: "a", Type: "demo"}, config.Action{Name: "b", Type: "demo", Timeout: dur(90 * time.Second)})
	if r.Timeout("a") != DefaultTimeout || r.Timeout("b") != 90*time.Second {
		t.Fatal(r.Timeout("a"), r.Timeout("b"))
	}
}
