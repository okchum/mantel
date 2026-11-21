// Package plans reads subscription usage windows (Claude Pro/Max, ChatGPT
// Plus/Pro for Codex) and spends an account's saved limit resets.
//
// It never holds the official logins itself: a Backend makes each upstream
// call with the account's credentials. The CLIProxyAPI backend (cpa.go) asks
// CLIProxyAPI to do it, which keeps and refreshes the OAuth tokens.
//
// The upstream endpoints are the ones the Codex and Claude Code clients use.
// They are not public APIs and may change; the request shapes follow
// CLIProxyAPI's management center, which already redeems both kinds of reset.
package plans

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Cred is one login the backend can act as.
type Cred struct {
	ID        string // backend handle (CLIProxyAPI auth_index)
	Name      string // credential name, e.g. "claude-me@example.com.json"
	Provider  string // "claude" or "codex"
	Email     string
	Label     string
	AccountID string // ChatGPT account id (Codex)
	Plan      string // plan hint from the login, when known
	Disabled  bool
}

// Request is an upstream call made with a credential. "$TOKEN$" in a header
// value stands for the credential's access token.
type Request struct {
	Method string
	URL    string
	Header map[string]string
	Body   string
}

type Response struct {
	Status int
	Body   []byte
}

// Backend lists credentials and makes upstream calls as them.
type Backend interface {
	Credentials(ctx context.Context) ([]Cred, error)
	Call(ctx context.Context, c Cred, r Request) (Response, error)
	// ClearCooldown tells the backend the account may be used again right
	// away (CLIProxyAPI parks accounts that hit their limit). Best effort.
	ClearCooldown(ctx context.Context, c Cred) error
}

// Account is one subscription as the dashboard shows it.
type Account struct {
	Key      string    `json:"key"`      // "<provider>:<credential name>", the reset param
	Provider string    `json:"provider"` // claude | codex
	Label    string    `json:"label"`    // "Claude", "Codex"
	Email    string    `json:"email,omitempty"`
	Plan     string    `json:"plan,omitempty"`
	Windows  []Window  `json:"windows"`
	Resets   *Resets   `json:"resets,omitempty"`
	Limited  bool      `json:"limited,omitempty"` // a window is used up
	Updated  time.Time `json:"updated"`
	Error    string    `json:"error,omitempty"`
}

// Window is a rolling usage limit.
type Window struct {
	ID       string     `json:"id"`    // five_hour, seven_day, seven_day_opus, …
	Label    string     `json:"label"` // "5 小时", "本周", …
	Used     float64    `json:"used"`  // percent, 0..100
	ResetsAt *time.Time `json:"resets_at,omitempty"`
}

// Resets are the saved, expiring "reset my limit now" credits.
type Resets struct {
	Available  int        `json:"available"`
	NextExpiry *time.Time `json:"next_expiry,omitempty"`
	// Usable says whether a reset can be spent right now; Blocker says why not.
	Usable  bool   `json:"usable"`
	Blocker string `json:"blocker,omitempty"`
}

// Max is the fullest window's percent.
func (a Account) Max() float64 {
	m := 0.0
	for _, w := range a.Windows {
		m = max(m, w.Used)
	}
	return m
}

var (
	ErrUnknownAccount = errors.New("没有这个订阅账号（列表可能还没刷新）")
	ErrUnsupported    = errors.New("这个账号类型不支持重置")
)

// Service keeps the latest reading per account and runs resets.
type Service struct {
	Backend Backend
	// Only keeps credentials whose name, email or label contains one of these.
	Only []string
	Now  func() time.Time

	mu       sync.Mutex
	last     map[string]Account
	creds    map[string]Cred
	pending  map[string]pending
	resetsMu sync.Mutex // one reset at a time; they are rare and spend credits
}

// pending remembers a reset whose outcome we never learned, so pressing again
// re-sends the same request id and the upstream can't spend a second credit.
type pending struct {
	ID    string
	Grant string
	At    time.Time
}

const retryWindow = 10 * time.Minute

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) wanted(c Cred) bool {
	if c.Disabled || (c.Provider != "claude" && c.Provider != "codex") {
		return false
	}
	if len(s.Only) == 0 {
		return true
	}
	for _, o := range s.Only {
		o = strings.ToLower(o)
		for _, f := range []string{c.Name, c.Email, c.Label} {
			if o != "" && strings.Contains(strings.ToLower(f), o) {
				return true
			}
		}
	}
	return false
}

func keyOf(c Cred) string { return c.Provider + ":" + c.Name }

