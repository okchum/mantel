package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/state"
)

// runCollector starts fn in the background and stops it when the test ends.
func runCollector(t *testing.T, fn func(ctx context.Context)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		fn(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func TestRunCommandRefusesOversizeOutput(t *testing.T) {
	ctx := context.Background()
	out, err := RunCommand(ctx, []string{"head", "-c", fmt.Sprint(maxBody), "/dev/zero"}, "", nil, 10*time.Second)
	if err != nil || len(out) != maxBody {
		t.Fatalf("exactly the cap: %d bytes, %v", len(out), err)
	}
	// One byte more used to come back as a cut-off success parsed as {"text": …}.
	_, err = RunCommand(ctx, []string{"head", "-c", fmt.Sprint(maxBody + 1), "/dev/zero"}, "", nil, 10*time.Second)
	if !errors.Is(err, errTooBig) {
		t.Fatalf("want errTooBig, got %v", err)
	}
	if _, err := runExec(ctx, []string{"head", "-c", fmt.Sprint(2 * maxBody), "/dev/zero"}, "", nil, 10*time.Second); !errors.Is(err, errTooBig) {
		t.Fatalf("runExec: want errTooBig, got %v", err)
	}
}

func TestLimitWriter(t *testing.T) {
	var b strings.Builder
	l := &limitWriter{w: &b, n: 4}
	for _, p := range []string{"ab", "cd"} {
		if n, err := l.Write([]byte(p)); n != len(p) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", p, n, err)
		}
	}
	if l.over || b.String() != "abcd" {
		t.Fatalf("at the cap: over=%v %q", l.over, b.String())
	}
	if n, _ := l.Write(nil); n != 0 || l.over {
		t.Fatal("an empty write is not over the cap")
	}
	if n, err := l.Write([]byte("e")); n != 1 || err != nil || !l.over || b.String() != "abcd" {
		t.Fatalf("past the cap: n=%d err=%v over=%v %q", n, err, l.over, b.String())
	}
	l = &limitWriter{w: &b, n: 1}
	_, _ = l.Write([]byte("xy"))
	if !l.over {
		t.Fatal("a write straddling the cap must set over")
	}
}

// probeServer answers each path with a fixed behaviour.
func probeServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = io.WriteString(w, "fine")
		case "/redir":
			http.Redirect(w, r, "/fail", http.StatusFound)
		case "/fail":
			http.Error(w, "down", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckOnce(t *testing.T) {
	srv := probeServer(t)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	client := NewClient(ClientOpts{Timeout: 5 * time.Second, NoKeepAlive: true, NoRedirect: true})
	cases := []struct {
		name, url  string
		expect     int
		wantStatus int
		wantErr    string
	}{
		{"2xx is up", srv.URL + "/ok", 0, 200, ""},
		{"5xx is down", srv.URL + "/fail", 0, 503, "HTTP 503"},
		{"404 is down", srv.URL + "/nope", 0, 404, "HTTP 404"},
		{"redirect is not followed", srv.URL + "/redir", 0, 302, ""},
		{"expect overrides the default", srv.URL + "/redir", 200, 302, "HTTP 302"},
		{"expected error status is up", srv.URL + "/fail", 503, 503, ""},
		{"unreachable", closed.URL, 0, 0, "refused"},
		{"bad url", "http://[::1", 0, 0, "missing ']'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, latency, certEnd, err := checkOnce(context.Background(), client, config.Probe{URL: c.url, Expect: c.expect})
			if status != c.wantStatus {
				t.Errorf("status = %d, want %d", status, c.wantStatus)
			}
			if c.wantErr == "" && err != nil || c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Errorf("err = %v, want %q", err, c.wantErr)
			}
			if c.wantStatus != 0 && latency <= 0 {
				t.Errorf("latency = %v", latency)
			}
			if !certEnd.IsZero() {
				t.Errorf("plain http must not report a certificate: %v", certEnd)
			}
		})
	}
}

