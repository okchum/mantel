package collect

import (
	"context"
	"math"
	"math/rand/v2"
	"time"

	"github.com/okchum/mantel/internal/state"
)

// Demo fills every panel with plausible moving data so the layout and motion
// can be judged on the device before any real source is configured.
func Demo(ctx context.Context, st *state.Store, loc *time.Location) {
	type walker struct{ cpu, net float64 }
	servers := []struct {
		name  string
		cores int
		mem   float64
		disk  float64
		base  float64
	}{
		{"nas", 4, 0.62, 0.71, 0.12},
		{"homelab", 16, 0.48, 0.38, 0.35},
		{"vps-tokyo", 2, 0.81, 0.56, 0.22},
		{"pi-hole", 4, 0.27, 0.19, 0.05},
	}
	walk := make([]walker, len(servers))
	probes := []string{"Blog", "Gitea", "Jellyfin", "Home VPN", "Status API"}
	quotas := []struct {
		name, unit string
		value, lim float64
		burn       float64
	}{
		{"DeepSeek", "¥", 4.6, 0, 0.004},
		{"OpenRouter", "$", 12.8, 25, 0.0007},
		{"Moonshot", "¥", 41.2, 0, 0.002},
	}
	now := time.Now().In(loc)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	at := func(d, h, m int) time.Time {
		return day.AddDate(0, 0, d).Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
	}
	soon := now.Truncate(time.Minute).Add(14 * time.Minute)
	events := []state.Event{
		{Title: "Bin night", Calendar: "Home", Color: "#9ccf8f", Start: day, End: day.AddDate(0, 0, 1), AllDay: true},
		{Title: "Stand-up", Calendar: "Work", Color: "#7fb7ff", Start: at(0, 9, 30), End: at(0, 9, 45)},
		{Title: "Renew TLS certs on nas", Calendar: "Home", Color: "#9ccf8f", Start: soon, End: soon.Add(30 * time.Minute)},
		{Title: "Dentist", Location: "Ponsonby Rd", Calendar: "Personal", Color: "#f5a97f", Start: soon.Add(3 * time.Hour), End: soon.Add(4 * time.Hour)},
		{Title: "Dinner with Mia", Calendar: "Personal", Color: "#f5a97f", Start: at(0, 19, 0), End: at(0, 21, 0)},
		{Title: "Backup drill", Calendar: "Home", Color: "#9ccf8f", Start: at(1, 10, 0), End: at(1, 11, 0)},
		{Title: "Design review", Calendar: "Work", Color: "#7fb7ff", Start: at(1, 14, 0), End: at(1, 15, 30)},
		{Title: "Flight NZ90 to Tokyo", Calendar: "Personal", Color: "#f5a97f", Start: at(3, 9, 15), End: at(3, 20, 0)},
	}

	st.Update(func(s *state.Snapshot) {
		s.Servers = make([]state.Server, len(servers))
		s.Probes = make([]state.Probe, len(probes))
		s.Quotas = make([]state.Quota, len(quotas))
		s.Events = events
		hourly := []state.WeatherHour{}
		for h := 0; h < 24; h++ {
			t := now.Truncate(time.Hour).Add(time.Duration(h) * time.Hour)
			hourly = append(hourly, state.WeatherHour{
				Time: t.Format("2006-01-02T15:04"),
				Temp: 16 + 5*math.Sin(float64(t.Hour()-9)/24*2*math.Pi),
				Pop:  math.Max(0, 60*math.Sin(float64(h)/24*math.Pi*2)),
			})
		}
		s.Weather = &state.Weather{
			Name: "Auckland", Temp: 18.4, Feels: 17.1, Code: 2, Wind: 14, Humidity: 71, IsDay: true,
			Hourly: hourly, Updated: now,
			Daily: []state.WeatherDay{
				{Date: day.Format("2006-01-02"), Code: 2, Max: 21, Min: 13, Pop: 10},
				{Date: day.AddDate(0, 0, 1).Format("2006-01-02"), Code: 61, Max: 18, Min: 12, Pop: 70},
				{Date: day.AddDate(0, 0, 2).Format("2006-01-02"), Code: 3, Max: 19, Min: 11, Pop: 30},
				{Date: day.AddDate(0, 0, 3).Format("2006-01-02"), Code: 0, Max: 23, Min: 12, Pop: 0},
			},
		}
	})

	tick := 0
	every(ctx, "demo", 2*time.Second, func() {
		tick++
		st.Update(func(s *state.Snapshot) {
			for i, sv := range servers {
				w := &walk[i]
				w.cpu = clamp(w.cpu*0.8+sv.base*0.2+(rand.Float64()-0.5)*0.12, 0.01, 0.98)
				if rand.Float64() < 0.03 {
					w.cpu = clamp(w.cpu+0.5, 0, 0.97) // occasional burst
				}
				w.net = math.Max(0, w.net*0.7+(rand.Float64()*4e6+5e5)*0.3*(1+float64(i%2)))
				out := &s.Servers[i]
				out.Name, out.Up, out.Cores = sv.name, true, sv.cores
				out.CPU, out.Mem, out.Disk = w.cpu, sv.mem+(rand.Float64()-0.5)*0.01, sv.disk
				out.MemTotal = float64(sv.cores*4) * (1 << 30)
				out.Load1 = w.cpu * float64(sv.cores)
				out.NetRx, out.NetTx = w.net*0.8, w.net*0.2
				out.Uptime = float64(86400*(12+i*30)) + float64(tick*2)
				t := 38 + w.cpu*30
				out.Temp = &t
				out.CPUHist = state.Push(out.CPUHist, w.cpu, histLen)
				out.NetHist = state.Push(out.NetHist, w.net, histLen)
				out.Updated = time.Now()
			}
			// pi-hole drops off the network for a while every few minutes.
			if (tick/60)%3 == 2 {
				s.Servers[3].Up, s.Servers[3].Error = false, "connection refused"
			} else {
				s.Servers[3].Error = ""
			}
			for i, name := range probes {
				p := &s.Probes[i]
				p.Name, p.URL, p.Checked, p.At = name, "https://example.invalid/"+name, true, time.Now()
				down := i == 3 && (tick/45)%2 == 1
				if down != !p.Up || p.Since.IsZero() {
					p.Since = time.Now()
				}
				p.Up = !down
				if down {
					p.Status, p.Error = 0, "timeout"
					p.Hist = state.Push(p.Hist, -1, histLen)
				} else {
					p.Status, p.Error = 200, ""
					p.LatencyMS = 40 + float64(i)*35 + rand.Float64()*30
					p.Hist = state.Push(p.Hist, p.LatencyMS, histLen)
				}
			}
			world.mu.Lock()
			if world.toppedUp {
				world.toppedUp = false
				quotas[0].value = 100
			}
			world.mu.Unlock()
			for i := range quotas {
				q := &quotas[i]
				q.value = math.Max(0, q.value-q.burn*rand.Float64()*3)
				out := &s.Quotas[i]
				out.Name, out.Unit, out.Mode = q.name, q.unit, "remaining"
				out.Value, out.Limit = q.value, q.lim
				out.Hist = state.Push(out.Hist, q.value, histLen)
				spent := q.burn * 2600
				out.Spent24 = &spent
				out.Updated = time.Now()
			}
		})
	})
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
