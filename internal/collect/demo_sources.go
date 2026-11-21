package collect

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/jpath"
	"github.com/okchum/mantel/internal/plans"
	"github.com/okchum/mantel/internal/state"
)

// demoWorld is the mutable fake home/work the demo sources describe; demo
// actions change it so buttons visibly do something.
type demoWorld struct {
	mu         sync.Mutex
	containers []demoContainer
	downloads  []demoDownload
	todos      []string
	orders     float64
	revenue    float64
	stars      float64
	fx         float64
	kwh        float64
	ups        float64
	lights     bool
	fan        bool
	checks     map[string]string
	note       string
	latency    []any
	toppedUp   bool
	plans      []*demoPlan
	plansAt    time.Time
}

type demoContainer struct {
	name, image, state string
	until              time.Time // restarting until
}

type demoDownload struct {
	name   string
	done   float64
	speed  float64 // fraction per tick
	paused bool
}

var world = &demoWorld{
	containers: []demoContainer{
		{name: "jellyfin", image: "jellyfin/jellyfin", state: "running"},
		{name: "gitea", image: "gitea/gitea:1.22", state: "running"},
		{name: "immich", image: "immich-server", state: "running"},
		{name: "vaultwarden", image: "vaultwarden/server", state: "running"},
		{name: "paperless", image: "paperless-ngx", state: "exited"},
	},
	downloads: []demoDownload{
		{name: "Ubuntu 24.04.1 LTS", done: .62, speed: .004},
		{name: "Blender 4.2 Splash Pack", done: .18, speed: .007},
		{name: "Planet Earth III S01", done: .91, speed: .002},
		{name: "Arch Linux ISO", done: .05, speed: .01, paused: true},
	},
	todos:  []string{"换卧室灯泡", "续费域名 example.dev", "整理 NAS 照片", "给妈妈打电话", "报税"},
	orders: 128, revenue: 18640, stars: 4213, fx: 4.182, kwh: 6.4, ups: 100,
	checks: map[string]string{"web": "success", "api": "success", "ios": "in_progress", "infra": "failure"},
	note:   "晚饭在冰箱，热 3 分钟就行。\n周六上午 10 点物业检修水管。",
}

// DemoSources fills the generic demo sources every two seconds.
func DemoSources(ctx context.Context, st *state.Store) {
	start := time.Now()
	every(ctx, "demo-sources", 2*time.Second, func() {
		t := time.Since(start).Seconds()
		w := world
		w.mu.Lock()
		now := time.Now()
		// Power wanders around a base with a kettle spike now and then.
		watts := 620 + 180*math.Sin(t/40) + rand.Float64()*60
		if int(t/45)%4 == 1 {
			watts += 2100
		}
		if w.fan {
			watts += 45
		}
		if w.lights {
			watts += 120
		}
		w.kwh += watts / 3600 / 1000 * 2
		w.ups = math.Max(64, w.ups-rand.Float64()*.05)
		if rand.Float64() < .3 {
			w.orders++
			w.revenue += 40 + rand.Float64()*260
		}
		if rand.Float64() < .05 {
			w.stars++
		}
		w.fx += (rand.Float64() - .5) * .002
		for i := range w.downloads {
			d := &w.downloads[i]
			if !d.paused && d.done < 1 {
				d.done = math.Min(1, d.done+d.speed*(.6+rand.Float64()*.8))
			}
		}
		for i := range w.containers {
			c := &w.containers[i]
			if c.state == "restarting" && now.After(c.until) {
				c.state = "running"
			}
		}
		if len(w.latency) == 0 {
			for i := 0; i < 60; i++ {
				w.latency = append(w.latency, 120+40*math.Sin(float64(i)/6)+rand.Float64()*30)
			}
		}
		if int(t)%10 == 0 {
			spike := 0.0
			if rand.Float64() < .15 {
				spike = 180
			}
			w.latency = append(w.latency[1:], 120+40*math.Sin(t/60)+rand.Float64()*30+spike)
		}

		containers := []any{}
		for _, c := range w.containers {
			containers = append(containers, map[string]any{"label": c.name, "state": c.state, "image": c.image})
		}
		downloads := []any{}
		for _, d := range w.downloads {
			detail := fmt.Sprintf("%.1f MB/s", d.speed*900*(.6+rand.Float64()*.8))
			state := ""
			switch {
			case d.done >= 1:
				detail, state = "完成", "ok"
			case d.paused:
				detail, state = "已暂停", "off"
			}
			downloads = append(downloads, map[string]any{"label": d.name, "value": d.done, "detail": detail, "state": state})
		}
		todos := []any{}
		for i, x := range w.todos {
			item := map[string]any{"label": x}
			if i == 1 {
				item["badge"] = "明天到期"
				item["state"] = "warn"
			}
			todos = append(todos, item)
		}
		checks := []any{}
		for _, k := range []string{"web", "api", "ios", "infra"} {
			checks = append(checks, map[string]any{"label": k, "state": w.checks[k], "detail": w.checks[k]})
		}
		if w.plans == nil {
			w.plans, w.plansAt = demoPlans(now), now
		}
		var accts []plans.Account
		for _, p := range w.plans {
			accts = append(accts, p.step(now, now.Sub(w.plansAt)))
		}
		w.plansAt = now
		hour := now.Hour()
		docs := map[string]any{
			"power": map[string]any{"watts": watts, "today_kwh": w.kwh, "ups": w.ups, "lights": w.lights, "fan": w.fan},
			"climate": map[string]any{
				"living":   21.5 + math.Sin(t/90)*1.2 + rand.Float64()*.2,
				"bedroom":  19.8 + math.Sin(t/120+1)*.8 + rand.Float64()*.2,
				"outdoor":  14 + math.Sin(t/150)*3 + rand.Float64()*.3,
				"humidity": 58 + math.Sin(t/70)*9,
			},
			"containers": map[string]any{"items": containers},
			"downloads":  map[string]any{"items": downloads},
			"github":     map[string]any{"stars": w.stars, "checks": checks},
			"todo":       map[string]any{"items": todos},
			"orders":     map[string]any{"today": w.orders, "revenue": w.revenue, "latency": slices.Clone(w.latency)},
			"backup": map[string]any{"items": []any{
				map[string]any{"label": "照片 → B2", "value": "2 小时前", "state": "ok"},
				map[string]any{"label": "Gitea → NAS", "value": fmt.Sprintf("%d 分钟前", 10+hour%40), "state": "ok"},
				map[string]any{"label": "VPS 快照", "value": "3 天前", "state": "warn", "detail": "超过 48 小时"},
				map[string]any{"label": "数据库 dump", "value": "失败", "state": "crit", "detail": "disk full"},
			}},
			"fx":              map[string]any{"nzdcny": w.fx},
			"notes":           map[string]any{"text": w.note},
			config.PlanSource: jpath.Normalize(map[string]any{"accounts": accts}),
			"ai_usage": map[string]any{"items": []any{
				map[string]any{"label": "一", "value": 12.4}, map[string]any{"label": "二", "value": 8.1},
				map[string]any{"label": "三", "value": 21.7}, map[string]any{"label": "四", "value": 15.2},
				map[string]any{"label": "五", "value": 9.6}, map[string]any{"label": "六", "value": 3.3},
				map[string]any{"label": "日", "value": 6.0 + math.Mod(t/30, 4)},
			}},
		}
		w.mu.Unlock()
		for name, doc := range docs {
			_ = Push(st, name, doc, false)
		}
	})
}

