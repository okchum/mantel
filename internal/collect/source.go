package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/httpx"
	"github.com/okchum/mantel/internal/jpath"
	"github.com/okchum/mantel/internal/state"
	"github.com/okchum/mantel/internal/textutil"
)

// Source polls one generic http_json or exec source into its store slot.
// Push sources have nothing to poll; see Push.
func Source(ctx context.Context, st *state.Store, src config.Source) {
	var fetch func(context.Context) (any, error)
	switch src.Type {
	case "http_json":
		client := NewClient(ClientOpts{Timeout: src.Timeout.Or(15 * time.Second), Insecure: src.Insecure})
		fetch = func(ctx context.Context) (any, error) { return fetchHTTP(ctx, client, src) }
	case "exec":
		fetch = func(ctx context.Context) (any, error) {
			return runExec(ctx, src.Command, src.Dir, src.Env, src.Timeout.Or(30*time.Second))
		}
	default:
		return
	}
	every(ctx, src.Name, src.Interval.Or(time.Minute), func() {
		v, err := fetch(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil && src.Select != "" {
			v, err = jpath.Get(v, src.Select)
		}
		st.Update(func(s *state.Snapshot) {
			i := s.SourceIndex(src.Name)
			if i < 0 {
				return
			}
			out := &s.Sources[i]
			if err != nil {
				out.Error = shortErr(err).Error()
				return
			}
			out.Data, out.Error, out.Updated = v, "", time.Now()
		})
	})
}

func fetchHTTP(ctx context.Context, client *http.Client, src config.Source) (any, error) {
	// Too long is an error: a cut-off JSON body would be stored as text.
	b, err := httpx.Get(ctx, client, httpx.Request{
		Method: src.Method, URL: src.URL, Body: src.Body,
		Accept: "application/json", Headers: src.Headers,
	}, maxBody)
	if err != nil {
		return nil, err
	}
	return ParseOutput(b), nil
}

// runExec runs a command (no shell) and parses its stdout.
func runExec(ctx context.Context, command []string, dir string, env map[string]string, timeout time.Duration) (any, error) {
	out, err := RunCommand(ctx, command, dir, env, timeout)
	if err != nil {
		return nil, err
	}
	return ParseOutput(out), nil
}

// RunCommand runs command with a timeout; the error carries the tail of stderr.
func RunCommand(ctx context.Context, command []string, dir string, env map[string]string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	killGroup(cmd)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	out := &limitWriter{w: &stdout, n: maxBody}
	cmd.Stdout = out
	cmd.Stderr = &limitWriter{w: &stderr, n: 64 << 10}
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return stdout.Bytes(), fmt.Errorf("timed out after %s", timeout)
	}
	if err != nil {
		msg := lastLine(stderr.String())
		if msg == "" {
			msg = lastLine(stdout.String())
		}
		if msg != "" {
			return stdout.Bytes(), fmt.Errorf("%w: %s", err, textutil.Clip(msg, 160))
		}
		return stdout.Bytes(), err
	}
	// Like an oversize HTTP body: cut-off output would parse as something else.
	if out.over {
		return stdout.Bytes(), fmt.Errorf("%w (stdout over %d MiB)", httpx.ErrTooBig, maxBody>>20)
	}
	return stdout.Bytes(), nil
}

// ParseOutput turns command or HTTP output into a document: JSON as is, a bare
// number into {"value": n}, JSON lines into {"items": [...]}, other text into {"text": "..."}.
func ParseOutput(b []byte) any {
	t := bytes.TrimSpace(b)
	var v any
	if json.Unmarshal(t, &v) == nil {
		if f, ok := v.(float64); ok {
			return map[string]any{"value": f}
		}
		return v
	}
	// "NaN"/"Inf" parse as floats but would break JSON encoding of the state.
	if f, err := strconv.ParseFloat(string(t), 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
		return map[string]any{"value": f}
	}
	// One JSON value per line (docker ps --format '{{json .}}', jq -c):
	// a list under "items".
	if lines := bytes.Split(t, []byte("\n")); len(lines) > 1 {
		items := make([]any, 0, len(lines))
		for _, l := range lines {
			if l = bytes.TrimSpace(l); len(l) == 0 {
				continue
			}
			var x any
			if json.Unmarshal(l, &x) != nil {
				items = nil
				break
			}
			items = append(items, x)
		}
		if items != nil {
			return map[string]any{"items": items}
		}
	}
	return map[string]any{"text": string(t)}
}

// Push stores a document sent to POST /api/v1/push/<name>. With merge, top-level
// keys are merged into the previous object instead of replacing it.
func Push(st *state.Store, name string, doc any, merge bool) error {
	var err error
	st.Update(func(s *state.Snapshot) {
		i := s.SourceIndex(name)
		if i < 0 {
			err = fmt.Errorf("no source %q", name)
			return
		}
		out := &s.Sources[i]
		if m, ok := doc.(map[string]any); ok && merge {
			if old, ok := out.Data.(map[string]any); ok {
				nm := make(map[string]any, len(old)+len(m))
				for k, v := range old {
					nm[k] = v
				}
				for k, v := range m {
					nm[k] = v
				}
				doc = nm
			}
		}
		out.Data, out.Error, out.Updated = doc, "", time.Now()
	})
	return err
}

// limitWriter keeps the first n bytes and swallows the rest (so the child is
// never blocked on a full pipe), noting in over that it dropped some.
type limitWriter struct {
	w    io.Writer
	n    int
	over bool
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		l.over = l.over || len(p) > 0
		return len(p), nil
	}
	q := p
	if len(q) > l.n {
		q = q[:l.n]
		l.over = true
	}
	l.n -= len(q)
	_, err := l.w.Write(q)
	return len(p), err
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}
