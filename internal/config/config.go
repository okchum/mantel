// Package config loads the dashboard's JSON configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/okchum/mantel-kit/conf"
)

// Duration is the "30s"-style duration shared with Mantel Cast.
type Duration = conf.Duration

type Config struct {
	Listen    string `json:"listen"`     // e.g. ":8080"
	PublicURL string `json:"public_url"` // URL the Nest Hub loads; empty = http://<LAN IP><listen>/
	Timezone  string `json:"timezone"`   // IANA name; empty = system local
	Title     string `json:"title"`
	TLSCert   string `json:"tls_cert"` // serve HTTPS (needed for a Custom Receiver); PEM paths
	TLSKey    string `json:"tls_key"`
	Demo      bool   `json:"demo"` // fill every panel with generated data
	// Cast is Mantel Cast's section (github.com/okchum/mantel-cast); Mantel
	// ignores it so one config.json can serve both.
	Cast      json.RawMessage `json:"cast,omitempty"`
	Interval  Duration        `json:"interval"` // node_exporter scrape interval
	Servers   []Server        `json:"servers"`
	Calendars []Calendar      `json:"calendars"`
	Weather   *Weather        `json:"weather"`
	Probes    []Probe         `json:"probes"`
	Quotas    []Quota         `json:"quotas"`
	// Tokens gate the page and API. Empty = open (fine on a trusted LAN).
	// Each display gets its own so one can be revoked alone.
	Tokens []Token `json:"tokens"`
	// AllowedHosts are extra Host names (exact, or "*.example.com") accepted
	// without tokens, besides IP literals, localhost, single-label and
	// .local/.lan/.home.arpa/.internal names and public_url's host. Other Hosts
	// are refused in open mode so DNS rebinding cannot reach the API.
	AllowedHosts []string `json:"allowed_hosts"`
	// StateFile keeps histories (traces, 24h spend) across restarts. Empty = off.
	StateFile string  `json:"state_file"`
	Alerts    *Alerts `json:"alerts"`

	// Generic feeds, server-side actions and the screen layout. Without pages the
	// classic single screen (clock, weather, agenda, servers, probes, quotas) is used.
	Sources []Source `json:"sources"`
	Actions []Action `json:"actions"`
	Display Display  `json:"display"`
	Pages   []Page   `json:"pages"`

	// Plans watches Claude and ChatGPT/Codex subscription windows through a
	// CLIProxyAPI and offers their saved resets as a button.
	Plans *Plans `json:"plans"`
}

type Token struct {
	Name  string `json:"name"`  // e.g. "nesthub", "iphone"
	Token string `json:"token"` // "$ENV_NAME" reads the environment
	// Scopes beyond viewing: "act" (run actions) and/or "push" (push data).
	Scopes []string `json:"scopes"`
}

type Alerts struct {
	Bark *Bark `json:"bark"`
	// After is how long something must stay down before it is reported (default 2m),
	// so a single missed scrape or a quick restart stays quiet.
	After Duration `json:"after"`
	// URL opened when the notification is tapped (default public_url).
	URL string `json:"url"`
	// Maintenance lists daily windows ("HH:MM-HH:MM", dashboard time zone)
	// when the network is expected to drop, such as a scheduled router
	// reboot. Servers and probes going down inside one are not reported,
	// and the after delay starts again when the window ends.
	Maintenance []string `json:"maintenance"`
}

// Bark is the iOS push app (https://bark.day.app); self-hosted bark-server works too.
type Bark struct {
	Server string `json:"server"` // default https://api.day.app
	Key    string `json:"key"`    // device key; "$BARK_KEY" reads the environment
	Group  string `json:"group"`  // default title
	Sound  string `json:"sound"`
	Icon   string `json:"icon"`
	Level  string `json:"level"` // level for "down" alerts; default timeSensitive
}

type Server struct {
	Name  string `json:"name"`
	URL   string `json:"url"`   // node_exporter endpoint, e.g. http://10.0.0.5:9100/metrics
	Mount string `json:"mount"` // filesystem to show, default "/"
	Mute  bool   `json:"mute"`  // never alert for this one
	// Actions offered when this server is tapped (action names).
	Actions []string `json:"actions"`
}

type Calendar struct {
	Name  string `json:"name"` // overrides X-WR-CALNAME
	URL   string `json:"url"`  // https:// or webcal:// ICS link
	Color string `json:"color"`
}

type Weather struct {
	Name      string   `json:"name"`
	Latitude  float64  `json:"latitude"`
	Longitude float64  `json:"longitude"`
	Interval  Duration `json:"interval"`
}

type Probe struct {
	Name     string   `json:"name"`
	URL      string   `json:"url"`
	Expect   int      `json:"expect"` // expected status; 0 = any 2xx/3xx
	Interval Duration `json:"interval"`
	Timeout  Duration `json:"timeout"`
	Insecure bool     `json:"insecure"` // skip TLS verification (self-signed LAN services)
	Mute     bool     `json:"mute"`
	Actions  []string `json:"actions"`
}

