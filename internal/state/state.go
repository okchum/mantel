// Package state holds the latest snapshot every collector writes into and the
// HTTP handler serves to the dashboard page.
package state

import (
	"net/url"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

type Server struct {
	Name     string    `json:"name"`
	Up       bool      `json:"up"`
	Error    string    `json:"error,omitempty"`
	CPU      float64   `json:"cpu"`  // 0..1
	Mem      float64   `json:"mem"`  // 0..1
	Disk     float64   `json:"disk"` // 0..1
	MemTotal float64   `json:"mem_total"`
	Load1    float64   `json:"load1"`
	Cores    int       `json:"cores"`
	NetRx    float64   `json:"net_rx"` // bytes/s
	NetTx    float64   `json:"net_tx"`
	Uptime   float64   `json:"uptime"` // seconds
	Temp     *float64  `json:"temp,omitempty"`
	CPUHist  []float64 `json:"cpu_hist"`
	NetHist  []float64 `json:"net_hist"` // rx+tx bytes/s
	Updated  time.Time `json:"updated"`
}

type Probe struct {
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Up        bool      `json:"up"`
	Checked   bool      `json:"checked"`
	Status    int       `json:"status"`
	LatencyMS float64   `json:"latency_ms"`
	Hist      []float64 `json:"hist"` // latency ms; -1 = failed check
	Since     time.Time `json:"since"`
	At        time.Time `json:"at"` // last check
	Error     string    `json:"error,omitempty"`
	// CertExpires is the HTTPS leaf certificate's NotAfter, when there is one.
	CertExpires *time.Time `json:"cert_expires,omitempty"`
}

type Weather struct {
	Name     string        `json:"name"`
	Temp     float64       `json:"temp"`
	Feels    float64       `json:"feels"`
	Code     int           `json:"code"`
	Wind     float64       `json:"wind"`
	Humidity float64       `json:"humidity"`
	IsDay    bool          `json:"is_day"`
	Daily    []WeatherDay  `json:"daily"`
	Hourly   []WeatherHour `json:"hourly"`
	Updated  time.Time     `json:"updated"`
	Error    string        `json:"error,omitempty"`
}

type WeatherDay struct {
	Date string  `json:"date"`
	Code int     `json:"code"`
	Max  float64 `json:"max"`
	Min  float64 `json:"min"`
	Pop  float64 `json:"pop"`
}

type WeatherHour struct {
	Time string  `json:"time"`
	Temp float64 `json:"temp"`
	Pop  float64 `json:"pop"`
}

type Event struct {
	Title    string    `json:"title"`
	Location string    `json:"location,omitempty"`
	Calendar string    `json:"calendar,omitempty"`
	Color    string    `json:"color,omitempty"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	AllDay   bool      `json:"all_day"`
}

type Quota struct {
	Name    string    `json:"name"`
	Value   float64   `json:"value"`           // remaining, or used when Mode == "used"
	Limit   float64   `json:"limit,omitempty"` // 0 = unknown
	Unit    string    `json:"unit"`
	Mode    string    `json:"mode"`
	Hist    []float64 `json:"hist"`
	Spent24 *float64  `json:"spent_24h,omitempty"` // derived from history
	Updated time.Time `json:"updated"`
	Error   string    `json:"error,omitempty"`
	// Series is ~25h of timed readings behind Spent24; persisted, not served.
	Series []Sample `json:"-"`
}

type Sample struct {
	At time.Time `json:"at"`
	V  float64   `json:"v"`
}

type Snapshot struct {
	Title     string    `json:"title"`
	Timezone  string    `json:"timezone"`
	Now       time.Time `json:"now"`
	Servers   []Server  `json:"servers"`
	Probes    []Probe   `json:"probes"`
	Weather   *Weather  `json:"weather"`
	Events    []Event   `json:"events"`
	EventsErr string    `json:"events_error,omitempty"`
	Quotas    []Quota   `json:"quotas"`
	// Sources are the generic data feeds (http_json, exec, push), one slot each.
	Sources []Source `json:"sources"`
	// Hist is the history the dashboard keeps for numbers it charts,
	// keyed "source|path" (see dash.HistKey).
	Hist map[string][]Sample `json:"-"`
	// Actions holds the latest result of each action, keyed by action name.
	Actions map[string]ActionResult `json:"actions,omitempty"`
	// Asleep is the last sleep report from a phone (POST /api/v1/quiet).
	// Replaced, never mutated, so sharing the pointer is safe.
	Asleep *Asleep `json:"asleep,omitempty"`

	// Gen is the Store generation this copy was read at (0: not from a store).
	Gen uint64 `json:"-"`
	// Redacted marks a copy prepared by ForViewer.
	Redacted bool `json:"-"`
}

// ForViewer returns a copy without what a view-only caller should not see:
// who ran actions or reported sleep (token names), action output (it may echo
// command output or upstream errors; OK and the time stay) and credentials or
// query strings in probe URLs.
func (s Snapshot) ForViewer() Snapshot {
	acts := make(map[string]ActionResult, len(s.Actions))
	for k, v := range s.Actions {
		v.By = ""
		v.Message = ""
		acts[k] = v
	}
	s.Actions = acts
	probes := make([]Probe, len(s.Probes))
	for i, p := range s.Probes {
		p.URL = RedactURL(p.URL)
		probes[i] = p
	}
	s.Probes = probes
	if s.Asleep != nil {
		a := *s.Asleep
		a.By = ""
		s.Asleep = &a
	}
	s.Redacted = true
	return s
}

// RedactURL drops userinfo, query and fragment, which may carry credentials.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User, u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = nil, "", false, "", ""
	return u.String()
}

// Asleep is a phone saying its owner went to sleep (On) or woke up.
type Asleep struct {
	On bool      `json:"on"`
	At time.Time `json:"at"`
	By string    `json:"by,omitempty"`
}

// Source is one generic data feed. Data is decoded JSON and is replaced,
// never mutated in place, so a shallow copy is safe to hand out.
type Source struct {
	Name    string    `json:"name"`
	Data    any       `json:"data"`
	Updated time.Time `json:"updated"`
	Error   string    `json:"error,omitempty"`
}

// ActionResult is what the last run of an action did.
type ActionResult struct {
	At      time.Time `json:"at"`
	OK      bool      `json:"ok"`
	Running bool      `json:"running,omitempty"`
	Message string    `json:"message,omitempty"`
	By      string    `json:"by,omitempty"` // token name
	TookMS  int64     `json:"took_ms"`
}

// Store is a mutex-guarded Snapshot. Collectors own fixed slots (index into a
// slice) so they never contend on structure, only on the lock.
type Store struct {
	mu  sync.RWMutex
	s   Snapshot
	gen atomic.Uint64 // bumped by every Update
}

// NewStore creates one named slot per configured server, probe and quota.
// Names are known up front so saved history can be matched back to them.
func NewStore(title, tz string, servers, probes, quotas, sources []string) *Store {
	s := Snapshot{
		Title:    title,
		Timezone: tz,
		Servers:  make([]Server, len(servers)),
		Probes:   make([]Probe, len(probes)),
		Quotas:   make([]Quota, len(quotas)),
		Events:   []Event{},
		Sources:  make([]Source, len(sources)),
		Hist:     map[string][]Sample{},
		Actions:  map[string]ActionResult{},
	}
	for i, n := range sources {
		s.Sources[i].Name = n
	}
	for i, n := range servers {
		s.Servers[i].Name = n
	}
	for i, n := range probes {
		s.Probes[i].Name = n
	}
	for i, n := range quotas {
		s.Quotas[i].Name = n
	}
	return &Store{s: s}
}

// Update runs fn with exclusive access to the snapshot.
func (st *Store) Update(fn func(s *Snapshot)) {
	st.mu.Lock()
	defer st.mu.Unlock()
	fn(&st.s)
	st.gen.Add(1)
}

// Generation counts Updates; an unchanged value means unchanged data.
func (st *Store) Generation() uint64 { return st.gen.Load() }

// Read returns a deep-enough copy for JSON encoding.
func (st *Store) Read() Snapshot {
	s, _ := st.ReadGen()
	return s
}

// ReadGen is Read plus the Generation the copy was taken at.
func (st *Store) ReadGen() (Snapshot, uint64) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	gen := st.gen.Load()
	s := st.s
	s.Servers = slices.Clone(st.s.Servers) // Clone keeps empty slices non-nil ([] not null in JSON)
	s.Probes = slices.Clone(st.s.Probes)
	s.Quotas = slices.Clone(st.s.Quotas)
	s.Events = slices.Clone(st.s.Events)
	s.Sources = slices.Clone(st.s.Sources)
	s.Hist = make(map[string][]Sample, len(st.s.Hist))
	for k, v := range st.s.Hist {
		s.Hist[k] = v[:len(v):len(v)]
	}
	s.Actions = make(map[string]ActionResult, len(st.s.Actions))
	for k, v := range st.s.Actions {
		s.Actions[k] = v
	}
	if st.s.Weather != nil {
		w := *st.s.Weather
		s.Weather = &w
	}
	s.Now = time.Now()
	s.Gen = gen
	return s, gen
}

// SourceIndex returns the slot of the named source, or -1.
func (s *Snapshot) SourceIndex(name string) int {
	for i := range s.Sources {
		if s.Sources[i].Name == name {
			return i
		}
	}
	return -1
}

// Push appends v to a fixed-size history, dropping the oldest values.
func Push(h []float64, v float64, max int) []float64 {
	h = append(h, v)
	if len(h) > max {
		h = append([]float64(nil), h[len(h)-max:]...)
	}
	return h
}
