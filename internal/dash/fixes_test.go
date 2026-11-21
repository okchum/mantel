package dash

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/state"
)

func cacheDash() *Dash {
	return New(&config.Config{
		Title:   "T",
		Servers: []config.Server{{Name: "nas", Actions: []string{"go"}}},
		Probes:  []config.Probe{{Name: "web"}},
		Actions: []config.Action{{Name: "go", Title: "Go", Confirm: "hold", Type: "demo"}},
		Display: config.Display{Cols: 12, Rows: 8, Home: "p"},
		Pages: []config.Page{{Name: "p", Widgets: []config.Widget{
			{ID: "cpu", Type: "stat", Source: "server:nas", Value: "cpu", At: []int{0, 0, 1, 1}},
			{ID: "list", Type: "servers", At: []int{1, 0, 1, 1}},
			{ID: "probes", Type: "probes", At: []int{2, 0, 1, 1}},
		}}},
	})
}

func TestNormalizedDocsFollowStoreGeneration(t *testing.T) {
	d := cacheDash()
	st := state.NewStore("t", "", []string{"nas"}, []string{"web"}, nil, nil)
	st.Update(func(s *state.Snapshot) { s.Servers[0].CPU = 0.25 })
	val := func() any { return d.Build(st.Read(), "", "", false).Data["cpu"].(map[string]any)["value"] }
	if v := val(); v != 0.25 {
		t.Fatalf("cpu = %v", v)
	}
	if v := val(); v != 0.25 {
		t.Fatalf("cached cpu = %v", v)
	}
	st.Update(func(s *state.Snapshot) { s.Servers[0].CPU = 0.5 })
	if v := val(); v != 0.5 {
		t.Fatalf("after update cpu = %v (stale cache)", v)
	}
	// The servers list adds "actions" to its copy, never to the shared doc.
	r := d.Build(st.Read(), "", "", false)
	row := r.Data["list"].(map[string]any)["servers"].([]any)[0].(map[string]any)
	if row["actions"] == nil {
		t.Fatalf("server row lacks actions: %v", row)
	}
	doc, _ := d.normalize(st.Read(), "server:nas", nil).(map[string]any)
	if _, leaked := doc["actions"]; leaked {
		t.Fatal("actions leaked into the cached document")
	}
}

func TestViewerSnapshotsCachedSeparately(t *testing.T) {
	d := cacheDash()
	st := state.NewStore("t", "", []string{"nas"}, []string{"web"}, nil, nil)
	st.Update(func(s *state.Snapshot) { s.Probes[0].URL = "https://u:p@web.local/x?token=1" })
	url := func(s state.Snapshot) any {
		return d.Build(s, "", "", false).Data["probes"].(map[string]any)["probes"].([]any)[0].(map[string]any)["url"]
	}
	if u := url(st.Read().ForViewer()); u != "https://web.local/x" {
		t.Fatalf("viewer url = %v", u)
	}
	if u := url(st.Read()); u != "https://u:p@web.local/x?token=1" {
		t.Fatalf("operator url = %v", u)
	}
	if u := url(st.Read().ForViewer()); u != "https://web.local/x" {
		t.Fatalf("viewer url after operator = %v", u)
	}
}

func imageDash(url string) *Dash {
	return one(nil, config.Widget{ID: "cam", Type: "image", URL: url})
}

func TestImageRefusesSVG(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
		_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	}))
	defer srv.Close()
	if _, _, err := imageDash(srv.URL).Image(context.Background(), "cam"); err == nil {
		t.Fatal("svg must be refused")
	}
	for ct, want := range map[string]bool{"image/png": true, "image/jpeg; q=1": true, "image/SVG+XML": false, "text/html": false, "": false} {
		if allowedImage(ct) != want {
			t.Errorf("allowedImage(%q) != %v", ct, want)
		}
	}
}

func TestImageCoalescesConcurrentMisses(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("jpeg"))
	}))
	defer srv.Close()
	d := imageDash(srv.URL)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, ct, err := d.Image(context.Background(), "cam")
			if err == nil && (string(b) != "jpeg" || ct != "image/jpeg") {
				t.Errorf("got %q %q", b, ct)
			}
			errs <- err
		}()
	}
	// Let every caller reach the in-flight fetch before answering.
	deadline := time.Now().Add(5 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("upstream hits = %d, want 1", n)
	}
	// A caller that gives up does not cancel the shared fetch.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := d.Image(ctx, "cam"); err != nil {
		t.Fatalf("cached image: %v", err)
	}
}

func TestSampleWithoutChangeKeepsGeneration(t *testing.T) {
	d := one([]config.Source{{Name: "s", Type: "push"}}, config.Widget{Type: "stat", Source: "s", Value: "v", Trend: true})
	st := newStore("s")
	setSrc(st, "s", map[string]any{"v": 1.0}, now, "")
	d.sample(st, now)
	g := st.Generation()
	d.sample(st, now.Add(time.Second))
	if st.Generation() != g {
		t.Fatal("a sample that adds nothing must not bump the store generation")
	}
	if len(st.Read().Hist) != 1 {
		t.Fatalf("hist = %v", st.Read().Hist)
	}
}

func TestImageErrorHidesURL(t *testing.T) {
	// A camera that cannot be reached: the error must not quote its URL,
	// whose query string often carries the camera's token.
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL + "/snap.jpg?token=s3cret"
	srv.Close()
	_, _, err := imageDash(u).Image(context.Background(), "cam")
	if err == nil || strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "snap.jpg") {
		t.Fatalf("err = %v", err)
	}
}
