package plans

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/okchum/mantel/internal/textutil"
)

const (
	codexUsageURL   = "https://chatgpt.com/backend-api/wham/usage"
	codexCreditsURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
	codexConsumeURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"
	codexUA         = "codex-tui/0.149.1 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.149.1)"
)

func codexHeader(c Cred) map[string]string {
	h := map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
		"User-Agent":    codexUA,
	}
	if c.AccountID != "" {
		h["Chatgpt-Account-Id"] = c.AccountID
	}
	return h
}

type codexWindow struct {
	UsedPercent       *float64 `json:"used_percent"`
	ResetAt           any      `json:"reset_at"`
	ResetAfterSeconds *float64 `json:"reset_after_seconds"`
	LimitWindowSecs   *float64 `json:"limit_window_seconds"`
}

type codexUsageDoc struct {
	PlanType  string `json:"plan_type"`
	RateLimit *struct {
		Primary      *codexWindow `json:"primary_window"`
		Secondary    *codexWindow `json:"secondary_window"`
		LimitReached bool         `json:"limit_reached"`
	} `json:"rate_limit"`
	ResetCredits json.RawMessage `json:"rate_limit_reset_credits"`
}

type codexCreditsDoc struct {
	AvailableCount *float64 `json:"available_count"`
	Credits        []struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		ResetType string `json:"reset_type"`
		ExpiresAt any    `json:"expires_at"`
	} `json:"credits"`
}

func codexUsage(ctx context.Context, b Backend, c Cred, now time.Time) (Account, error) {
	a := Account{Label: "Codex"}
	r, err := b.Call(ctx, c, Request{Method: "GET", URL: codexUsageURL, Header: codexHeader(c)})
	if err != nil {
		return a, err
	}
	if r.Status/100 != 2 {
		return a, httpErr(r)
	}
	var doc codexUsageDoc
	if err := json.Unmarshal(r.Body, &doc); err != nil {
		return a, fmt.Errorf("用量数据看不懂: %w", err)
	}
	a.Plan = planName(doc.PlanType)
	if rl := doc.RateLimit; rl != nil {
		for i, w := range []*codexWindow{rl.Primary, rl.Secondary} {
			if w == nil || w.UsedPercent == nil {
				continue
			}
			id, label := codexWindowName(w, i)
			win := Window{ID: id, Label: label, Used: clampPct(*w.UsedPercent)}
			if t := parseTime(w.ResetAt); t != nil {
				win.ResetsAt = t
			} else if w.ResetAfterSeconds != nil {
				t := now.Add(time.Duration(*w.ResetAfterSeconds * float64(time.Second)))
				win.ResetsAt = &t
			}
			a.Windows = append(a.Windows, win)
		}
		a.Limited = rl.LimitReached
	}

	// The dedicated credits endpoint lists expiry; the usage payload only counts.
	hdr := codexHeader(c)
	hdr["Accept"] = "application/json"
	hdr["OpenAI-Beta"] = "codex-1"
	hdr["Originator"] = "Codex Desktop"
	var credits codexCreditsDoc
	got := false
	if cr, err := b.Call(ctx, c, Request{Method: "GET", URL: codexCreditsURL, Header: hdr}); err == nil && cr.Status/100 == 2 {
		got = json.Unmarshal(cr.Body, &credits) == nil
	}
	if !got && len(doc.ResetCredits) > 0 {
		got = json.Unmarshal(doc.ResetCredits, &credits) == nil
	}
	if got {
		a.Resets = codexResets(credits, now)
	}
	return a, nil
}

func codexWindowName(w *codexWindow, i int) (string, string) {
	if w.LimitWindowSecs != nil {
		switch s := *w.LimitWindowSecs; {
		case s == 5*3600:
			return "five_hour", "5 小时"
		case s == 7*24*3600:
			return "seven_day", "本周"
		case s >= 27*24*3600 && s <= 32*24*3600:
			return "month", "本月"
		case s > 0:
			return "window_" + strconv.Itoa(int(s)), humanSpan(time.Duration(s) * time.Second)
		}
	}
	if i == 0 {
		return "five_hour", "5 小时"
	}
	return "seven_day", "本周"
}

func codexResets(d codexCreditsDoc, now time.Time) *Resets {
	res := &Resets{}
	n := 0
	for _, cr := range d.Credits {
		if cr.Status != "available" || (cr.ResetType != "" && cr.ResetType != "codex_rate_limits") {
			continue
		}
		t := parseTime(cr.ExpiresAt)
		if t != nil && !t.After(now) {
			continue
		}
		n++
		if t != nil && (res.NextExpiry == nil || t.Before(*res.NextExpiry)) {
			res.NextExpiry = t
		}
	}
	res.Available = n
	if d.AvailableCount != nil && len(d.Credits) == 0 {
		res.Available = int(*d.AvailableCount)
	}
	res.Usable = res.Available > 0
	if !res.Usable {
		res.Blocker = "没有可用的重置次数"
	}
	return res
}

func (s *Service) codexReset(ctx context.Context, key string, c Cred) (string, error) {
	s.mu.Lock()
	last := s.last[key]
	s.mu.Unlock()
	// After a doubtful attempt the count may already show the reset spent:
	// resend the same id so the answer says whether that attempt took effect.
	if r := last.Resets; r != nil && r.Available == 0 && !s.hasPending(key) {
		return "", errors.New("没有可用的重置次数")
	}
	id, retry, err := s.requestID(key, "codex")
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]string{"redeem_request_id": id})
	r, err := s.Backend.Call(ctx, c, Request{Method: "POST", URL: codexConsumeURL, Header: codexHeader(c), Body: string(body)})
	if err != nil {
		return "", errDoubt
	}
	if r.Status/100 != 2 {
		if r.Status >= 500 {
			return "", errDoubt
		}
		s.settle(key)
		return "", httpErr(r)
	}
	var out struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.Body, &out)
	switch out.Code {
	case "reset":
		s.settle(key)
		return "Codex 用量已重置", nil
	case "already_redeemed":
		s.settle(key)
		if retry {
			return "Codex 用量已重置（上一次的请求其实已经生效）", nil
		}
		return "这次重置已经用过了", nil
	case "nothing_to_reset", "nothingToReset":
		s.settle(key)
		return "", errors.New("现在没有需要重置的用量，没有花掉重置次数")
	case "no_credit", "noCredit":
		s.settle(key)
		return "", errors.New("没有可用的重置次数")
	case "":
		return "", errDoubt
	}
	s.settle(key)
	return "", fmt.Errorf("官方没有确认重置（%s）", out.Code)
}

func planName(p string) string {
	p = strings.TrimSpace(p)
	switch strings.ToLower(p) {
	case "":
		return ""
	case "plus":
		return "Plus"
	case "pro":
		return "Pro"
	case "team":
		return "Team"
	case "max":
		return "Max"
	}
	return textutil.Title(p)
}
