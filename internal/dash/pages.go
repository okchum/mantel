package dash

import (
	"time"

	"github.com/okchum/mantel/internal/config"
)

// ClassicPages is the original single screen, used when no pages are configured:
// clock, weather and agenda on the left; servers, probes and quotas on the right.
func ClassicPages(cfg *config.Config) []config.Page {
	left := []config.Widget{{ID: "clock", Type: "clock", At: []int{0, 0, 4, 2}}}
	agendaTop := 2
	if cfg.Weather != nil {
		left = append(left, config.Widget{ID: "weather", Type: "weather", At: []int{0, 2, 4, 2}})
		agendaTop = 4
	}
	left = append(left, config.Widget{ID: "agenda", Type: "agenda", Title: "日程", At: []int{0, agendaTop, 4, 8 - agendaTop}})

	var right []config.Widget
	lower := len(cfg.Probes) > 0 || len(cfg.Quotas) > 0
	srvRows := 8
	if lower {
		srvRows = 5
	}
	if len(cfg.Servers) > 0 || !lower {
		right = append(right, config.Widget{ID: "servers", Type: "servers", At: []int{4, 0, 8, srvRows}})
	} else {
		srvRows = 0
	}
	switch {
	case len(cfg.Probes) > 0 && len(cfg.Quotas) > 0:
		right = append(right,
			config.Widget{ID: "probes", Type: "probes", Title: "服务", At: []int{4, srvRows, 4, 8 - srvRows}},
			config.Widget{ID: "quotas", Type: "quotas", Title: "AI 额度", At: []int{8, srvRows, 4, 8 - srvRows}})
	case len(cfg.Probes) > 0:
		right = append(right, config.Widget{ID: "probes", Type: "probes", Title: "服务", At: []int{4, srvRows, 8, 8 - srvRows}})
	case len(cfg.Quotas) > 0:
		right = append(right, config.Widget{ID: "quotas", Type: "quotas", Title: "AI 额度", At: []int{4, srvRows, 8, 8 - srvRows}})
	}
	return []config.Page{{Name: "main", Title: "总览", Widgets: append(left, right...)}}
}

func f(v float64) *float64 { return &v }
func n(v int) *int         { return &v }
func dur(d time.Duration) config.Duration {
	return config.Duration{Duration: d}
}