func TestCheckOnceCertExpiry(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	want := srv.Certificate().NotAfter

	_, _, certEnd, err := checkOnce(context.Background(), NewClient(ClientOpts{Insecure: true}), config.Probe{URL: srv.URL})
	if err != nil || !certEnd.Equal(want) {
		t.Fatalf("insecure: certEnd = %v, want %v (err %v)", certEnd, want, err)
	}
	// A verified client refuses the self-signed certificate and reports no expiry.
	_, _, certEnd, err = checkOnce(context.Background(), NewClient(ClientOpts{}), config.Probe{URL: srv.URL})
	if err == nil || !certEnd.IsZero() {
		t.Fatalf("verified: certEnd = %v, err = %v", certEnd, err)
	}
}

func TestProbeTransitions(t *testing.T) {
	var down atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "down", http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	st := state.NewStore("t", "", nil, []string{"edge"}, nil, nil)
	p := config.Probe{Name: "edge", URL: srv.URL, Insecure: true, Interval: config.Duration{Duration: time.Hour}}
	runCollector(t, func(ctx context.Context) { Probe(ctx, st, 0, p) })
	read := func() state.Probe { return st.Read().Probes[0] }
	// recheck kicks the probe and waits for a check newer than the last one.
	recheck := func() state.Probe {
		t.Helper()
		last := read().At
		if !Kick("probe:edge") {
			t.Fatal("probe collector not registered")
		}
		waitFor(t, "a new check", func() bool { return read().At.After(last) })
		return read()
	}

	waitFor(t, "first check", func() bool { return read().Checked })
	first := read()
	if !first.Up || first.Status != 200 || first.Error != "" || first.Since.IsZero() || first.Name != "edge" || first.URL != srv.URL {
		t.Fatalf("first check: %+v", first)
	}
	if first.CertExpires == nil || !first.CertExpires.Equal(srv.Certificate().NotAfter) {
		t.Fatalf("cert expiry = %v", first.CertExpires)
	}

	if again := recheck(); !again.Up || !again.Since.Equal(first.Since) || len(again.Hist) != 2 {
		t.Fatalf("staying up must keep Since: %+v", again)
	}

	down.Store(true)
	fail := recheck()
	if fail.Up || fail.Status != 502 || fail.Error != "HTTP 502" || !fail.Since.After(first.Since) || fail.Hist[len(fail.Hist)-1] != -1 {
		t.Fatalf("going down: %+v", fail)
	}
	if still := recheck(); still.Up || !still.Since.Equal(fail.Since) {
		t.Fatalf("staying down must keep Since: %+v", still)
	}

	down.Store(false)
	back := recheck()
	if !back.Up || back.Error != "" || !back.Since.After(fail.Since) || back.LatencyMS <= 0 {
		t.Fatalf("coming back: %+v", back)
	}

	// A check that gets no answer must not keep the old certificate's expiry.
	srv.Close()
	gone := recheck()
	if gone.Up || gone.Status != 0 || gone.Error == "" || gone.CertExpires != nil {
		t.Fatalf("unreachable: %+v", gone)
	}
}

