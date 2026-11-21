package plans

import (
	"context"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCAFileTrustsPrivateCA(t *testing.T) {
	now := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)
	f := &fakeCPA{t: t, now: now, atLimit: true, resetCode: 200, result: "reset", codexCode: "reset"}
	srv := newTLS(t, f)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	try := func(caFile string, insecure bool) error {
		conf, err := TLSConfig(caFile, insecure)
		if err != nil {
			t.Fatal(err)
		}
		s := &Service{Backend: NewCPA(srv.URL+"/v8/management/", "mgmt-key", conf), Now: func() time.Time { return now }}
		_, err = s.Refresh(context.Background())
		return err
	}
	if err := try("", false); err == nil {
		t.Fatal("unknown CA must be refused by default")
	}
	if err := try(ca, false); err != nil {
		t.Fatalf("ca_file: %v", err)
	}
	if err := try("", true); err != nil {
		t.Fatalf("insecure: %v", err)
	}
	if _, err := TLSConfig(filepath.Join(t.TempDir(), "missing.pem"), false); err == nil {
		t.Fatal("missing ca_file must fail")
	}
}

func newTLS(t *testing.T, f *fakeCPA) *httptest.Server {
	srv := httptest.NewTLSServer(f.handler())
	t.Cleanup(srv.Close)
	return srv
}
