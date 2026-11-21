package collect

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/state"
)

const histLen = 60

// sample is one parsed metric line: name{labels} value.
type sample struct {
	name   string
	labels map[string]string
	value  float64
}

// parseProm parses the Prometheus text exposition format, keeping only the
// metric families we need (filter) to stay cheap on large node_exporter pages.
func parseProm(r io.Reader, keep func(name string) bool) ([]sample, error) {
	var out []sample
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		nameEnd := strings.IndexAny(line, "{ ")
		if nameEnd <= 0 {
			continue
		}
		name := line[:nameEnd]
		if !keep(name) {
			continue
		}
		s := sample{name: name}
		rest := line[nameEnd:]
		if rest[0] == '{' {
			end := strings.LastIndexByte(rest, '}')
			if end < 0 {
				continue
			}
			s.labels = parseLabels(rest[1:end])
			rest = rest[end+1:]
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue
		}
		s.value = v
		out = append(out, s)
	}
	return out, sc.Err()
}

func parseLabels(s string) map[string]string {
	m := map[string]string{}
	for len(s) > 0 {
		eq := strings.IndexByte(s, '=')
		if eq < 0 || eq+1 >= len(s) || s[eq+1] != '"' {
			break
		}
		key := strings.TrimSpace(strings.TrimPrefix(s[:eq], ","))
		var b strings.Builder
		i := eq + 2
		for ; i < len(s); i++ {
			c := s[i]
			if c == '\\' && i+1 < len(s) {
				i++
				switch s[i] {
				case 'n':
					b.WriteByte('\n')
				default:
					b.WriteByte(s[i])
				}
				continue
			}
			if c == '"' {
				break
			}
			b.WriteByte(c)
		}
		m[key] = b.String()
		s = strings.TrimLeft(s[min(i+1, len(s)):], ", ")
	}
	return m
}

var wantedFamilies = map[string]bool{
	"node_cpu_seconds_total":            true,
	"node_memory_MemTotal_bytes":        true,
	"node_memory_MemAvailable_bytes":    true,
	"node_memory_total_bytes":           true, // darwin
	"node_memory_free_bytes":            true,
	"node_memory_inactive_bytes":        true,
	"node_memory_purgeable_bytes":       true,
	"node_filesystem_size_bytes":        true,
	"node_filesystem_avail_bytes":       true,
	"node_network_receive_bytes_total":  true,
	"node_network_transmit_bytes_total": true,
	"node_load1":                        true,
	"node_boot_time_seconds":            true,
	"node_time_seconds":                 true,
	"node_hwmon_temp_celsius":           true,
	"node_thermal_zone_temp":            true,
}

// nodeReading is the raw counters from one scrape.
type nodeReading struct {
	at                  time.Time
	cpuBusy, cpuTotal   float64
	cores               int
	memTotal, memAvail  float64
	diskSize, diskAvail float64
	rx, tx              float64
	load1               float64
	uptime              float64
	temp                *float64
}

func skipIface(dev string) bool {
	for _, p := range []string{"lo", "veth", "docker", "br-", "virbr", "cni", "flannel", "cali", "tun", "tap", "utun", "gif", "stf", "awdl", "llw", "anpi", "bridge"} {
		if strings.HasPrefix(dev, p) {
			return true
		}
	}
	return false
}