func TestWeather(t *testing.T) {
	const tz = "Pacific/Auckland"
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Skip(err)
	}
	var fail atomic.Bool
	var query atomic.Value
	start := time.Now().In(loc).Truncate(time.Hour).Add(-3 * time.Hour)
	var hours []string
	var temps []float64
	for i := 0; i < 40; i++ {
		hours = append(hours, start.Add(time.Duration(i)*time.Hour).Format("2006-01-02T15:04"))
		temps = append(temps, float64(i))
	}
	doc := map[string]any{
		"current": map[string]any{
			"temperature_2m": 18.5, "apparent_temperature": 17, "weather_code": 3,
			"wind_speed_10m": 12, "relative_humidity_2m": 70, "is_day": 1,
		},
		"hourly": map[string]any{"time": hours, "temperature_2m": temps, "precipitation_probability": []float64{5}},
		"daily": map[string]any{
			"time": []string{"2026-11-21", "2026-11-22"}, "weather_code": []int{1},
			"temperature_2m_max": []float64{20, 22}, "temperature_2m_min": []float64{10, 11},
			"precipitation_probability_max": []float64{30, 40},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query.Store(r.URL.Query())
		if fail.Load() {
			http.Error(w, "busy", http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	defer srv.Close()
	old := openMeteoURL
	openMeteoURL = srv.URL
	t.Cleanup(func() { openMeteoURL = old })

	fail.Store(true)
	st := state.NewStore("t", tz, nil, nil, nil, nil)
	w := config.Weather{Name: "Home", Latitude: -36.85, Longitude: 174.76, Interval: config.Duration{Duration: time.Hour}}
	runCollector(t, func(ctx context.Context) { Weather(ctx, st, w, tz) })

	// A failure before any good reading still shows the name and the error.
	waitFor(t, "first error", func() bool { return st.Read().Weather != nil })
	if got := st.Read().Weather; got.Name != "Home" || !strings.Contains(got.Error, "HTTP 429") || got.Temp != 0 {
		t.Fatalf("first error: %+v", got)
	}
	q := query.Load().(url.Values)
	if q["latitude"][0] != "-36.85" || q["longitude"][0] != "174.76" || q["timezone"][0] != tz || q["forecast_days"][0] != "4" {
		t.Fatalf("query = %v", q)
	}

	fail.Store(false)
	Kick("weather")
	waitFor(t, "a reading", func() bool { return st.Read().Weather.Error == "" })
	got := st.Read().Weather
	if got.Temp != 18.5 || got.Feels != 17 || got.Code != 3 || got.Wind != 12 || got.Humidity != 70 || !got.IsDay || got.Updated.IsZero() {
		t.Fatalf("current: %+v", got)
	}
	// Missing daily values (one weather_code for two days) come out as zero.
	if len(got.Daily) != 2 || got.Daily[0] != (state.WeatherDay{Date: "2026-11-21", Code: 1, Max: 20, Min: 10, Pop: 30}) ||
		got.Daily[1].Code != 0 || got.Daily[1].Max != 22 {
		t.Fatalf("daily: %+v", got.Daily)
	}
	// 24 hours from the current one: the three past hours are skipped.
	if len(got.Hourly) != 24 || got.Hourly[0].Time != hours[3] || got.Hourly[0].Temp != 3 || got.Hourly[23].Temp != 26 || got.Hourly[0].Pop != 0 {
		t.Fatalf("hourly: %d %+v", len(got.Hourly), got.Hourly)
	}

	// A later failure keeps the last good values on screen.
	fail.Store(true)
	Kick("weather")
	waitFor(t, "a later error", func() bool { return st.Read().Weather.Error != "" })
	if got := st.Read().Weather; got.Temp != 18.5 || len(got.Hourly) != 24 {
		t.Fatalf("later error dropped the reading: %+v", got)
	}
}

func TestLocOrLocal(t *testing.T) {
	if locOrLocal("") != time.Local || locOrLocal("Not/AZone") != time.Local {
		t.Fatal("unknown or empty zones fall back to Local")
	}
	if l := locOrLocal("UTC"); l.String() != "UTC" {
		t.Fatalf("got %v", l)
	}
}

// quotaServer serves canned answers per path and counts the calls.
type quotaServer struct {
	*httptest.Server
	hits   map[string]*atomic.Int32
	answer map[string]func(w http.ResponseWriter)
	auth   atomic.Value
}

func newQuotaServer(t *testing.T, answer map[string]func(w http.ResponseWriter)) *quotaServer {
	t.Helper()
	q := &quotaServer{hits: map[string]*atomic.Int32{}, answer: answer}
	for p := range answer {
		q.hits[p] = &atomic.Int32{}
	}
	q.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q.auth.Store(r.Header.Get("Authorization"))
		a, ok := q.answer[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		q.hits[r.URL.Path].Add(1)
		a(w)
	}))
	t.Cleanup(q.Close)
	return q
}

func answerBody(s string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { _, _ = io.WriteString(w, s) }
}

func answerStatus(code int) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { http.Error(w, http.StatusText(code), code) }
}

