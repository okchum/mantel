package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExampleLoads(t *testing.T) {
	for _, k := range []string{"MANTEL_TOKEN_NESTHUB", "MANTEL_TOKEN_PHONE", "MANTEL_TOKEN_EDGE", "MANTEL_TOKEN_TV"} {
		t.Setenv(k, "example-token-"+strings.ToLower(k))
	}
	t.Setenv("BARK_KEY", "bark-key")
	t.Setenv("CPA_MANAGEMENT_KEY", "cpa-key")
	c, err := Load("../../config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if c.StateFile != "state.json" || c.Quotas[0].AlertBelow != 10 {
		t.Fatalf("unexpected: %+v", c)
	}
}

func load(t *testing.T, js string) error {
	p := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(p, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	return err
}

func TestTokensAndAlertsValidation(t *testing.T) {
	t.Setenv("GOOD_TOKEN", "0123456789abcdef0123")
	t.Setenv("BARK_KEY", "abc")
	cases := []struct{ js, wantErr string }{
		{`{"tokens":[{"token":"$GOOD_TOKEN"}],"alerts":{"bark":{"key":"$BARK_KEY"}}}`, ""},
		{`{"tokens":[{"name":"x","token":"$UNSET_TOKEN_VAR"}]}`, "shorter than 16"},
		{`{"tokens":[{"token":"$GOOD_TOKEN"},{"token":"0123456789abcdef0123"}]}`, "duplicate"},
		{`{"alerts":{"bark":{"key":"$UNSET_BARK_VAR"}}}`, "alerts.bark.key is empty"},
		{`{"allowed_hosts":["dash.example.com","*.ts.net","Box.Example.org."]}`, ""},
		{`{"allowed_hosts":["https://dash.example.com"]}`, "allowed_hosts[0]"},
		{`{"allowed_hosts":["dash.example.com:8080"]}`, "no scheme, port or path"},
		{`{"allowed_hosts":["*."]}`, "allowed_hosts[0]"},
		{`{"allowed_hosts":["*.com"]}`, "top-level domain"},
		{`{"allowed_hosts":["intranet"]}`, ""},
		{`{"alerts":{"maintenance":["04:55-05:20","23:50-00:10"]}}`, ""},
		{`{"alerts":{"maintenance":["5:00"]}}`, "alerts.maintenance[0]"},
		{`{"alerts":{"maintenance":["05:00-05:00"]}}`, "empty window"},
	}
	for _, c := range cases {
		err := load(t, c.js)
		if c.wantErr == "" && err != nil || c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: err = %v, want %q", c.js, err, c.wantErr)
		}
	}
}
