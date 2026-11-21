package collect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/jpath"
	"github.com/okchum/mantel/internal/state"
)

// quotaReading is what one poll of a provider yields.
type quotaReading struct {
	value, limit float64
	unit, mode   string
}

type provider func(ctx context.Context, c *http.Client, q config.Quota) (quotaReading, error)

var providers = map[string]provider{
	"deepseek": func(ctx context.Context, c *http.Client, q config.Quota) (quotaReading, error) {
		var r map[string]any
		if err := getJSON(ctx, c, base(q, "https://api.deepseek.com")+"/user/balance", bearer(q), &r); err != nil {
			return quotaReading{}, err
		}
		v, err := pathNum(r, "balance_infos.0.total_balance")
		cur, _ := pathStr(r, "balance_infos.0.currency")
		return quotaReading{value: v, unit: currencySign(cur)}, err
	},
	"moonshot": func(ctx context.Context, c *http.Client, q config.Quota) (quotaReading, error) {
		var r map[string]any
		if err := getJSON(ctx, c, base(q, "https://api.moonshot.cn")+"/v1/users/me/balance", bearer(q), &r); err != nil {
			return quotaReading{}, err
		}
		v, err := pathNum(r, "data.available_balance")
		return quotaReading{value: v, unit: "¥"}, err
	},
	"siliconflow": func(ctx context.Context, c *http.Client, q config.Quota) (quotaReading, error) {
		var r map[string]any
		if err := getJSON(ctx, c, base(q, "https://api.siliconflow.cn")+"/v1/user/info", bearer(q), &r); err != nil {
			return quotaReading{}, err
		}
		v, err := pathNum(r, "data.totalBalance")
		return quotaReading{value: v, unit: "¥"}, err
	},
	"openrouter": func(ctx context.Context, c *http.Client, q config.Quota) (quotaReading, error) {
		b := base(q, "https://openrouter.ai")
		var r map[string]any
		err := getJSON(ctx, c, b+"/api/v1/credits", bearer(q), &r)
		if err == nil {
			total, err1 := pathNum(r, "data.total_credits")
			used, err2 := pathNum(r, "data.total_usage")
			if err := errors.Join(err1, err2); err != nil {
				return quotaReading{}, err
			}
			return quotaReading{value: total - used, limit: total, unit: "$"}, nil
		}
		// Fall back to the per-key endpoint, which any key may read, only when
		// this key may not read the account credits (401/403). Its numbers mean
		// something else (a per-key limit, or usage), so any other failure, such
		// as a passing outage, is reported instead of switching to them.
		if code := httpStatus(err); code != http.StatusUnauthorized && code != http.StatusForbidden {
			return quotaReading{}, err
		}
		if err := getJSON(ctx, c, b+"/api/v1/key", bearer(q), &r); err != nil {
			return quotaReading{}, err
		}
		if rem, err := pathNum(r, "data.limit_remaining"); err == nil {
			lim, _ := pathNum(r, "data.limit")
			return quotaReading{value: rem, limit: lim, unit: "$"}, nil
		}
		used, err := pathNum(r, "data.usage")
		return quotaReading{value: used, unit: "$", mode: "used"}, err
	},
	"http": func(ctx context.Context, c *http.Client, q config.Quota) (quotaReading, error) {
		if q.URL == "" || q.Value == "" {
			return quotaReading{}, errors.New(`provider "http" needs url and value`)
		}
		h := map[string]string{}
		if q.APIKey != "" {
			h["Authorization"] = "Bearer " + q.APIKey
		}
		for k, v := range q.Headers {
			h[k] = v
		}
		var r any
		if err := getJSON(ctx, c, q.URL, h, &r); err != nil {
			return quotaReading{}, err
		}
		v, err := pathNum(r, q.Value)
		if err != nil {
			return quotaReading{}, err
		}
		out := quotaReading{value: v}
		if q.Limit != "" {
			out.limit, _ = pathNum(r, q.Limit)
		}
		return out, nil
	},
}

func base(q config.Quota, def string) string {
	if q.BaseURL != "" {
		return strings.TrimRight(q.BaseURL, "/")
	}
	return def
}

func bearer(q config.Quota) map[string]string {
	return map[string]string{"Authorization": "Bearer " + q.APIKey}
}

func currencySign(c string) string {
	switch strings.ToUpper(c) {
	case "CNY", "RMB":
		return "¥"
	case "USD":
		return "$"
	case "":
		return ""
	}
	return c + " "
}

func pathNum(v any, path string) (float64, error) { return jpath.Num(v, path) }

func pathStr(v any, path string) (string, error) { return jpath.Str(v, path) }

// Quota polls one AI provider balance into store slot idx. The readings behind
// the 24h spend live in the slot (Series) so they survive a restart via the state file.
func Quota(ctx context.Context, st *state.Store, idx int, q config.Quota) {
	client := NewClient(ClientOpts{Timeout: 15 * time.Second})
	p, ok := providers[q.Provider]
	every(ctx, "quota:"+q.Name, q.Interval.Or(10*time.Minute), func() {
		var r quotaReading
		err := fmt.Errorf("unknown provider %q (have deepseek, moonshot, siliconflow, openrouter, http)", q.Provider)
		if ok {
			r, err = p(ctx, client, q)
		}
		if ctx.Err() != nil {
			return
		}
		st.Update(func(s *state.Snapshot) {
			out := &s.Quotas[idx]
			out.Name = q.Name
			if err != nil {
				out.Error = err.Error()
				return
			}
			now := time.Now()
			out.Series = state.TrimSeries(append(out.Series, state.Sample{At: now, V: r.value}), now.Add(-state.SeriesKeep))
			out.Error, out.Updated = "", now
			out.Value, out.Limit = r.value, r.limit
			out.Unit = r.unit
			if q.Unit != "" {
				out.Unit = q.Unit
			}
			out.Mode = r.mode
			if q.Mode != "" {
				out.Mode = q.Mode
			}
			if out.Mode == "" {
				out.Mode = "remaining"
			}
			out.Hist = state.Push(out.Hist, r.value, histLen)
			out.Spent24 = spentSince(out.Series, now.Add(-24*time.Hour), out.Mode)
		})
	})
}

// spentSince sums consumption over the window, ignoring top-ups (jumps the wrong way).
func spentSince(series []state.Sample, since time.Time, mode string) *float64 {
	if len(series) < 2 {
		return nil
	}
	var spent float64
	for i := 1; i < len(series); i++ {
		if series[i].At.Before(since) {
			continue
		}
		d := series[i-1].V - series[i].V // remaining goes down as you spend
		if mode == "used" {
			d = -d
		}
		if d > 0 {
			spent += d
		}
	}
	return &spent
}
