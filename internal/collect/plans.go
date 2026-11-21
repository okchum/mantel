package collect

import (
	"context"
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/jpath"
	"github.com/okchum/mantel/internal/plans"
	"github.com/okchum/mantel/internal/state"
)

// Plans reads the subscription accounts into the "plans" source. A reset
// action kicks it ("plans") so the screen shows the new numbers right away.
func Plans(ctx context.Context, st *state.Store, svc *plans.Service, interval time.Duration) {
	every(ctx, config.PlanSource, interval, func() {
		rctx, cancel := context.WithTimeout(ctx, time.Minute)
		accts, err := svc.Refresh(rctx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		st.Update(func(s *state.Snapshot) {
			i := s.SourceIndex(config.PlanSource)
			if i < 0 {
				return
			}
			out := &s.Sources[i]
			if err != nil {
				out.Error = shortErr(err).Error()
				return
			}
			if accts == nil {
				accts = []plans.Account{}
			}
			out.Data, out.Error, out.Updated = jpath.Normalize(map[string]any{"accounts": accts}), "", time.Now()
		})
	})
}

// demoPlan is a fake subscription for demo mode.
type demoPlan struct {
	acct  plans.Account
	rates []float64 // percent per minute per window
}

func demoPlans(now time.Time) []*demoPlan {
	at := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	return []*demoPlan{
		{acct: plans.Account{
			Key: "claude:claude-me@example.com.json", Provider: "claude", Label: "Claude", Email: "me@example.com", Plan: "Max 5x",
			Windows: []plans.Window{
				{ID: "five_hour", Label: "5 小时", Used: 86, ResetsAt: at(97 * time.Minute)},
				{ID: "seven_day", Label: "本周", Used: 64, ResetsAt: at(3*24*time.Hour + 5*time.Hour)},
				{ID: "seven_day_opus", Label: "Opus 本周", Used: 41, ResetsAt: at(3*24*time.Hour + 5*time.Hour)},
			},
			Resets: &plans.Resets{Available: 2, NextExpiry: at(5 * 24 * time.Hour), Blocker: "还没到限额，到了才能重置"},
		}, rates: []float64{1.5, .08, .05}},
		{acct: plans.Account{
			Key: "codex:codex-me@example.com.json", Provider: "codex", Label: "Codex", Email: "me@example.com", Plan: "Pro",
			Windows: []plans.Window{
				{ID: "five_hour", Label: "5 小时", Used: 34, ResetsAt: at(3*time.Hour + 12*time.Minute)},
				{ID: "seven_day", Label: "本周", Used: 91, ResetsAt: at(26 * time.Hour)},
			},
			Resets: &plans.Resets{Available: 1, NextExpiry: at(20 * time.Hour), Usable: true},
		}, rates: []float64{.6, .05}},
		{acct: plans.Account{
			Key: "claude:claude-work@example.com.json", Provider: "claude", Label: "Claude", Email: "work@example.com", Plan: "Pro",
			Windows: []plans.Window{
				{ID: "five_hour", Label: "5 小时", Used: 12, ResetsAt: at(4 * time.Hour)},
				{ID: "seven_day", Label: "本周", Used: 27, ResetsAt: at(6 * 24 * time.Hour)},
			},
			Resets: &plans.Resets{Blocker: "没有可用的重置次数"},
		}, rates: []float64{.2, .02}},
		{acct: plans.Account{
			Key: "codex:codex-work@example.com.json", Provider: "codex", Label: "Codex", Email: "work@example.com", Plan: "Plus",
			Windows: []plans.Window{
				{ID: "five_hour", Label: "5 小时", Used: 47, ResetsAt: at(2*time.Hour + 40*time.Minute)},
				{ID: "seven_day", Label: "本周", Used: 18, ResetsAt: at(4*24*time.Hour + 9*time.Hour)},
			},
		}, rates: []float64{.4, .03}},
	}
}

// step advances the fake usage by dt and keeps resets' state consistent.
func (p *demoPlan) step(now time.Time, dt time.Duration) plans.Account {
	a := &p.acct
	a.Limited = false
	for i := range a.Windows {
		w := &a.Windows[i]
		w.Used = min(100, w.Used+p.rates[i]*dt.Minutes())
		if w.ResetsAt != nil && now.After(*w.ResetsAt) {
			w.Used = 0
			t := now.Add(5 * time.Hour)
			if w.ID != "five_hour" {
				t = now.Add(7 * 24 * time.Hour)
			}
			w.ResetsAt = &t
		}
		if w.Used >= 100 {
			a.Limited = true
		}
	}
	if r := a.Resets; r != nil {
		switch {
		case r.Available == 0:
			r.Usable, r.Blocker = false, "没有可用的重置次数"
		case a.Provider == "claude" && !a.Limited:
			r.Usable, r.Blocker = false, "还没到限额，到了才能重置"
		default:
			r.Usable, r.Blocker = true, ""
		}
	}
	a.Updated = now
	out := *a
	out.Windows = append([]plans.Window(nil), a.Windows...)
	if a.Resets != nil {
		r := *a.Resets
		out.Resets = &r
	}
	return out
}

// reset spends a fake reset: every window back to zero.
func (p *demoPlan) reset() (string, error) {
	a := &p.acct
	if a.Resets == nil || !a.Resets.Usable {
		why := "现在不能重置"
		if a.Resets != nil && a.Resets.Blocker != "" {
			why = a.Resets.Blocker
		}
		return "", errorString(why)
	}
	for i := range a.Windows {
		a.Windows[i].Used = 0
	}
	a.Resets.Available--
	return a.Label + " 用量已重置（演示）", nil
}

type errorString string

func (e errorString) Error() string { return string(e) }
