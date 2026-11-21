package collect

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/state"
)

// Probe checks one HTTP endpoint on an interval into store slot idx.
func Probe(ctx context.Context, st *state.Store, idx int, p config.Probe) {
	client := NewClient(ClientOpts{
		Timeout:     p.Timeout.Or(8 * time.Second),
		Insecure:    p.Insecure,
		NoKeepAlive: true, // measure a full connect each time
		NoRedirect:  true,
	})
	every(ctx, "probe:"+p.Name, p.Interval.Or(30*time.Second), func() {
		status, latency, certEnd, err := checkOnce(ctx, client, p)
		if ctx.Err() != nil {
			return
		}
		st.Update(func(s *state.Snapshot) {
			out := &s.Probes[idx]
			wasUp, checked := out.Up, out.Checked
			out.Name, out.URL, out.Checked = p.Name, p.URL, true
			out.Status, out.At = status, time.Now()
			// Only report what this check saw: a failed check or a plain
			// http:// URL must not keep showing an old certificate's expiry.
			out.CertExpires = nil
			if !certEnd.IsZero() {
				out.CertExpires = &certEnd
			}
			up := err == nil
			if err != nil {
				out.Error = err.Error()
				out.Hist = state.Push(out.Hist, -1, histLen)
			} else {
				out.Error = ""
				out.LatencyMS = float64(latency.Microseconds()) / 1000
				out.Hist = state.Push(out.Hist, out.LatencyMS, histLen)
			}
			out.Up = up
			if !checked || wasUp != up {
				out.Since = time.Now()
			}
		})
	})
}

func checkOnce(ctx context.Context, client *http.Client, p config.Probe) (status int, latency time.Duration, certEnd time.Time, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return 0, 0, certEnd, err
	}
	req.Header.Set("User-Agent", "github.com/okchum/mantel/1 (uptime probe)")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, certEnd, shortErr(err)
	}
	latency = time.Since(start)
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		certEnd = resp.TLS.PeerCertificates[0].NotAfter
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	ok := resp.StatusCode < 400
	if p.Expect != 0 {
		ok = resp.StatusCode == p.Expect
	}
	if !ok {
		return resp.StatusCode, latency, certEnd, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, latency, certEnd, nil
}