// DemoPages shows off every widget type over the demo sources.
func DemoPages() []config.Page {
	return []config.Page{
		{Name: "main", Title: "总览", Widgets: []config.Widget{
			{ID: "clock", Type: "clock", At: []int{0, 0, 4, 2}},
			{ID: "weather", Type: "weather", At: []int{0, 2, 4, 2}},
			{ID: "agenda", Type: "agenda", Title: "日程", At: []int{0, 4, 4, 4}},
			{ID: "servers", Type: "servers", At: []int{4, 0, 8, 5}},
			{ID: "probes", Type: "probes", Title: "服务", At: []int{4, 5, 4, 3}},
			{ID: "quotas", Type: "quotas", Title: "AI 额度", At: []int{8, 5, 4, 3}},
		}},
		{Name: "home", Title: "家里", Widgets: []config.Widget{
			{ID: "home.power", Type: "stat", Title: "当前功率", Source: "power", Value: "watts", Unit: "W",
				Format: "int", Trend: true, Window: dur(30 * time.Minute), Warn: f(2500), Crit: f(3500), At: []int{0, 0, 3, 2}},
			{ID: "home.today", Type: "stat", Title: "今日用电", Source: "power", Value: "today_kwh", Unit: "kWh",
				Decimals: n(1), At: []int{3, 0, 3, 2}},
			{ID: "home.indoor", Type: "gauge", Title: "室内湿度", Source: "climate", Value: "humidity",
				MaxValue: f(100), Unit: "%", Format: "int", Warn: f(65), Crit: f(75), At: []int{6, 0, 3, 2}},
			{ID: "home.ups", Type: "gauge", Title: "UPS 电量", Source: "power", Value: "ups", MaxValue: f(100), Unit: "%",
				Invert: true, Warn: f(50), Crit: f(20), At: []int{9, 0, 3, 2}},
			{ID: "home.chart", Type: "timeseries", Title: "温度", Source: "climate", Unit: "°", Window: dur(30 * time.Minute),
				Series: []config.Series{
					{Label: "客厅", Value: "living", Color: "sodium"},
					{Label: "卧室", Value: "bedroom", Color: "seaglass"},
					{Label: "室外", Value: "outdoor", Color: "sky"},
				}, At: []int{0, 2, 6, 3}},
			{ID: "home.containers", Type: "status", Title: "容器", Source: "containers", Detail: "image",
				ItemActions: []string{"demo-restart"}, At: []int{6, 2, 3, 3}},
			{ID: "home.backup", Type: "list", Title: "备份", Source: "backup", Limit: 4, At: []int{9, 2, 3, 3}},
			{ID: "home.downloads", Type: "progress", Title: "下载", Source: "downloads", Limit: 4,
				ItemActions: []string{"demo-pause"}, At: []int{0, 5, 5, 3}},
			{ID: "home.trip", Type: "countdown", Title: "东京旅行", To: time.Now().AddDate(0, 0, 3).Format("2006-01-02") + "T09:15", At: []int{5, 5, 3, 3}},
			{ID: "home.scenes", Type: "buttons", Title: "快捷", Actions: []string{"demo-lights", "demo-fan", "demo-gate"}, At: []int{8, 5, 4, 3}},
		}},
		{Name: "ai", Title: "AI", Widgets: []config.Widget{
			{ID: "ai.plans", Type: "plans", Title: "订阅用量", At: []int{0, 0, 7, 8}},
			{ID: "ai.quotas", Type: "quotas", Title: "API 余额", At: []int{7, 0, 5, 3}},
			{ID: "ai.usage", Type: "bars", Title: "本周 API 花费", Source: "ai_usage", Unit: "¥", Decimals: n(1), At: []int{7, 3, 5, 5}},
		}},
		{Name: "work", Title: "工作", Widgets: []config.Widget{
			{ID: "work.orders", Type: "stat", Title: "今日订单", Source: "orders", Value: "today", Format: "int",
				Trend: true, Window: dur(30 * time.Minute), At: []int{0, 0, 3, 2}},
			{ID: "work.revenue", Type: "stat", Title: "今日收入", Source: "orders", Value: "revenue", Unit: "¥", Format: "money", At: []int{3, 0, 3, 2}},
			{ID: "work.stars", Type: "stat", Title: "GitHub stars", Source: "github", Value: "stars", Format: "int",
				Trend: true, Window: dur(30 * time.Minute), Accent: "sodium", At: []int{6, 0, 3, 2}},
			{ID: "work.fx", Type: "stat", Title: "NZD → CNY", Source: "fx", Value: "nzdcny", Decimals: n(3),
				Trend: true, Window: dur(30 * time.Minute), At: []int{9, 0, 3, 2}},
			{ID: "work.usage", Type: "bars", Title: "本周 AI 花费", Source: "ai_usage", Unit: "¥", Decimals: n(1), At: []int{0, 2, 5, 3}},
			{ID: "work.ci", Type: "status", Title: "CI", Source: "github", Items: "checks", Detail: "detail",
				ItemActions: []string{"demo-rerun"}, At: []int{5, 2, 3, 3}},
			{ID: "work.todo", Type: "list", Title: "待办", Source: "todo", Limit: 5, ItemActions: []string{"demo-done"}, At: []int{8, 2, 4, 3}},
			{ID: "work.note", Type: "text", Title: "留言板", Source: "notes", Size: "l", At: []int{0, 5, 5, 3}},
			{ID: "work.latency", Type: "timeseries", Title: "服务延迟", Source: "orders", Unit: "ms",
				Series: []config.Series{{Label: "p95", Points: "latency"}}, At: []int{5, 5, 7, 3}},
		}},
	}
}

// DemoActions back the demo buttons; they only change demo data.
func DemoActions() []config.Action {
	mk := func(name, title, confirm string, param bool) config.Action {
		a := config.Action{Name: name, Title: title, Type: "demo", Confirm: confirm, Cooldown: dur(time.Second),
			Refresh: []string{"demo", "demo-sources"}}
		if param {
			a.Param = config.PatternParam(`[\p{L}\p{N} ._:/@+=-]{1,80}`)
		}
		return a
	}
	return []config.Action{
		mk("demo-topup", "重置额度", "hold", false),
		mk("demo-restart", "重启", "hold", true),
		mk("demo-pause", "暂停/继续", "tap", true),
		mk("demo-rerun", "重跑", "hold", true),
		mk("demo-done", "完成", "tap", true),
		mk("demo-lights", "客厅灯", "none", false),
		mk("demo-fan", "风扇", "none", false),
		mk("demo-gate", "开车库门", "hold", false),
		mk(config.PlanResetAction, "重置用量", "hold", true),
	}
}

// PlanResetAction spends one saved subscription reset (param = account key).
func PlanResetAction() config.Action {
	return config.Action{
		Name: config.PlanResetAction, Title: "重置用量", Type: "plan", Confirm: "hold",
		Param:    config.PatternParam(config.PlanKeyPattern),
		Timeout:  dur(90 * time.Second),
		Cooldown: dur(30 * time.Second),
		Refresh:  []string{config.PlanSource},
	}
}
