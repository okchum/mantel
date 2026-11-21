package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveRestoreMatchesByName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	old := time.Now().Add(-30 * time.Hour)
	recent := time.Now().Add(-time.Hour)

	a := NewStore("t", "", []string{"nas", "gone"}, []string{"blog"}, []string{"deepseek"}, nil)
	a.Update(func(s *Snapshot) {
		s.Servers[0].CPUHist = []float64{0.1, 0.2}
		s.Servers[1].CPUHist = []float64{9}
		s.Probes[0].Hist = []float64{12, -1}
		s.Probes[0].Since = recent
		s.Quotas[0].Value = 42
		s.Quotas[0].Series = []Sample{{old, 50}, {recent, 42}}
		s.Weather = &Weather{Name: "here", Temp: 18}
		s.Events = []Event{{Title: "meet"}}
	})
	if err := a.Save(path); err != nil {
		t.Fatal(err)
	}

	// Config changed: "gone" removed, "pi" added, server order swapped.
	b := NewStore("t", "", []string{"pi", "nas"}, []string{"blog"}, []string{"deepseek"}, nil)
	n, err := b.Restore(path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("restored %d slots, want 3", n)
	}
	s := b.Read()
	if s.Servers[0].Name != "pi" || len(s.Servers[0].CPUHist) != 0 {
		t.Fatalf("new slot should stay empty: %+v", s.Servers[0])
	}
	if got := s.Servers[1].CPUHist; len(got) != 2 || got[1] != 0.2 {
		t.Fatalf("nas hist = %v", got)
	}
	if !s.Probes[0].Since.Equal(recent) || len(s.Probes[0].Hist) != 2 {
		t.Fatalf("probe = %+v", s.Probes[0])
	}
	q := s.Quotas[0]
	if q.Value != 42 || len(q.Series) != 1 || q.Series[0].V != 42 {
		t.Fatalf("quota = %+v (series older than 25h must be trimmed)", q)
	}
	if s.Weather == nil || s.Weather.Temp != 18 || len(s.Events) != 1 {
		t.Fatalf("fresh weather/events should restore: %+v %+v", s.Weather, s.Events)
	}

	// Same file but weather/agenda considered stale.
	c := NewStore("t", "", nil, nil, nil, nil)
	if _, err := c.Restore(path, 0); err != nil {
		t.Fatal(err)
	}
	if cs := c.Read(); cs.Weather != nil || len(cs.Events) != 0 {
		t.Fatalf("stale weather/events should not restore: %+v", cs)
	}
}

func TestRestoreMissingAndBad(t *testing.T) {
	dir := t.TempDir()
	st := NewStore("t", "", nil, nil, nil, nil)
	if n, err := st.Restore(filepath.Join(dir, "none.json"), time.Hour); n != 0 || err != nil {
		t.Fatalf("missing file: %d %v", n, err)
	}
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte(`{"version":99}`), 0o600)
	if _, err := st.Restore(bad, time.Hour); err == nil {
		t.Fatal("wrong version should error")
	}
}

func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	st := NewStore("t", "", []string{"a"}, nil, nil, nil)
	for i := 0; i < 3; i++ {
		if err := st.Save(filepath.Join(dir, "state.json")); err != nil {
			t.Fatal(err)
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("dir has %d entries, want just state.json", len(ents))
	}
}