func readNode(samples []sample, mount string) nodeReading {
	var r nodeReading
	cpus := map[string]bool{}
	var darwinFree, darwinInactive, darwinPurge float64
	haveAvail := false
	var boot, now float64
	for _, s := range samples {
		switch s.name {
		case "node_cpu_seconds_total":
			cpus[s.labels["cpu"]] = true
			r.cpuTotal += s.value
			switch s.labels["mode"] {
			case "idle", "iowait":
			default:
				r.cpuBusy += s.value
			}
		case "node_memory_MemTotal_bytes", "node_memory_total_bytes":
			r.memTotal = s.value
		case "node_memory_MemAvailable_bytes":
			r.memAvail, haveAvail = s.value, true
		case "node_memory_free_bytes":
			darwinFree = s.value
		case "node_memory_inactive_bytes":
			darwinInactive = s.value
		case "node_memory_purgeable_bytes":
			darwinPurge = s.value
		case "node_filesystem_size_bytes":
			if s.labels["mountpoint"] == mount {
				r.diskSize = s.value
			}
		case "node_filesystem_avail_bytes":
			if s.labels["mountpoint"] == mount {
				r.diskAvail = s.value
			}
		case "node_network_receive_bytes_total":
			if !skipIface(s.labels["device"]) {
				r.rx += s.value
			}
		case "node_network_transmit_bytes_total":
			if !skipIface(s.labels["device"]) {
				r.tx += s.value
			}
		case "node_load1":
			r.load1 = s.value
		case "node_boot_time_seconds":
			boot = s.value
		case "node_time_seconds":
			now = s.value
		case "node_hwmon_temp_celsius", "node_thermal_zone_temp":
			if s.value > 0 && s.value < 150 && (r.temp == nil || s.value > *r.temp) {
				v := s.value
				r.temp = &v
			}
		}
	}
	if !haveAvail {
		r.memAvail = darwinFree + darwinInactive + darwinPurge
	}
	r.cores = len(cpus)
	// Uptime needs both; node_time_seconds is missing on some exporters and
	// an uptime of minus the boot time would show as nonsense.
	if boot > 0 && now > boot {
		r.uptime = now - boot
	}
	return r
}

func ratio(a, b float64) float64 {
	if b <= 0 || math.IsNaN(a/b) {
		return 0
	}
	return math.Max(0, math.Min(1, a/b))
}

// NodeExporter scrapes one node_exporter endpoint into store slot idx; it
// answers Kick("server:"+srv.Name).
func NodeExporter(ctx context.Context, st *state.Store, idx int, srv config.Server, interval time.Duration) {
	client := NewClient(ClientOpts{Timeout: 4 * time.Second})
	var prev *nodeReading
	every(ctx, "server:"+srv.Name, interval, func() {
		r, err := scrapeNode(ctx, client, srv)
		if ctx.Err() != nil {
			return // shutting down: keep the last reading rather than "context canceled"
		}
		st.Update(func(s *state.Snapshot) {
			out := &s.Servers[idx]
			out.Name = srv.Name
			if err != nil {
				out.Up = false
				out.Error = err.Error()
				prev = nil
				return
			}
			out.Up, out.Error, out.Updated = true, "", r.at
			out.Mem = ratio(r.memTotal-r.memAvail, r.memTotal)
			out.MemTotal = r.memTotal
			out.Disk = ratio(r.diskSize-r.diskAvail, r.diskSize)
			out.Load1, out.Cores, out.Uptime, out.Temp = r.load1, r.cores, r.uptime, r.temp
			if prev != nil {
				dt := r.at.Sub(prev.at).Seconds()
				if r.cpuTotal > prev.cpuTotal && r.cpuBusy >= prev.cpuBusy { // counters reset on reboot
					out.CPU = ratio(r.cpuBusy-prev.cpuBusy, r.cpuTotal-prev.cpuTotal)
					out.CPUHist = state.Push(out.CPUHist, out.CPU, histLen)
				}
				if dt > 0 && r.rx >= prev.rx && r.tx >= prev.tx { // counters reset on reboot
					out.NetRx = (r.rx - prev.rx) / dt
					out.NetTx = (r.tx - prev.tx) / dt
				}
				out.NetHist = state.Push(out.NetHist, out.NetRx+out.NetTx, histLen)
			}
			cp := r
			prev = &cp
		})
	})
}

func scrapeNode(ctx context.Context, client *http.Client, srv config.Server) (nodeReading, error) {
	body, err := get(ctx, client, srv.URL, "text/plain", nil)
	if err != nil {
		return nodeReading{}, err
	}
	samples, err := parseProm(bytes.NewReader(body), func(n string) bool { return wantedFamilies[n] })
	if err != nil {
		return nodeReading{}, err
	}
	if len(samples) == 0 {
		return nodeReading{}, fmt.Errorf("no node_exporter metrics at %s", srv.URL)
	}
	r := readNode(samples, srv.Mount)
	r.at = time.Now()
	return r, nil
}