func TestQuotaProviders(t *testing.T) {
	const credits, key = "/api/v1/credits", "/api/v1/key"
	cases := []struct {
		name, provider string
		answer         map[string]func(http.ResponseWriter)
		want           quotaReading
		wantErr        string
		wantKeyHits    int32 // openrouter: calls to the per-key fallback
	}{
		{name: "deepseek", provider: "deepseek",
			answer: map[string]func(http.ResponseWriter){"/user/balance": answerBody(`{"balance_infos":[{"total_balance":"12.50","currency":"CNY"}]}`)},
			want:   quotaReading{value: 12.5, unit: "¥"}},
		{name: "deepseek usd", provider: "deepseek",
			answer: map[string]func(http.ResponseWriter){"/user/balance": answerBody(`{"balance_infos":[{"total_balance":"3","currency":"USD"}]}`)},
			want:   quotaReading{value: 3, unit: "$"}},
		{name: "deepseek down", provider: "deepseek",
			answer:  map[string]func(http.ResponseWriter){"/user/balance": answerStatus(500)},
			wantErr: "HTTP 500"},
		{name: "deepseek no balance", provider: "deepseek",
			answer:  map[string]func(http.ResponseWriter){"/user/balance": answerBody(`{"balance_infos":[]}`)},
			wantErr: "balance_infos"},
		{name: "moonshot", provider: "moonshot",
			answer: map[string]func(http.ResponseWriter){"/v1/users/me/balance": answerBody(`{"data":{"available_balance":49.5}}`)},
			want:   quotaReading{value: 49.5, unit: "¥"}},
		{name: "moonshot unauthorized", provider: "moonshot",
			answer:  map[string]func(http.ResponseWriter){"/v1/users/me/balance": answerStatus(401)},
			wantErr: "HTTP 401"},
		{name: "siliconflow", provider: "siliconflow",
			answer: map[string]func(http.ResponseWriter){"/v1/user/info": answerBody(`{"data":{"totalBalance":"7.25"}}`)},
			want:   quotaReading{value: 7.25, unit: "¥"}},
		{name: "openrouter credits", provider: "openrouter",
			answer: map[string]func(http.ResponseWriter){
				credits: answerBody(`{"data":{"total_credits":20,"total_usage":5.5}}`),
				key:     answerBody(`{"data":{"limit_remaining":1}}`)},
			want: quotaReading{value: 14.5, limit: 20, unit: "$"}},
		{name: "openrouter credits malformed", provider: "openrouter",
			answer: map[string]func(http.ResponseWriter){
				credits: answerBody(`{"data":{"total_credits":20}}`),
				key:     answerBody(`{"data":{"limit_remaining":1}}`)},
			wantErr: "total_usage"},
		{name: "openrouter 403 falls back to the key limit", provider: "openrouter",
			answer: map[string]func(http.ResponseWriter){
				credits: answerStatus(403),
				key:     answerBody(`{"data":{"limit":10,"limit_remaining":4,"usage":6}}`)},
			want: quotaReading{value: 4, limit: 10, unit: "$"}, wantKeyHits: 1},
		{name: "openrouter 401 falls back to key usage", provider: "openrouter",
			answer: map[string]func(http.ResponseWriter){
				credits: answerStatus(401),
				key:     answerBody(`{"data":{"limit":null,"usage":2.5}}`)},
			want: quotaReading{value: 2.5, unit: "$", mode: "used"}, wantKeyHits: 1},
		{name: "openrouter fallback fails too", provider: "openrouter",
			answer:  map[string]func(http.ResponseWriter){credits: answerStatus(403), key: answerStatus(401)},
			wantErr: "HTTP 401", wantKeyHits: 1},
		// Any other failure is reported: the per-key numbers mean something
		// else and would fire a "low" alert or flip the mode to "used".
		{name: "openrouter 5xx is an error", provider: "openrouter",
			answer: map[string]func(http.ResponseWriter){
				credits: answerStatus(502),
				key:     answerBody(`{"data":{"limit_remaining":1}}`)},
			wantErr: "HTTP 502"},
		{name: "openrouter 429 is an error", provider: "openrouter",
			answer: map[string]func(http.ResponseWriter){
				credits: answerStatus(429),
				key:     answerBody(`{"data":{"usage":1}}`)},
			wantErr: "HTTP 429"},
		{name: "openrouter bad json is an error", provider: "openrouter",
			answer: map[string]func(http.ResponseWriter){
				credits: answerBody(`<html>`),
				key:     answerBody(`{"data":{"usage":1}}`)},
			wantErr: "invalid character"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newQuotaServer(t, c.answer)
			got, err := providers[c.provider](context.Background(), srv.Client(), config.Quota{Provider: c.provider, APIKey: "sk-1", BaseURL: srv.URL + "/"})
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
			} else if err != nil || got != c.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, c.want)
			}
			if h, ok := srv.hits[key]; ok && h.Load() != c.wantKeyHits {
				t.Errorf("/key calls = %d, want %d", h.Load(), c.wantKeyHits)
			}
			if a := srv.auth.Load(); a != "Bearer sk-1" {
				t.Errorf("Authorization = %v", a)
			}
		})
	}
}