// Quota describes an AI provider balance/usage source.
// Built-in providers: deepseek, moonshot, siliconflow, openrouter. "http" is generic.
type Quota struct {
	Name     string   `json:"name"`
	Provider string   `json:"provider"`
	APIKey   string   `json:"api_key"` // "$ENV_NAME" reads the environment
	BaseURL  string   `json:"base_url"`
	Interval Duration `json:"interval"`
	// Generic "http" provider fields. Paths are dot paths into the JSON response,
	// e.g. "data.balance" or "balance_infos.0.total_balance".
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Value   string            `json:"value"`
	Limit   string            `json:"limit"`
	Unit    string            `json:"unit"`
	// Mode "remaining" (default): Value is what is left; "used": Value is spent so far.
	Mode string `json:"mode"`
	// AlertBelow pushes an alert once what is left drops under this (same unit as shown).
	AlertBelow float64 `json:"alert_below"`
	// Actions shown on this quota's row, e.g. a "reset" or "top up" action.
	Actions []string `json:"actions"`
}

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, c.normalize()
}

func (c *Config) normalize() error {
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.Title == "" {
		c.Title = "Home"
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return errors.New("tls_cert and tls_key must be set together")
	}
	for i := range c.Servers {
		if c.Servers[i].URL == "" {
			return fmt.Errorf("servers[%d]: url is required", i)
		}
		if c.Servers[i].Mount == "" {
			c.Servers[i].Mount = "/"
		}
		if c.Servers[i].Name == "" {
			c.Servers[i].Name = c.Servers[i].URL
		}
	}
	for i := range c.Calendars {
		if c.Calendars[i].URL == "" {
			return fmt.Errorf("calendars[%d]: url is required", i)
		}
	}
	for i := range c.Probes {
		if c.Probes[i].URL == "" {
			return fmt.Errorf("probes[%d]: url is required", i)
		}
		if c.Probes[i].Name == "" {
			c.Probes[i].Name = c.Probes[i].URL
		}
	}
	for i := range c.Quotas {
		q := &c.Quotas[i]
		if q.Provider == "" {
			return fmt.Errorf("quotas[%d]: provider is required", i)
		}
		if q.Name == "" {
			q.Name = q.Provider
		}
		q.APIKey = conf.ExpandEnv(q.APIKey)
		for k, v := range q.Headers {
			q.Headers[k] = conf.ExpandEnv(v)
		}
	}
	seen := map[string]bool{}
	for i := range c.Tokens {
		t := &c.Tokens[i]
		raw := t.Token
		t.Token = conf.ExpandEnv(t.Token)
		if t.Name == "" {
			t.Name = fmt.Sprintf("token%d", i+1)
		}
		if len(t.Token) < 16 {
			return fmt.Errorf("tokens[%d] (%s): token is empty or shorter than 16 characters (value %q; run `mantel token` for one)", i, t.Name, raw)
		}
		if seen[t.Token] {
			return fmt.Errorf("tokens[%d] (%s): duplicate token", i, t.Name)
		}
		seen[t.Token] = true
	}
	for i, h := range c.AllowedHosts {
		h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
		name := strings.TrimPrefix(h, "*.")
		if name == "" || strings.ContainsAny(name, "/:*@ \t[]") {
			return fmt.Errorf("allowed_hosts[%d]: %q must be a bare host name like \"dash.example.com\" or \"*.example.com\" (no scheme, port or path)", i, c.AllowedHosts[i])
		}
		if name != h && !strings.Contains(name, ".") {
			// "*.com" would let any .com site rebind to this dashboard.
			return fmt.Errorf("allowed_hosts[%d]: %q covers a whole top-level domain; list your own domain, like \"*.example.com\"", i, c.AllowedHosts[i])
		}
		c.AllowedHosts[i] = h
	}
	if a := c.Alerts; a != nil {
		if a.URL == "" {
			a.URL = c.PublicURL
		}
		for i, w := range a.Maintenance {
			start, end, err := conf.ParseWindow(w)
			if err != nil {
				return fmt.Errorf("alerts.maintenance[%d] %q: %v", i, w, err)
			}
			if start == end {
				return fmt.Errorf("alerts.maintenance[%d] %q: empty window", i, w)
			}
		}
		if b := a.Bark; b != nil {
			b.Key = conf.ExpandEnv(b.Key)
			if b.Key == "" {
				return errors.New("alerts.bark.key is empty")
			}
			if b.Server == "" {
				b.Server = "https://api.day.app"
			}
			if b.Group == "" {
				b.Group = c.Title
			}
			if b.Level == "" {
				b.Level = "timeSensitive"
			}
		}
	}
	if c.Plans != nil {
		if err := c.Plans.normalize(); err != nil {
			return err
		}
	}
	return c.normalizeDash()
}
