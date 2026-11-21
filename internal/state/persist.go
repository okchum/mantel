package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// fileVersion guards against reading a file written by an incompatible build.
const fileVersion = 1

type savedQuota struct {
	Quota
	Series []Sample `json:"series"`
}

type savedFile struct {
	Version   int                     `json:"version"`
	Saved     time.Time               `json:"saved"`
	Servers   []Server                `json:"servers"`
	Probes    []Probe                 `json:"probes"`
	Quotas    []savedQuota            `json:"quotas"`
	Weather   *Weather                `json:"weather,omitempty"`
	Events    []Event                 `json:"events,omitempty"`
	EventsErr string                  `json:"events_error,omitempty"`
	Sources   []Source                `json:"sources,omitempty"`
	Hist      map[string][]Sample     `json:"hist,omitempty"`
	Actions   map[string]ActionResult `json:"actions,omitempty"`
	Asleep    *Asleep                 `json:"asleep,omitempty"`
}

// Save writes the snapshot atomically (temp file + rename), so a crash mid-write
// leaves the previous file intact.
func (st *Store) Save(path string) error {
	_, err := st.save(path)
	return err
}

// save writes a copy of the snapshot and returns the generation it reflects.
// Encoding happens on the copy, outside the store lock, so a multi-megabyte
// state does not stall collectors and dashboard polls.
func (st *Store) save(path string) (uint64, error) {
	s, gen := st.ReadGen()
	f := savedFile{
		Version:   fileVersion,
		Saved:     time.Now(),
		Servers:   s.Servers,
		Probes:    s.Probes,
		Weather:   s.Weather,
		Events:    s.Events,
		EventsErr: s.EventsErr,
		Sources:   s.Sources,
		Hist:      s.Hist,
		Actions:   s.Actions,
		Asleep:    s.Asleep,
	}
	for _, q := range s.Quotas {
		f.Quotas = append(f.Quotas, savedQuota{Quota: q, Series: q.Series})
	}
	b, err := json.Marshal(f)
	if err != nil {
		return 0, err
	}
	return gen, writeAtomic(path, b)
}

// writeAtomic replaces path with b via a synced temp file, a rename and a
// best-effort sync of the directory so the rename itself survives a power cut.
func writeAtomic(path string, b []byte) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	renamed := false
	defer func() {
		if renamed {
			return
		}
		if rerr := os.Remove(tmp.Name()); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			err = errors.Join(err, rerr)
		}
	}()
	if _, err := tmp.Write(b); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	renamed = true
	syncDir(dir)
	return nil
}

// syncDir flushes a directory entry; some platforms (Windows) cannot, which is fine.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// Restore loads a file written by Save into the matching named slots. Entries
// whose names are no longer configured are dropped; new ones start empty.
// Weather and agenda are restored only if the file is fresher than staleAfter,
// since those collectors refresh within seconds anyway and old ones would mislead.
// A missing file is not an error; it returns (0, nil).
func (st *Store) Restore(path string, staleAfter time.Duration) (int, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var f savedFile
	if err := json.Unmarshal(b, &f); err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	if f.Version != fileVersion {
		return 0, fmt.Errorf("%s: version %d, want %d", path, f.Version, fileVersion)
	}
	now := time.Now()
	n := 0
	st.Update(func(s *Snapshot) {
		n += restoreSame(s.Servers, f.Servers, func(x Server) string { return x.Name })
		n += restoreSame(s.Probes, f.Probes, func(x Probe) string { return x.Name })
		n += restoreByName(s.Quotas, f.Quotas, func(x Quota) string { return x.Name },
			func(o savedQuota) string { return o.Name },
			func(o savedQuota) Quota {
				q := o.Quota
				q.Series = TrimSeries(o.Series, now.Add(-SeriesKeep))
				return q
			})
		n += restoreSame(s.Sources, f.Sources, func(x Source) string { return x.Name })
		for k, v := range f.Hist {
			s.Hist[k] = v
		}
		for k, v := range f.Actions {
			v.Running = false
			s.Actions[k] = v
		}
		s.Asleep = f.Asleep // the keeper ignores reports that are too old
		if now.Sub(f.Saved) < staleAfter {
			if f.Weather != nil {
				s.Weather = f.Weather
			}
			if f.Events != nil {
				s.Events, s.EventsErr = f.Events, f.EventsErr
			}
		}
	})
	return n, nil
}

// SeriesKeep is how much quota history is kept: a little over the 24h window.
const SeriesKeep = 25 * time.Hour

// TrimSeries drops samples older than since.
func TrimSeries(s []Sample, since time.Time) []Sample {
	i := 0
	for i < len(s) && s[i].At.Before(since) {
		i++
	}
	return s[i:]
}

// Autosave saves every interval and once more when ctx ends, then closes done.
// A save is skipped when nothing was updated since the last one; the state
// as restored at startup counts as saved.
func (st *Store) Autosave(ctx context.Context, path string, every time.Duration, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(every)
	defer t.Stop()
	saved := st.Generation()
	save := func(final bool) {
		if st.Generation() == saved {
			return
		}
		gen, err := st.save(path)
		switch {
		case err != nil && final:
			log.Printf("state: final save: %v", err)
		case err != nil:
			log.Printf("state: save: %v", err)
		default:
			saved = gen
			if final {
				log.Printf("state: saved to %s", path)
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			save(true)
			return
		case <-t.C:
			save(false)
		}
	}
}

// restoreByName replaces each cur entry with conv of the saved entry of the
// same non-empty name, and returns how many it replaced.
func restoreByName[C, S any](cur []C, saved []S, curName func(C) string, savedName func(S) string, conv func(S) C) int {
	n := 0
	for i := range cur {
		for _, o := range saved {
			if k := savedName(o); k != "" && k == curName(cur[i]) {
				cur[i] = conv(o)
				n++
				break
			}
		}
	}
	return n
}

// restoreSame is restoreByName for entries saved as they are.
func restoreSame[T any](cur, saved []T, name func(T) string) int {
	return restoreByName(cur, saved, name, name, func(o T) T { return o })
}
