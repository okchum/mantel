package state

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGenerationAndAutosaveSkipsUnchanged(t *testing.T) {
	st := NewStore("t", "", []string{"nas"}, nil, nil, nil)
	g0 := st.Generation()
	st.Update(func(s *Snapshot) { s.Servers[0].Up = true })
	if st.Generation() != g0+1 {
		t.Fatal("Update must bump the generation")
	}
	if s := st.Read(); s.Gen != st.Generation() {
		t.Fatalf("Read Gen %d, store %d", s.Gen, st.Generation())
	}

	path := filepath.Join(t.TempDir(), "state.json")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go st.Autosave(ctx, path, 10*time.Millisecond, done)
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("nothing changed since start: no save expected (%v)", err)
	}
	st.Update(func(s *Snapshot) { s.Servers[0].Load1 = 1 })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("changed state was never saved")
		}
		time.Sleep(5 * time.Millisecond)
	}
	fi1, _ := os.Stat(path)
	time.Sleep(60 * time.Millisecond)
	fi2, _ := os.Stat(path)
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Fatal("unchanged state was written again")
	}
	cancel()
	<-done
}

func TestSaveConcurrentWithUpdates(t *testing.T) {
	st := NewStore("t", "", []string{"nas"}, nil, nil, []string{"big"})
	path := filepath.Join(t.TempDir(), "state.json")
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			st.Update(func(s *Snapshot) {
				s.Servers[0].CPUHist = Push(s.Servers[0].CPUHist, float64(i), 60)
				s.Sources[0].Data = map[string]any{"i": i}
			})
		}
	}()
	for i := 0; i < 20; i++ {
		if err := st.Save(path); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestReadKeepsEmptySlicesNonNil(t *testing.T) {
	st := NewStore("t", "", nil, nil, nil, nil)
	b, _ := json.Marshal(st.Read())
	if !strings.Contains(string(b), `"events":[]`) {
		t.Fatalf("%s", b)
	}
}

func TestForViewer(t *testing.T) {
	s := Snapshot{
		Probes:  []Probe{{Name: "p", URL: "https://user:secret@example.com/health?token=abc#frag"}},
		Actions: map[string]ActionResult{"restart": {OK: true, By: "alice", Message: "token=abc failed", At: time.Unix(5, 0)}},
		Asleep:  &Asleep{On: true, By: "phone"},
	}
	v := s.ForViewer()
	if v.Probes[0].URL != "https://example.com/health" || v.Actions["restart"].By != "" || v.Asleep.By != "" || !v.Redacted {
		t.Fatalf("%+v", v)
	}
	if a := v.Actions["restart"]; a.Message != "" || !a.OK || !a.At.Equal(time.Unix(5, 0)) {
		t.Fatalf("viewer action result: %+v (message hidden, ok/time kept)", a)
	}
	if s.Probes[0].URL == v.Probes[0].URL || s.Actions["restart"].By != "alice" || s.Actions["restart"].Message == "" || s.Asleep.By != "phone" {
		t.Fatal("ForViewer must not modify the original")
	}
	if RedactURL("::bad") != "" {
		t.Fatal("unparsable URL must be dropped")
	}
}
