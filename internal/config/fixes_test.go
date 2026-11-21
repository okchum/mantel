package config

import (
	"crypto/tls"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKickNamesUnique(t *testing.T) {
	cases := []struct{ name, js, want string }{
		{"two servers same name", `{"servers":[{"url":"http://a:9100/metrics","name":"nas"},{"url":"http://b:9100/metrics","name":"nas"}]}`,
			`server:nas: duplicate name`},
		{"two quotas default name", `{"quotas":[{"provider":"deepseek","api_key":"a"},{"provider":"deepseek","api_key":"b"}]}`,
			`quota:deepseek: duplicate name`},
		{"source shadows weather", `{"weather":{"latitude":1,"longitude":2},"sources":[{"name":"weather","type":"push"}]}`,
			`duplicate name "weather"`},
		{"reserved demo", `{"sources":[{"name":"demo","type":"push"}]}`, `name "demo" is reserved`},
	}
	for _, c := range cases {
		_, err := loadCfg(t, c.js)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", c.name, c.want, err)
		}
	}
	if _, err := loadCfg(t, `{"servers":[{"url":"http://a:9100/metrics"},{"url":"http://b:9100/metrics"}]}`); err != nil {
		t.Errorf("distinct default names must load: %v", err)
	}
}

func TestParamCompiledAtLoad(t *testing.T) {
	c, err := loadCfg(t, `{"actions":[{"name":"x","type":"http","url":"http://h/","param":{"pattern":"[a-z]+"}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	p := c.Actions[0].Param
	if p.re == nil || !p.Allows("abc") || p.Allows("abc1") {
		t.Fatalf("%+v", p)
	}
	if pp := PatternParam(`\d+`); pp.re == nil || !pp.Allows("42") {
		t.Fatal("PatternParam")
	}
}

func BenchmarkParamAllows(b *testing.B) {
	p := PatternParam(`[\p{L}\p{N} ._:/@+=-]{1,80}`)
	for i := 0; i < b.N; i++ {
		p.Allows("some-container_name")
	}
}

func TestCLIProxyCAFile(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	defer srv.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(dir, "junk.pem")
	_ = os.WriteFile(junk, []byte("not a cert"), 0o600)
	plans := func(extra string) string {
		return `{"plans":{"cliproxy":{"url":"https://cpa.local","key":"k"` + extra + `}}}`
	}
	c, err := loadCfg(t, plans(`,"ca_file":"`+ca+`"`))
	if err != nil || c.Plans.CLIProxy.CAFile != ca {
		t.Fatalf("%v %+v", err, c)
	}
	pool, err := CertPool(ca)
	if err != nil {
		t.Fatal(err)
	}
	// The pool really trusts the test server.
	conn, err := tls.Dial("tcp", srv.Listener.Addr().String(), &tls.Config{RootCAs: pool, ServerName: "example.com"})
	if err != nil {
		t.Fatalf("CA from ca_file not trusted: %v", err)
	}
	_ = conn.Close()

	for extra, want := range map[string]string{
		`,"ca_file":"` + junk + `"`:                       "no PEM certificates",
		`,"ca_file":"` + filepath.Join(dir, "nope") + `"`: "ca_file",
		`,"ca_file":"` + ca + `","insecure":true`:         "not both",
	} {
		if _, err := loadCfg(t, plans(extra)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", extra, want, err)
		}
	}
}

func TestActionParamPlacement(t *testing.T) {
	cases := []struct{ action, wantErr string }{
		{`{"name":"a","type":"http","url":"http://nas/restart/{param}?x={param}","param":{"values":["web"]}}`, ""},
		{`{"name":"a","type":"http","url":"http://{param}/restart","param":{"values":["web"]}}`, "scheme or host"},
		{`{"name":"a","type":"http","url":"http://nas:{param}/","param":{"values":["1"]}}`, "scheme or host"},
		{`{"name":"a","type":"http","url":"{param}://nas/","param":{"values":["http"]}}`, "scheme or host"},
		{`{"name":"a","type":"http","url":"http://user:{param}@nas","param":{"values":["x"]}}`, "scheme or host"},
		{`{"name":"a","type":"exec","command":["docker","restart","{param}"],"param":{"values":["web"]}}`, ""},
		{`{"name":"a","type":"exec","command":["sh","-c","docker restart \"$1\"","sh","{param}"],"param":{"values":["web"]}}`, ""},
		{`{"name":"a","type":"exec","command":["bash","./restart.sh","-c","{param}"],"param":{"values":["web"]}}`, ""},
		{`{"name":"a","type":"exec","command":["sh","-c","docker restart {param}"],"param":{"values":["web"]}}`, "shell -c script"},
		{`{"name":"a","type":"exec","command":["/bin/bash","-ec","docker restart {param}"],"param":{"values":["web"]}}`, "shell -c script"},
		{`{"name":"a","type":"exec","command":["zsh","-l","-c","echo {param}"],"param":{"values":["web"]}}`, "shell -c script"},
		{`{"name":"a","type":"exec","command":["bash","-o","pipefail","-c","echo {param}"],"param":{"values":["web"]}}`, "shell -c script"},
		{`{"name":"a","type":"exec","command":["/usr/bin/env","FOO=1","sh","-c","echo {param}"],"param":{"values":["web"]}}`, "shell -c script"},
		{`{"name":"a","type":"exec","command":["busybox","sh","-c","echo {param}"],"param":{"values":["web"]}}`, "shell -c script"},
		{`{"name":"a","type":"exec","command":["env","docker","restart","{param}"],"param":{"values":["web"]}}`, ""},
		{`{"name":"a","type":"exec","command":["bash","-o","pipefail","./restart.sh","{param}"],"param":{"values":["web"]}}`, ""},
	}
	for _, c := range cases {
		_, err := loadCfg(t, `{"actions":[`+c.action+`]}`)
		if c.wantErr == "" && err != nil || c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: err = %v, want %q", c.action, err, c.wantErr)
		}
	}
}

func TestSketchToleratesOutOfGridWidgets(t *testing.T) {
	lines, labels := Sketch([]Widget{{Type: "stat", At: []int{-2, -1, 4, 3}}, {Type: "text", At: []int{10, 6, 5, 5}}}, 12, 8)
	if len(lines) != 8 || len(labels) != 2 || lines[0][:2*len("A")] != "AA" {
		t.Fatalf("%q %q", lines, labels)
	}
}
