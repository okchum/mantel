package config

import (
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/okchum/mantel-kit/conf"
)

// Plans watches subscription usage windows (Claude Pro/Max, ChatGPT Plus/Pro
// for Codex) and can spend the account's saved limit resets. The logins live
// in a CLIProxyAPI; this program only holds its management key.
type Plans struct {
	CLIProxy *CLIProxy `json:"cliproxy"`
	Interval Duration  `json:"interval"` // default 2m
	// Accounts limits the list to credentials whose name, email or label
	// contains one of these. Empty = every Claude and Codex login.
	Accounts []string `json:"accounts"`
	// AlertAt pushes an alert when a window passes this percent while the
	// account still has a reset to spend. Default 90; negative turns it off.
	AlertAt float64 `json:"alert_at"`
	// ExpiryAlert warns this long before a saved reset expires unused.
	// Default 24h; "0s" turns it off.
	ExpiryAlert *Duration `json:"expiry_alert"`
	// Reset offers the reset button (hold to confirm). Default true.
	Reset *bool `json:"reset"`
}

type CLIProxy struct {
	URL string `json:"url"` // e.g. "https://cpa.example.com"
	Key string `json:"key"` // management key; "$ENV_NAME" reads the environment
	// CAFile is a PEM bundle trusted in addition to the system roots, for a
	// CLIProxyAPI behind a private CA or self-signed certificate.
	CAFile string `json:"ca_file"`
	// Insecure skips TLS verification entirely. The management key then
	// travels over an unverified connection; prefer CAFile.
	Insecure bool `json:"insecure"`
}

const (
	// PlanSource is the source the plans collector fills.
	PlanSource = "plans"
	// PlanResetAction is the action behind every plan's reset button.
	PlanResetAction = "plan-reset"
	// PlanKeyPattern is what a plan account key looks like ("claude:<name>").
	PlanKeyPattern = `(claude|codex):[A-Za-z0-9@._+=-]{1,160}`
)

// ResetEnabled reports whether the reset button is offered.
func (p *Plans) ResetEnabled() bool { return p != nil && (p.Reset == nil || *p.Reset) }

// ExpiryWarning is how long before expiry a saved reset is announced (0 = off).
func (p *Plans) ExpiryWarning() time.Duration {
	if p.ExpiryAlert == nil {
		return 24 * time.Hour
	}
	return p.ExpiryAlert.Duration
}

func (p *Plans) normalize() error {
	if p.CLIProxy == nil || strings.TrimSpace(p.CLIProxy.URL) == "" {
		return errors.New("plans: cliproxy.url is required")
	}
	p.CLIProxy.URL = conf.ExpandEnv(strings.TrimSpace(p.CLIProxy.URL))
	p.CLIProxy.Key = conf.ExpandEnv(p.CLIProxy.Key)
	if p.CLIProxy.Key == "" {
		return errors.New("plans: cliproxy.key (management key) is required")
	}
	if p.CLIProxy.CAFile != "" {
		if p.CLIProxy.Insecure {
			return errors.New("plans: set cliproxy.ca_file or cliproxy.insecure, not both")
		}
		p.CLIProxy.CAFile = conf.ExpandEnv(p.CLIProxy.CAFile)
		if _, err := CertPool(p.CLIProxy.CAFile); err != nil {
			return fmt.Errorf("plans: cliproxy.ca_file: %w", err)
		}
	}
	if p.AlertAt == 0 {
		p.AlertAt = 90
	}
	if p.AlertAt > 100 {
		return errors.New("plans: alert_at is a percent (0-100)")
	}
	return nil
}

// CertPool returns the system roots plus the PEM certificates in file.
func CertPool(file string) (*x509.CertPool, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("%s: no PEM certificates found", file)
	}
	return pool, nil
}
