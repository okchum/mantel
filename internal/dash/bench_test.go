package dash

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/okchum/mantel/internal/collect"
	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/state"
)

// demoStore fills a store the way demo mode does, then stops the generators.
func demoStore(b *testing.B) (*Dash, *state.Store) {
	b.Helper()
	cfg := &config.Config{Title: "Home", Demo: true, Timezone: "Pacific/Auckland"}
	d := New(cfg)
	d.Backfill = true
	st := state.NewStore("Home", "Pacific/Auckland", nil, nil, nil, config.DemoSources)
	ctx, cancel := context.WithCancel(context.Background())
	go collect.Demo(ctx, st, time.UTC)
	go collect.DemoSources(ctx, st)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := st.Read()
		if len(s.Servers) > 0 && !s.Servers[0].Updated.IsZero() && !s.Sources[0].Updated.IsZero() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	time.Sleep(50 * time.Millisecond)
	d.sample(st, time.Now())
	return d, st
}

// BenchmarkDashboard is one GET /api/v1/dashboard poll: snapshot copy, build
// and JSON encoding. "steady" polls an unchanged store (several screens between
// two collector writes); "changing" writes to the store before every poll.
func BenchmarkDashboard(b *testing.B) {
	d, st := demoStore(b)
	poll := func() {
		r := d.Build(st.Read(), d.Version(), "", true)
		if err := json.NewEncoder(io.Discard).Encode(r); err != nil {
			b.Fatal(err)
		}
	}
	b.Run("steady", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			poll()
		}
	})
	b.Run("changing", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			st.Update(func(s *state.Snapshot) { s.Servers[0].Load1 = float64(i) })
			poll()
		}
	})
	b.Run("page", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			r := d.Build(st.Read(), d.Version(), "main", true)
			if err := json.NewEncoder(io.Discard).Encode(r); err != nil {
				b.Fatal(err)
			}
		}
	})
}
