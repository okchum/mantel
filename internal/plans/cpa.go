package plans

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/textutil"
)

// CPA is a Backend that asks CLIProxyAPI (v8 management API) to make each
// upstream call with the stored login. CLIProxyAPI swaps "$TOKEN$" for the
// account's access token and refreshes it when needed, so the official
// credentials never leave it.
type CPA struct {
	URL    string // base, e.g. "https://cpa.example.com"
	Key    string // management key
	Client *http.Client
}

// NewCPA normalises the base URL (a pasted ".../v8/management" is fine).
// tlsConf, if not nil, replaces the default TLS settings (see TLSConfig).
func NewCPA(base, key string, tlsConf *tls.Config) *CPA {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	for _, s := range []string{"/v8/management", "/v0/management", "/management.html"} {
		base = strings.TrimSuffix(base, s)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if tlsConf != nil {
		tr.TLSClientConfig = tlsConf
	}
	return &CPA{URL: base, Key: key, Client: &http.Client{Transport: tr, Timeout: 40 * time.Second}}
}

// TLSConfig builds the TLS settings for a CLIProxyAPI: extra trusted roots
// from caFile, or no verification at all when insecure. Nil means defaults.
func TLSConfig(caFile string, insecure bool) (*tls.Config, error) {
	switch {
	case caFile != "":
		pool, err := config.CertPool(caFile)
		if err != nil {
			return nil, err
		}
		return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
	case insecure:
		return &tls.Config{InsecureSkipVerify: true}, nil //nolint:gosec // explicit opt-in, warned about at startup
	}
	return nil, nil
}

func (p *CPA) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.URL+"/v8/management"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.Key)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return fmt.Errorf("CLIProxyAPI 连不上: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &e)
		msg := textutil.Clip(cmpOr(e.Error, e.Message, string(b)), 120)
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return fmt.Errorf("CLIProxyAPI 拒绝了管理密钥（HTTP %d %s）", resp.StatusCode, msg)
		}
		return fmt.Errorf("CLIProxyAPI HTTP %d: %s", resp.StatusCode, msg)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

func (p *CPA) Credentials(ctx context.Context) ([]Cred, error) {
	var doc struct {
		Files []map[string]any `json:"files"`
	}
	if err := p.do(ctx, "GET", "/credentials", nil, &doc); err != nil {
		return nil, err
	}
	var out []Cred
	for _, f := range doc.Files {
		c := Cred{
			ID:       str(f["auth_index"]),
			Name:     str(f["name"]),
			Provider: strings.ToLower(cmpOr(str(f["provider"]), str(f["type"]))),
			Email:    str(f["email"]),
			Label:    cmpOr(str(f["label"]), str(f["account"])),
			Disabled: f["disabled"] == true,
		}
		c.Name = cmpOr(c.Name, str(f["id"]), c.ID)
		claims := tokenClaims(f["id_token"])
		if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
			for k, v := range auth {
				if _, has := claims[k]; !has {
					claims[k] = v
				}
			}
		}
		c.AccountID = cmpOr(str(f["chatgpt_account_id"]), str(claims["chatgpt_account_id"]))
		c.Plan = planName(cmpOr(str(claims["plan_type"]), str(claims["chatgpt_plan_type"])))
		if c.Email == "" {
			c.Email = str(claims["email"])
		}
		if c.ID == "" {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// tokenClaims accepts the decoded claims CLIProxyAPI lists, or a raw JWT.
func tokenClaims(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return x
	case string:
		parts := strings.Split(x, ".")
		if len(parts) < 2 {
			return map[string]any{}
		}
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
		if err != nil {
			return map[string]any{}
		}
		var m map[string]any
		if json.Unmarshal(b, &m) == nil && m != nil {
			return m
		}
	}
	return map[string]any{}
}

func (p *CPA) Call(ctx context.Context, c Cred, r Request) (Response, error) {
	in := map[string]any{"auth_index": c.ID, "method": r.Method, "url": r.URL, "header": r.Header}
	if r.Body != "" {
		in["data"] = r.Body
	}
	var out struct {
		StatusCode int             `json:"status_code"`
		Body       json.RawMessage `json:"body"`
	}
	if err := p.do(ctx, "POST", "/requests/api-call", in, &out); err != nil {
		return Response{}, err
	}
	if out.StatusCode == 0 {
		return Response{}, errors.New("CLIProxyAPI 没有返回上游状态")
	}
	body := []byte(out.Body)
	// The upstream body comes back as a JSON string (or, in some versions, inline).
	var s string
	if json.Unmarshal(out.Body, &s) == nil {
		body = []byte(s)
	}
	return Response{Status: out.StatusCode, Body: body}, nil
}

func (p *CPA) ClearCooldown(ctx context.Context, c Cred) error {
	return p.do(ctx, "POST", "/routing/cooldown/reset", map[string]string{"auth_index": c.ID}, nil)
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return fmt.Sprintf("%.0f", x)
	case json.Number:
		return x.String()
	}
	return ""
}