func TestQuotaHTTPProvider(t *testing.T) {
	var hdr atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr.Store(r.Header.Clone())
		_, _ = io.WriteString(w, `{"plan":{"left":"80","cap":100},"name":"x"}`)
	}))
	defer srv.Close()
	http1 := providers["http"]
	ctx := context.Background()

	if _, err := http1(ctx, srv.Client(), config.Quota{URL: srv.URL}); err == nil || !strings.Contains(err.Error(), "needs url and value") {
		t.Fatalf("missing value: %v", err)
	}
	got, err := http1(ctx, srv.Client(), config.Quota{
		URL: srv.URL, Value: "plan.left", Limit: "plan.cap", APIKey: "k",
		Headers: map[string]string{"X-Team": "home", "Authorization": "Token override"},
	})
	if err != nil || got != (quotaReading{value: 80, limit: 100}) {
		t.Fatalf("got %+v, %v", got, err)
	}
	h := hdr.Load().(http.Header)
	if h.Get("X-Team") != "home" || h.Get("Authorization") != "Token override" {
		t.Fatalf("headers = %v", h)
	}
	if _, err := http1(ctx, srv.Client(), config.Quota{URL: srv.URL, Value: "plan.left", APIKey: "k"}); err != nil {
		t.Fatal(err)
	} else if hdr.Load().(http.Header).Get("Authorization") != "Bearer k" {
		t.Fatal("api_key must be sent as a bearer token")
	}
	if _, err := http1(ctx, srv.Client(), config.Quota{URL: srv.URL, Value: "name"}); err == nil {
		t.Fatal("a non-number value must be an error")
	}
	if _, err := http1(ctx, srv.Client(), config.Quota{URL: srv.URL + "/\x7f", Value: "x"}); err == nil {
		t.Fatal("a bad url must be an error")
	}
}

func TestQuotaCollector(t *testing.T) {
	var balance atomic.Int64
	var fail atomic.Bool
	balance.Store(50)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "oops", http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintf(w, `{"v":%d}`, balance.Load())
	}))
	defer srv.Close()
	st := state.NewStore("t", "", nil, nil, []string{"bad", "good"}, nil)
	hour := config.Duration{Duration: time.Hour}
	runCollector(t, func(ctx context.Context) {
		Quota(ctx, st, 0, config.Quota{Name: "bad", Provider: "nope", Interval: hour})
	})
	runCollector(t, func(ctx context.Context) {
		Quota(ctx, st, 1, config.Quota{Name: "good", Provider: "http", URL: srv.URL, Value: "v", Unit: "credits", Interval: hour})
	})
	read := func() state.Quota { return st.Read().Quotas[1] }

	waitFor(t, "unknown provider error", func() bool { return st.Read().Quotas[0].Error != "" })
	if q := st.Read().Quotas[0]; q.Name != "bad" || !strings.Contains(q.Error, `unknown provider "nope"`) {
		t.Fatalf("bad: %+v", q)
	}

	waitFor(t, "first reading", func() bool { return !read().Updated.IsZero() })
	if q := read(); q.Name != "good" || q.Value != 50 || q.Unit != "credits" || q.Mode != "remaining" || q.Spent24 != nil || len(q.Series) != 1 {
		t.Fatalf("first: %+v", q)
	}
	balance.Store(42)
	last := read().Updated
	Kick("quota:good")
	waitFor(t, "second reading", func() bool { return read().Updated.After(last) })
	if q := read(); q.Value != 42 || q.Spent24 == nil || *q.Spent24 != 8 || len(q.Hist) != 2 {
		t.Fatalf("second: %+v", q)
	}

	// A failed poll keeps the last reading and reports the error.
	fail.Store(true)
	Kick("quota:good")
	waitFor(t, "error", func() bool { return read().Error != "" })
	if q := read(); q.Value != 42 || !strings.Contains(q.Error, "HTTP 500") || len(q.Series) != 2 {
		t.Fatalf("after error: %+v", q)
	}
}