// Refresh reads every account. A failing account keeps its last reading with
// Error set; only a failing credential list fails the whole refresh.
func (s *Service) Refresh(ctx context.Context) ([]Account, error) {
	creds, err := s.Backend.Credentials(ctx)
	if err != nil {
		return nil, err
	}
	var use []Cred
	for _, c := range creds {
		if s.wanted(c) {
			use = append(use, c)
		}
	}
	out := make([]Account, len(use))
	var wg sync.WaitGroup
	for i, c := range use {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			out[i] = s.read(cctx, c)
		}()
	}
	wg.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		s.last = map[string]Account{}
	}
	s.creds = map[string]Cred{}
	for i, a := range out {
		s.creds[a.Key] = use[i]
		if a.Error != "" {
			if prev, ok := s.last[a.Key]; ok {
				prev.Error = a.Error
				out[i] = prev
				continue
			}
		}
		s.last[a.Key] = a
	}
	slices.SortStableFunc(out, func(a, b Account) int {
		return strings.Compare(a.Provider+a.Label+a.Email, b.Provider+b.Label+b.Email)
	})
	return out, nil
}

func (s *Service) read(ctx context.Context, c Cred) Account {
	var a Account
	var err error
	switch c.Provider {
	case "claude":
		a, err = claudeUsage(ctx, s.Backend, c, s.now())
	case "codex":
		a, err = codexUsage(ctx, s.Backend, c, s.now())
	}
	a.Key, a.Provider = keyOf(c), c.Provider
	a.Email = cmpOr(a.Email, c.Email, c.Label)
	a.Plan = cmpOr(a.Plan, c.Plan)
	if a.Label == "" {
		a.Label = map[string]string{"claude": "Claude", "codex": "Codex"}[c.Provider]
	}
	a.Updated = s.now()
	if err != nil {
		a.Error = err.Error()
	}
	for _, w := range a.Windows {
		if w.Used >= 100 {
			a.Limited = true
		}
	}
	return a
}

// Reset spends one saved reset on the account. The message is for the screen.
func (s *Service) Reset(ctx context.Context, key string) (string, error) {
	s.resetsMu.Lock()
	defer s.resetsMu.Unlock()
	s.mu.Lock()
	c, ok := s.creds[key]
	s.mu.Unlock()
	if !ok {
		return "", ErrUnknownAccount
	}
	var msg string
	var err error
	switch c.Provider {
	case "claude":
		msg, err = s.claudeReset(ctx, key, c)
	case "codex":
		msg, err = s.codexReset(ctx, key, c)
	default:
		return "", ErrUnsupported
	}
	if err == nil {
		if cerr := s.Backend.ClearCooldown(ctx, c); cerr != nil {
			msg += "（代理的冷却没清掉：" + cerr.Error() + "）"
		}
	}
	return msg, err
}

// requestID returns the id to send for this account's reset: the remembered
// one if a previous attempt ended in doubt recently, else a fresh one. A
// doubtful attempt on a different grant still inside the retry window is
// refused: that attempt may have spent its grant (which is why another one
// shows up now), and spending a second one with a new id would cost twice.
func (s *Service) requestID(key, grant string) (id string, retry bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.pending[key]; ok {
		if left := retryWindow - s.now().Sub(p.At); left > 0 {
			if p.Grant == grant {
				return p.ID, true, nil
			}
			return "", false, unconfirmed(left)
		}
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id = hex.EncodeToString(b)
	if s.pending == nil {
		s.pending = map[string]pending{}
	}
	s.pending[key] = pending{ID: id, Grant: grant, At: s.now()}
	return id, false, nil
}

// hasPending reports whether a doubtful reset of the account may still be
// retried with its remembered id.
func (s *Service) hasPending(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[key]
	return ok && s.now().Sub(p.At) < retryWindow
}

// unconfirmed refuses a reset while an earlier one is still in doubt.
func unconfirmed(left time.Duration) error {
	mins := int((left + time.Minute - 1) / time.Minute)
	return fmt.Errorf("上一次重置的结果还没确认，可能已经生效；为了不多花一次，请先刷新看看用量，%d 分钟后再重试", mins)
}

// settle forgets the remembered id once the outcome is known.
func (s *Service) settle(key string) {
	s.mu.Lock()
	delete(s.pending, key)
	s.mu.Unlock()
}

// errDoubt is returned when the request may or may not have reached upstream.
var errDoubt = errors.New("结果不确定，可能已经生效。10 分钟内再按一次会重发同一个请求，不会多花一次")

func httpErr(r Response) error {
	switch r.Status {
	case 401, 403:
		return errors.New("登录失效，请在 CLIProxyAPI 里重新登录这个账号")
	case 429:
		return errors.New("官方限流，稍后再试")
	}
	return fmt.Errorf("官方返回 HTTP %d", r.Status)
}

func cmpOr(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
