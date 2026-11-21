package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func loadCfg(t *testing.T, js string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(p, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

// page wraps widgets into a minimal config with one push source "s" and an
// action "go", so each case only states what it tests.
func page(widgets string) string {
	return `{"sources":[{"name":"s","type":"push"}],
	"actions":[{"name":"go","type":"http","url":"http://x/"}],
	"pages":[{"name":"p","widgets":[` + widgets + `]}]}`
}

func TestNormalizeDashErrors(t *testing.T) {
	cases := []struct {
		name, js string
		wantErr  []string // every substring must appear; empty = must load
	}{
		{"valid", page(`{"type":"stat","source":"s","at":[0,0,12,8]}`), nil},
		{"grid exact fit", page(`{"type":"stat","source":"s","at":[11,7,1,1]}`), nil},
		{"side by side", page(`{"type":"stat","source":"s","at":[0,0,6,8]},{"type":"gauge","source":"s","at":[6,0,6,8]}`), nil},
		{"past right edge", page(`{"type":"stat","source":"s","at":[8,0,5,1]}`), []string{"inside the 12x8 grid"}},
		{"past bottom", page(`{"type":"stat","source":"s","at":[0,7,1,2]}`), []string{"inside the 12x8 grid"}},
		{"negative col", page(`{"type":"stat","source":"s","at":[-1,0,1,1]}`), []string{"inside the 12x8 grid"}},
		{"zero width", page(`{"type":"stat","source":"s","at":[0,0,0,1]}`), []string{"inside the 12x8 grid"}},
		{"three coords", page(`{"type":"stat","source":"s","at":[0,0,1]}`), []string{"inside the 12x8 grid"}},
		{"missing at", page(`{"type":"stat","source":"s"}`), []string{"must be [col,row,w,h]"}},
		{"custom grid", `{"display":{"cols":4,"rows":2},"sources":[{"name":"s","type":"push"}],
			"pages":[{"name":"p","widgets":[{"type":"stat","source":"s","at":[0,0,4,3]}]}]}`, []string{"inside the 4x2 grid"}},
		{"overlap", page(`{"type":"stat","source":"s","at":[0,0,4,4]},{"type":"stat","source":"s","at":[3,3,2,2]}`),
			[]string{"pages p widgets[1] (stat): overlaps p.0 at cell [3,3]"}},
		{"overlap reported once", page(`{"type":"stat","source":"s","at":[0,0,4,4]},{"type":"stat","source":"s","at":[0,0,4,4]}`),
			[]string{"overlaps p.0 at cell [0,0]"}},
		{"unknown type", page(`{"type":"pie","source":"s","at":[0,0,1,1]}`), []string{"(pie): unknown type; have clock"}},
		{"unknown source", page(`{"type":"stat","source":"nope","at":[0,0,1,1]}`), []string{`unknown source "nope"`}},
		{"unknown series source", page(`{"type":"timeseries","at":[0,0,1,1],"series":[{"source":"zz"}]}`), []string{`series[0]: unknown source "zz"`}},
		{"series without source", page(`{"type":"timeseries","at":[0,0,1,1],"series":[{"value":"v"}]}`), []string{"series[0]: no source"}},
		{"stat needs source", page(`{"type":"stat","at":[0,0,1,1]}`), []string{"source is required"}},
		{"status with names ok", page(`{"type":"status","names":["x"],"at":[0,0,1,1]}`), nil},
		{"status needs source", page(`{"type":"status","at":[0,0,1,1]}`), []string{"source is required"}},
		{"countdown needs to", page(`{"type":"countdown","at":[0,0,1,1]}`), []string{"to is required"}},
		{"image needs url", page(`{"type":"image","at":[0,0,1,1]}`), []string{"url is required"}},
		{"unknown widget action", page(`{"type":"buttons","actions":["go","nope"],"at":[0,0,1,1]}`), []string{`unknown action "nope"`}},
		{"unknown item action", page(`{"type":"list","source":"s","item_actions":["bad"],"at":[0,0,1,1]}`), []string{`unknown action "bad"`}},
		{"duplicate widget id", page(`{"id":"w","type":"clock","at":[0,0,1,1]},{"id":"w","type":"clock","at":[1,0,1,1]}`), []string{`duplicate id "w"`}},
		{"duplicate page", `{"pages":[{"name":"a"},{"name":"a"}]}`, []string{`pages[1]: duplicate name "a"`}},
		{"bad home", `{"display":{"home":"zz"},"pages":[{"name":"a"}]}`, []string{`display.home: no page "zz"`}},
		{"bad rotate_quiet", `{"display":{"rotate_quiet":"late"}}`, []string{"display.rotate_quiet"}},
		{"text sizes and themes", `{"display":{"text_size":"xlarge","theme":"paper","theme_night":"ember"}}`, nil},
		{"bad text_size", `{"display":{"text_size":"huge"}}`, []string{`display.text_size: "huge" is not one of small, medium, large, xlarge`}},
		{"text_size is case sensitive", `{"display":{"text_size":"Large"}}`, []string{"display.text_size"}},
		{"bad theme", `{"display":{"theme":"solarized"}}`, []string{`display.theme: "solarized" is not one of petrol`}},
		{"bad theme_night", `{"display":{"theme_night":"dark"}}`, []string{`display.theme_night: "dark"`}},

		{"duplicate source", `{"sources":[{"name":"a","type":"push"},{"name":"a","type":"push"}]}`, []string{`sources[1]: duplicate name "a"`}},
		{"source clashes with builtin", `{"servers":[{"name":"n","url":"http://x"}],"sources":[{"name":"server:n","type":"push"}]}`, []string{"name \"server:n\" must be letters"}},
		{"source clashes with weather", `{"weather":{"latitude":1},"sources":[{"name":"weather","type":"push"}]}`, []string{`duplicate name "weather"`}},
		{"source clashes with demo", `{"demo":true,"sources":[{"name":"power","type":"push"}]}`, []string{`duplicate name "power"`}},
		{"bad source name", `{"sources":[{"name":"a b","type":"push"}]}`, []string{`name "a b" must be letters`}},
		{"bad source type", `{"sources":[{"name":"a","type":"ftp"}]}`, []string{"type must be http_json, exec or push"}},
		{"http_json needs url", `{"sources":[{"name":"a","type":"http_json"}]}`, []string{"(a): url is required"}},
		{"exec needs command", `{"sources":[{"name":"a","type":"exec"}]}`, []string{"(a): command is required"}},
		{"alert without line", `{"sources":[{"name":"a","type":"push","alerts":[{"path":"v"}]}]}`, []string{"sources[0].alerts[0]: set above, below or not"}},

		{"duplicate action", `{"actions":[{"name":"a","type":"demo"},{"name":"a","type":"demo"}]}`, []string{`actions[1]: name "a" missing, malformed or duplicate`}},
		{"missing action name", `{"actions":[{"type":"demo"}]}`, []string{`actions[0]: name "" missing`}},
		{"bad action type", `{"actions":[{"name":"a","type":"ssh"}]}`, []string{"type must be http or exec"}},
		{"http action needs url", `{"actions":[{"name":"a","type":"http"}]}`, []string{"(a): url is required"}},
		{"exec action needs command", `{"actions":[{"name":"a","type":"exec"}]}`, []string{"(a): command is required"}},
		{"bad confirm", `{"actions":[{"name":"a","type":"demo","confirm":"swipe"}]}`, []string{"confirm must be hold, tap or none"}},
		{"bad param pattern", `{"actions":[{"name":"a","type":"demo","param":{"pattern":"("}}]}`, []string{"param.pattern"}},
		{"unknown refresh", `{"actions":[{"name":"a","type":"demo","refresh":["nope"]}]}`, []string{`refresh: unknown source "nope"`}},
		{"refresh known", `{"sources":[{"name":"s","type":"push"}],"probes":[{"name":"pr","url":"http://x"}],
			"actions":[{"name":"a","type":"demo","refresh":["s","probe:pr","demo"]}]}`, nil},
		{"server unknown action", `{"servers":[{"name":"n","url":"http://x","actions":["zz"]}]}`, []string{`servers n: unknown action "zz"`}},
		{"probe unknown action", `{"probes":[{"name":"n","url":"http://x","actions":["zz"]}]}`, []string{`probes n: unknown action "zz"`}},
		{"quota unknown action", `{"quotas":[{"provider":"deepseek","actions":["zz"]}]}`, []string{`quotas deepseek: unknown action "zz"`}},

		{"bad scope", `{"tokens":[{"name":"t","token":"0123456789abcdef","scopes":["act","admin"]}]}`, []string{`tokens[0] (t): unknown scope "admin"`}},
		{"good scopes", `{"tokens":[{"name":"t","token":"0123456789abcdef","scopes":["view","act","push"]}]}`, nil},

		{"errors joined", page(`{"type":"pie","source":"nope","at":[0,0,1,1]}`), []string{"unknown type", `unknown source "nope"`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadCfg(t, c.js)
			if len(c.wantErr) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", c.wantErr)
			}
			for _, w := range c.wantErr {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}

func TestOverlapReportedOncePerWidget(t *testing.T) {
	_, err := loadCfg(t, page(`{"type":"stat","source":"s","at":[0,0,4,4]},{"type":"stat","source":"s","at":[0,0,4,4]}`))
	if err == nil {
		t.Fatal("want overlap error")
	}
	if n := strings.Count(err.Error(), "overlaps"); n != 1 {
		t.Errorf("overlap reported %d times, want 1: %v", n, err)
	}
}

func TestNormalizeDashDefaults(t *testing.T) {
	t.Setenv("ACT_TOKEN", "secret-x")
	c, err := loadCfg(t, `{
		"sources":[
			{"name":"poll","type":"http_json","url":"http://h/","interval":"20s"},
			{"name":"slow","type":"exec","command":["true"]},
			{"name":"pushed","type":"push"},
			{"name":"own","type":"http_json","url":"http://h/","stale_after":"7m"}
		],
		"actions":[
			{"name":"a","type":"http","url":"http://h/$ACT_TOKEN","headers":{"Authorization":"Bearer $ACT_TOKEN"}},
			{"name":"b","title":"Bee","type":"exec","command":["echo"],"method":"","confirm":"tap"},
			{"name":"c","type":"http","url":"http://h/","method":"PUT","confirm":"none"}
		],
		"pages":[
			{"widgets":[{"type":"clock","at":[0,0,1,1]},{"id":"mine","type":"clock","at":[1,0,1,1]},{"type":"clock","at":[2,0,1,1]}]},
			{"name":"second","title":"Two","widgets":[{"type":"clock","at":[0,0,1,1]}]}
		]
	}`)
	if err != nil {
		t.Fatal(err)
	}

	d := c.Display
	if d.Cols != 12 || d.Rows != 8 || d.IdleReturn.Duration != 90*time.Second || d.Home != "page1" {
		t.Errorf("display defaults = %+v", d)
	}

	a, b, cc := c.Actions[0], c.Actions[1], c.Actions[2]
	if a.Method != "POST" || a.Confirm != "hold" || a.Title != "a" {
		t.Errorf("action a defaults: method %q confirm %q title %q", a.Method, a.Confirm, a.Title)
	}
	if a.URL != "http://h/secret-x" || a.Headers["Authorization"] != "Bearer secret-x" {
		t.Errorf("action env not expanded: %q %v", a.URL, a.Headers)
	}
	if b.Method != "" || b.Confirm != "tap" || b.Title != "Bee" {
		t.Errorf("exec action b: method %q confirm %q title %q", b.Method, b.Confirm, b.Title)
	}
	if cc.Method != "PUT" || cc.Confirm != "none" {
		t.Errorf("action c: method %q confirm %q", cc.Method, cc.Confirm)
	}
	// Timeout and cooldown stay zero in the config; the runner applies 20s / 5s.
	if a.Timeout.Or(20*time.Second) != 20*time.Second || a.Cooldown.Or(5*time.Second) != 5*time.Second {
		t.Errorf("timeout/cooldown should default via Or: %v %v", a.Timeout, a.Cooldown)
	}

	p1, p2 := c.Pages[0], c.Pages[1]
	if p1.Name != "page1" || p1.Title != "page1" || p2.Title != "Two" {
		t.Errorf("page names: %q/%q, %q", p1.Name, p1.Title, p2.Title)
	}
	ids := []string{p1.Widgets[0].ID, p1.Widgets[1].ID, p1.Widgets[2].ID, p2.Widgets[0].ID}
	want := []string{"page1.0", "mine", "page1.2", "second.0"}
	for i := range ids {
		if ids[i] != want[i] {
			t.Errorf("widget ids = %v, want %v", ids, want)
			break
		}
	}

	stale := map[string]time.Duration{}
	for _, s := range c.Sources {
		stale[s.Name] = s.StaleAfter.Duration
	}
	if stale["poll"] != time.Minute || stale["slow"] != 3*time.Minute || stale["pushed"] != 0 || stale["own"] != 7*time.Minute {
		t.Errorf("stale_after defaults = %v", stale)
	}
}

func TestDisplayHomeExplicit(t *testing.T) {
	c, err := loadCfg(t, `{"display":{"home":"b","cols":6,"rows":4,"idle_return":"5s"},"pages":[{"name":"a"},{"name":"b"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Display.Home != "b" || c.Display.Cols != 6 || c.Display.Rows != 4 || c.Display.IdleReturn.Duration != 5*time.Second {
		t.Errorf("display = %+v", c.Display)
	}
}

func TestBuiltinSources(t *testing.T) {
	c := &Config{
		Servers:   []Server{{Name: "nas"}},
		Probes:    []Probe{{Name: "web"}},
		Quotas:    []Quota{{Name: "ds"}},
		Weather:   &Weather{},
		Calendars: []Calendar{{URL: "x"}},
	}
	got := strings.Join(c.BuiltinSources(), ",")
	if want := "server:nas,probe:web,quota:ds,weather,calendar"; got != want {
		t.Errorf("BuiltinSources = %q, want %q", got, want)
	}
	if got := (&Config{}).BuiltinSources(); len(got) != 0 {
		t.Errorf("empty config: %v", got)
	}
}

func TestTokenCan(t *testing.T) {
	cases := []struct {
		scopes []string
		scope  string
		want   bool
	}{
		{nil, ScopeView, true},
		{nil, ScopeAct, false},
		{nil, ScopePush, false},
		{[]string{"act"}, ScopeAct, true},
		{[]string{"act"}, ScopePush, false},
		{[]string{"push"}, ScopePush, true},
		{[]string{"push"}, ScopeAct, false},
		{[]string{"act", "push"}, ScopePush, true},
		{[]string{"view"}, ScopeView, true},
		{[]string{"act"}, "admin", false},
	}
	for _, c := range cases {
		if got := (Token{Scopes: c.scopes}).Can(c.scope); got != c.want {
			t.Errorf("Token%v.Can(%q) = %v, want %v", c.scopes, c.scope, got, c.want)
		}
	}
}

func TestParamAllows(t *testing.T) {
	cases := []struct {
		name string
		p    *Param
		v    string
		want bool
	}{
		{"nil allows empty", nil, "", true},
		{"nil refuses value", nil, "x", false},
		{"in values", &Param{Values: []string{"a", "b"}}, "b", true},
		{"not in values", &Param{Values: []string{"a", "b"}}, "c", false},
		{"values refuse empty", &Param{Values: []string{"a"}}, "", false},
		{"pattern full match", &Param{Pattern: `[a-z]+`}, "web", true},
		{"pattern is anchored at end", &Param{Pattern: `[a-z]+`}, "web1", false},
		{"pattern is anchored at start", &Param{Pattern: `[a-z]+`}, "1web", false},
		{"alternation anchored as a whole", &Param{Pattern: `a|b`}, "ab", false},
		{"alternation", &Param{Pattern: `a|bc`}, "bc", true},
		{"pattern refuses injection", &Param{Pattern: `[\w-]+`}, "x/../y", false},
		{"values or pattern", &Param{Values: []string{"special one"}, Pattern: `\d+`}, "special one", true},
		{"values or pattern 2", &Param{Values: []string{"special one"}, Pattern: `\d+`}, "42", true},
		{"bad pattern refuses", &Param{Pattern: `(`}, "(", false},
		{"empty param refuses", &Param{}, "x", false},
	}
	for _, c := range cases {
		if got := c.p.Allows(c.v); got != c.want {
			t.Errorf("%s: Allows(%q) = %v, want %v", c.name, c.v, got, c.want)
		}
	}
}

func TestDisplayLookDefaults(t *testing.T) {
	c, err := loadCfg(t, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if d := c.Display; d.TextSize != "medium" || d.Theme != "petrol" || d.ThemeNight != "" {
		t.Errorf("defaults = %q %q %q, want medium petrol and no night theme", d.TextSize, d.Theme, d.ThemeNight)
	}
	c, err = loadCfg(t, `{"display":{"text_size":"small","theme":"contrast","theme_night":"ember"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if d := c.Display; d.TextSize != "small" || d.Theme != "contrast" || d.ThemeNight != "ember" {
		t.Errorf("kept = %q %q %q", d.TextSize, d.Theme, d.ThemeNight)
	}
}

func TestShellScripts(t *testing.T) {
	cases := []struct {
		cmd  []string
		want []string
	}{
		{[]string{"sh", "-c", "echo {param}"}, []string{"echo {param}"}},
		{[]string{"/bin/bash", "-ec", "x"}, []string{"x"}},
		{[]string{"bash", "-lc", "x"}, []string{"x"}},
		{[]string{"bash", "-o", "pipefail", "-c", "x"}, []string{"x"}},
		// Wrappers are not parsed: any argument named like a shell starts one.
		{[]string{"env", "-u", "X", "sh", "-c", "x {param}"}, []string{"x {param}"}},
		{[]string{"nice", "-n", "5", "bash", "-c", "x"}, []string{"x"}},
		{[]string{"timeout", "5", "sh", "-c", "x"}, []string{"x"}},
		{[]string{"sudo", "-u", "admin", "/bin/sh", "-c", "x"}, []string{"x"}},
		{[]string{"docker", "exec", "web", "sh", "-c", "x"}, []string{"x"}},
		{[]string{"busybox", "sh", "-c", "x"}, []string{"x"}},
		// No inline script.
		{[]string{"sh", "./restart.sh", "-c", "{param}"}, nil},
		{[]string{"bash", "-o", "pipefail", "./restart.sh", "{param}"}, nil},
		{[]string{"sh", "-c"}, nil},
		{[]string{"sh", "--norc", "./x.sh"}, nil},
		{[]string{"ping", "-c", "3", "{param}"}, nil},
		{[]string{"curl", "-c", "/tmp/jar-{param}", "http://nas"}, nil},
		{[]string{"shellcheck", "-c", "{param}"}, nil},
		{nil, nil},
	}
	for _, c := range cases {
		if got := shellScripts(c.cmd); !reflect.DeepEqual(got, c.want) {
			t.Errorf("shellScripts(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}
}

// {param} in a shell -c script is refused behind any wrapper; non-shell
// commands with a -c flag of their own keep it as a plain argument.
func TestActionParamInWrappedShell(t *testing.T) {
	cases := []struct {
		cmd     string
		refused bool
	}{
		{`["env","-u","X","sh","-c","echo {param}"]`, true},
		{`["nice","-n","5","bash","-c","echo {param}"]`, true},
		{`["timeout","5","sh","-c","echo {param}"]`, true},
		{`["sudo","sh","-c","echo {param}"]`, true},
		{`["sudo","-u","admin","bash","-lc","echo {param}"]`, true},
		{`["docker","exec","web","sh","-c","kill {param}"]`, true},
		{`["timeout","5","sh","-c","docker restart \"$1\"","sh","{param}"]`, false},
		{`["ping","-c","{param}","nas"]`, false},
		{`["curl","-c","/tmp/jar","http://nas/{param}"]`, false},
	}
	for _, c := range cases {
		_, err := loadCfg(t, `{"actions":[{"name":"a","type":"exec","command":`+c.cmd+`,"param":{"values":["1"]}}]}`)
		if got := err != nil && strings.Contains(err.Error(), "shell -c script"); got != c.refused || !c.refused && err != nil {
			t.Errorf("%s: err = %v, want refused=%v", c.cmd, err, c.refused)
		}
	}
}