func TestHTTPStatus(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", &statusError{code: 403, body: "no"})
	if httpStatus(err) != 403 || err.Error() != "wrapped: HTTP 403 no" {
		t.Fatalf("%v → %d", err, httpStatus(err))
	}
	if httpStatus(errors.New("x")) != 0 || httpStatus(nil) != 0 {
		t.Fatal("non-status errors have no code")
	}
}

func TestRunCommandErrors(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		cmd     []string
		timeout time.Duration
		wantErr string
		wantOut string
	}{
		{"ok", []string{"sh", "-c", "echo hi"}, 5 * time.Second, "", "hi\n"},
		{"env and exit", []string{"sh", "-c", `echo "$MANTEL_T"; exit 0`}, 5 * time.Second, "", "v1\n"},
		{"stderr tail", []string{"sh", "-c", "echo partial; echo one >&2; echo two >&2; exit 3"}, 5 * time.Second, "exit status 3: two", "partial\n"},
		{"stdout tail when stderr is empty", []string{"sh", "-c", "echo why; exit 1"}, 5 * time.Second, "exit status 1: why", "why\n"},
		{"bare exit", []string{"false"}, 5 * time.Second, "exit status 1", ""},
		{"timeout", []string{"sleep", "5"}, 50 * time.Millisecond, "timed out after 50ms", ""},
		{"missing binary", []string{"/nonexistent/mantel-cmd"}, 5 * time.Second, "no such file", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := RunCommand(ctx, c.cmd, t.TempDir(), map[string]string{"MANTEL_T": "v1"}, c.timeout)
			if c.wantErr == "" && err != nil || c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("err = %v, want %q", err, c.wantErr)
			}
			if string(out) != c.wantOut {
				t.Fatalf("out = %q, want %q", out, c.wantOut)
			}
		})
	}
}

func TestSourceCollector(t *testing.T) {
	var gotReq atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			http.Error(w, "nope", http.StatusTeapot)
			return
		}
		b, _ := io.ReadAll(r.Body)
		gotReq.Store(r.Method + " " + r.Header.Get("X-Key") + " " + r.Header.Get("Content-Type") + " " + string(b))
		_, _ = io.WriteString(w, `{"a":{"b":7}}`)
	}))
	defer srv.Close()
	names := []string{"web", "web-fail", "cmd", "cmd-select-miss", "push-only"}
	st := state.NewStore("t", "", nil, nil, nil, names)
	hour := config.Duration{Duration: time.Hour}
	srcs := []config.Source{
		{Name: "web", Type: "http_json", URL: srv.URL, Method: "POST", Body: `{"q":1}`, Headers: map[string]string{"X-Key": "k"}, Select: "a.b", Interval: hour},
		{Name: "web-fail", Type: "http_json", URL: srv.URL + "/fail", Interval: hour},
		{Name: "cmd", Type: "exec", Command: []string{"sh", "-c", "echo 41.5"}, Interval: hour},
		{Name: "cmd-select-miss", Type: "exec", Command: []string{"echo", `{"x":1}`}, Select: "y", Interval: hour},
		{Name: "push-only", Type: "push"},
	}
	for _, s := range srcs {
		runCollector(t, func(ctx context.Context) { Source(ctx, st, s) })
	}
	src := func(name string) state.Source {
		s := st.Read()
		return s.Sources[s.SourceIndex(name)]
	}
	done := func(name string) bool { s := src(name); return s.Error != "" || !s.Updated.IsZero() }
	for _, n := range names[:4] {
		waitFor(t, n, func() bool { return done(n) })
	}
	if s := src("web"); s.Data != 7.0 || s.Error != "" {
		t.Errorf("web: %+v", s)
	}
	if r := gotReq.Load(); r != `POST k application/json {"q":1}` {
		t.Errorf("request = %q", r)
	}
	if s := src("web-fail"); !strings.Contains(s.Error, "HTTP 418") || s.Data != nil {
		t.Errorf("web-fail: %+v", s)
	}
	if s := src("cmd"); fmt.Sprint(s.Data) != "map[value:41.5]" || s.Error != "" {
		t.Errorf("cmd: %+v", s)
	}
	if s := src("cmd-select-miss"); !strings.Contains(s.Error, `no "y"`) {
		t.Errorf("cmd-select-miss: %+v", s)
	}
	if s := src("push-only"); s.Error != "" || !s.Updated.IsZero() {
		t.Errorf("push sources are not polled: %+v", s)
	}
}
