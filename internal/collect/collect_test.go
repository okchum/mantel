package collect

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/okchum/mantel/internal/state"
)

const nodeFixture = `# HELP node_cpu_seconds_total Seconds the CPUs spent in each mode.
# TYPE node_cpu_seconds_total counter
node_cpu_seconds_total{cpu="0",mode="idle"} 900
node_cpu_seconds_total{cpu="0",mode="user"} 80
node_cpu_seconds_total{cpu="0",mode="system"} 20
node_cpu_seconds_total{cpu="1",mode="idle"} 950
node_cpu_seconds_total{cpu="1",mode="iowait"} 10
node_cpu_seconds_total{cpu="1",mode="user"} 40
node_memory_MemTotal_bytes 8e+09
node_memory_MemAvailable_bytes 2e+09
node_filesystem_size_bytes{device="/dev/sda1",fstype="ext4",mountpoint="/"} 1000
node_filesystem_avail_bytes{device="/dev/sda1",fstype="ext4",mountpoint="/"} 250
node_filesystem_size_bytes{device="tmpfs",fstype="tmpfs",mountpoint="/run"} 10
node_network_receive_bytes_total{device="eth0"} 5000
node_network_receive_bytes_total{device="lo"} 999999
node_network_receive_bytes_total{device="veth12ab"} 777
node_network_transmit_bytes_total{device="eth0"} 3000
node_load1 0.42
node_boot_time_seconds 1.7e+09
node_time_seconds 1.7000864e+09
node_hwmon_temp_celsius{chip="platform_coretemp_0",sensor="temp1"} 47
node_hwmon_temp_celsius{chip="platform_coretemp_0",sensor="temp2"} 52
node_uname_info{machine="x86_64",nodename="nas",sysname="Linux"} 1
`

func TestReadNode(t *testing.T) {
	samples, err := parseProm(strings.NewReader(nodeFixture), func(n string) bool { return wantedFamilies[n] })
	if err != nil {
		t.Fatal(err)
	}
	r := readNode(samples, "/")
	check := func(name string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 1e-6 {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	check("cpuTotal", r.cpuTotal, 2000)
	check("cpuBusy", r.cpuBusy, 140)
	check("cores", float64(r.cores), 2)
	check("memUsed", ratio(r.memTotal-r.memAvail, r.memTotal), 0.75)
	check("disk", ratio(r.diskSize-r.diskAvail, r.diskSize), 0.75)
	check("rx", r.rx, 5000)
	check("tx", r.tx, 3000)
	check("load", r.load1, 0.42)
	check("uptime", r.uptime, 86400)
	if r.temp == nil || *r.temp != 52 {
		t.Errorf("temp = %v, want 52", r.temp)
	}
}

func TestParseLabelsEscapes(t *testing.T) {
	m := parseLabels(`a="x\"y",b="c,d", c="\\n"`)
	if m["a"] != `x"y` || m["b"] != "c,d" || m["c"] != `\n` {
		t.Fatalf("got %#v", m)
	}
}

func TestPathNum(t *testing.T) {
	var v any
	_ = json.Unmarshal([]byte(`{"balance_infos":[{"currency":"CNY","total_balance":"110.50"}],"data":{"x":null,"n":3}}`), &v)
	if got, err := pathNum(v, "balance_infos.0.total_balance"); err != nil || got != 110.5 {
		t.Fatalf("string number: %v %v", got, err)
	}
	if got, err := pathNum(v, "data.n"); err != nil || got != 3 {
		t.Fatalf("number: %v %v", got, err)
	}
	if _, err := pathNum(v, "data.x"); err == nil {
		t.Fatal("null should error")
	}
	if _, err := pathNum(v, "data.missing"); err == nil {
		t.Fatal("missing should error")
	}
}

func TestSpentSinceIgnoresTopUps(t *testing.T) {
	t0 := time.Now().Add(-30 * time.Hour)
	series := []state.Sample{
		{At: t0, V: 100},                     // outside window
		{At: t0.Add(10 * time.Hour), V: 90},  // spent 10 (inside: at -20h)
		{At: t0.Add(20 * time.Hour), V: 150}, // top-up, ignored
		{At: t0.Add(29 * time.Hour), V: 140}, // spent 10
	}
	got := spentSince(series, time.Now().Add(-24*time.Hour), "remaining")
	if got == nil || *got != 20 {
		t.Fatalf("spent = %v, want 20", got)
	}
}

// Without node_time_seconds the uptime is unknown (0), not minus the boot time.
func TestNodeUptimeNeedsBothClocks(t *testing.T) {
	samples, err := parseProm(strings.NewReader("node_boot_time_seconds 1.7e9\n"), func(n string) bool { return wantedFamilies[n] })
	if err != nil {
		t.Fatal(err)
	}
	if u := readNode(samples, "/").uptime; u != 0 {
		t.Fatalf("uptime = %v, want 0", u)
	}
}