// DemoAction carries out a demo button press against the fake world.
func DemoAction(st *state.Store) func(name, param string) (string, error) {
	return func(name, param string) (string, error) {
		w := world
		w.mu.Lock()
		defer w.mu.Unlock()
		switch name {
		case "demo-topup":
			w.toppedUp = true
			return "DeepSeek 已重置为 ¥100", nil
		case "demo-restart":
			for i := range w.containers {
				if w.containers[i].name == param {
					w.containers[i].state = "restarting"
					w.containers[i].until = time.Now().Add(6 * time.Second)
					return param + " 正在重启", nil
				}
			}
			return "", fmt.Errorf("没有容器 %s", param)
		case "demo-pause":
			for i := range w.downloads {
				if w.downloads[i].name == param {
					w.downloads[i].paused = !w.downloads[i].paused
					if w.downloads[i].paused {
						return "已暂停", nil
					}
					return "继续下载", nil
				}
			}
			return "", fmt.Errorf("没有任务 %s", param)
		case "demo-rerun":
			if _, ok := w.checks[param]; !ok {
				return "", fmt.Errorf("没有流水线 %s", param)
			}
			w.checks[param] = "in_progress"
			go func() {
				time.Sleep(8 * time.Second)
				w.mu.Lock()
				w.checks[param] = "success"
				w.mu.Unlock()
			}()
			return param + " 已重新排队", nil
		case "demo-done":
			if i := slices.Index(w.todos, param); i >= 0 {
				w.todos = slices.Delete(w.todos, i, i+1)
				return "完成：" + param, nil
			}
			return "", fmt.Errorf("没有这一项")
		case "demo-lights":
			w.lights = !w.lights
			if w.lights {
				return "灯已打开", nil
			}
			return "灯已关闭", nil
		case "demo-fan":
			w.fan = !w.fan
			if w.fan {
				return "风扇已打开", nil
			}
			return "风扇已关闭", nil
		case config.PlanResetAction:
			for _, p := range w.plans {
				if p.acct.Key == param {
					return p.reset()
				}
			}
			return "", fmt.Errorf("没有这个订阅账号")
		case "demo-gate":
			if rand.Float64() < .3 {
				return "", fmt.Errorf("车库门控制器没有响应")
			}
			return "车库门正在打开", nil
		}
		return "", fmt.Errorf("unknown demo action %s", name)
	}
}
