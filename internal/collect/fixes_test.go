package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/state"
)

// waitFor polls cond for up to 5s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCalendarsKeepRestoredAgendaUntilAFeedWorks(t *testing.T) {
	var mode atomic.Int32 // 0: all feeds fail, 1: only home fails, 2: all fine
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := mode.Load()
		if m == 0 || m == 1 && strings.HasSuffix(r.URL.Path, "home.ics") {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n")
	}))
	defer srv.Close()
	st := state.NewStore("t", "", nil, nil, nil, nil)
	future := time.Now().Add(2 * time.Hour)
	restored := []state.Event{
		{Title: "Dentist", Calendar: "Home", Start: future, End: future.Add(time.Hour)},
		{Title: "Standup", Calendar: "", Start: future, End: future.Add(time.Hour)},
	}
	st.Update(func(s *state.Snapshot) { s.Events = restored })
	cals := []config.Calendar{{Name: "Home", URL: srv.URL + "/home.ics"}, {URL: srv.URL + "/other.ics"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Calendars(ctx, st, cals, time.UTC, time.Hour)

	waitFor(t, "error published", func() bool { return st.Read().EventsErr != "" })
	if evs := st.Read().Events; len(evs) != 2 {
		t.Fatalf("failed boot fetch must keep the restored agenda, got %+v", evs)
	}

	// The unnamed feed now answers (empty); the named one still fails and
	// keeps its restored events.
	mode.Store(1)
	Kick("calendar")
	waitFor(t, "merged agenda", func() bool { return len(st.Read().Events) == 1 })
	s := st.Read()
	if s.Events[0].Title != "Dentist" || !strings.Contains(s.EventsErr, "Home") {
		t.Fatalf("got %+v err %q", s.Events, s.EventsErr)
	}

	// Everything empty: [] not null.
	mode.Store(2)
	Kick("calendar")
	waitFor(t, "empty agenda", func() bool { return len(st.Read().Events) == 0 && st.Read().EventsErr == "" })
	b, _ := json.Marshal(st.Read())
	if !bytes.Contains(b, []byte(`"events":[]`)) {
		t.Fatalf("events must encode as []: %s", b)
	}
}

func TestSeedCalendars(t *testing.T) {
	cals := []config.Calendar{{Name: "A"}, {Name: "B"}, {Name: "B"}, {}}
	got := seedCalendars(cals, []state.Event{{Title: "1", Calendar: "A"}, {Title: "2", Calendar: "B"}, {Title: "3", Calendar: "calendar"}})
	if len(got[0]) != 1 || len(got[1]) != 0 || len(got[2]) != 0 || len(got[3]) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestCapReader(t *testing.T) {
	b, err := io.ReadAll(&capReader{r: strings.NewReader("hello"), left: 5})
	if err != nil || string(b) != "hello" {
		t.Fatalf("%q %v", b, err)
	}
	_, err = io.ReadAll(&capReader{r: strings.NewReader("hello!"), left: 5})
	if !errors.Is(err, errTooBig) {
		t.Fatalf("want errTooBig, got %v", err)
	}
}

func TestNodeExporterKickAndShutdown(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, nodeFixture)
	}))
	defer srv.Close()
	st := state.NewStore("t", "", []string{"kicky"}, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		NodeExporter(ctx, st, 0, config.Server{Name: "kicky", URL: srv.URL}, time.Hour)
		close(done)
	}()
	waitFor(t, "first scrape", func() bool { return st.Read().Servers[0].Up })
	if !Kick("server:kicky") {
		t.Fatal(`Kick("server:kicky") must find the collector`)
	}
	waitFor(t, "kicked scrape", func() bool { return hits.Load() >= 2 })
	cancel()
	<-done
	if s := st.Read().Servers[0]; !s.Up || s.Error != "" {
		t.Fatalf("shutdown must not write an error: %+v", s)
	}
}

func TestNewClient(t *testing.T) {
	if c := NewClient(ClientOpts{Timeout: time.Second}); c.Transport != nil || c.Timeout != time.Second {
		t.Fatalf("plain client must share the default transport: %+v", c)
	}
	c := NewClient(ClientOpts{Insecure: true, NoKeepAlive: true, NoRedirect: true})
	tr, ok := c.Transport.(*http.Transport)
	if !ok || !tr.TLSClientConfig.InsecureSkipVerify || !tr.DisableKeepAlives || tr.IdleConnTimeout == 0 || c.CheckRedirect == nil {
		t.Fatalf("%+v", c)
	}
}

func TestFetchHTTPRefusesOversizeBody(t *testing.T) {
	// A JSON body over maxBody used to be cut off and stored as {"text": …}.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"v":"`+strings.Repeat("x", maxBody)+`"}`)
	}))
	defer srv.Close()
	_, err := fetchHTTP(context.Background(), srv.Client(), config.Source{Name: "big", URL: srv.URL})
	if !errors.Is(err, errTooBig) {
		t.Fatalf("want errTooBig, got %v", err)
	}
	if b, err := get(context.Background(), srv.Client(), srv.URL, "*/*", nil); !errors.Is(err, errTooBig) || b != nil {
		t.Fatalf("get: want errTooBig, got %d bytes, %v", len(b), err)
	}
}
