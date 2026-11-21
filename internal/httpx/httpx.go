// Package httpx is the one outgoing-request flow shared by collectors,
// actions and the image proxy: build the request, set the User-Agent, check
// the status and read a capped body that fails instead of being cut short.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/okchum/mantel/internal/textutil"
)

// UserAgent identifies Mantel to the services it calls.
const UserAgent = "github.com/okchum/mantel/1"

// ErrTooBig reports a body over its size cap.
var ErrTooBig = errors.New("response too large")

// StatusError is a status the caller did not accept; errors.As lets callers
// branch on Code. Body is the clipped start of the response.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("HTTP %d", e.Code)
	}
	return fmt.Sprintf("HTTP %d %s", e.Code, e.Body)
}

// Status is the HTTP status behind err, or 0 when err is not a *StatusError.
func Status(err error) int {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code
	}
	return 0
}

// Request describes one outgoing request.
type Request struct {
	Method string // "" means GET
	URL    string
	// Body, if not empty, is sent as ContentType ("" means application/json).
	Body        string
	ContentType string
	Accept      string // "" sends no Accept header
	// Headers are set last, so they may override any of the above.
	Headers map[string]string
	// OK reports which statuses are a success; nil accepts any 2xx.
	OK func(code int) bool
}

// Do sends r and returns the response when its status is accepted; the caller
// closes the body. Any other status is a *StatusError quoting the start of the
// body. A transport error loses its *url.Error wrapper, which quotes the URL
// with its query string (tokens included).
func Do(ctx context.Context, c *http.Client, r Request) (*http.Response, error) {
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if r.Body != "" {
		body = strings.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.URL, body)
	if err != nil {
		return nil, unwrapURL(err)
	}
	req.Header.Set("User-Agent", UserAgent)
	if r.Accept != "" {
		req.Header.Set("Accept", r.Accept)
	}
	if r.Body != "" {
		ct := r.ContentType
		if ct == "" {
			ct = "application/json"
		}
		req.Header.Set("Content-Type", ct)
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, unwrapURL(err)
	}
	ok := r.OK
	if ok == nil {
		ok = func(code int) bool { return code/100 == 2 }
	}
	if !ok(resp.StatusCode) {
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &StatusError{Code: resp.StatusCode, Body: textutil.Clip(strings.TrimSpace(string(b)), 120)}
	}
	return resp, nil
}

// Get sends r and reads the whole body of an accepted response, failing with
// ErrTooBig past max bytes.
func Get(ctx context.Context, c *http.Client, r Request, max int64) ([]byte, error) {
	resp, err := Do(ctx, c, r)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return ReadAll(resp.Body, max)
}

func unwrapURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// ReadAll reads r whole, or fails with ErrTooBig past max bytes rather than
// returning a cut-off body that would parse as something else.
func ReadAll(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(CapReader(r, max))
	if err != nil {
		return nil, err
	}
	return b, nil
}

// CapReader reads at most max bytes of r and fails with ErrTooBig, rather
// than silently truncating, when r is longer.
func CapReader(r io.Reader, max int64) io.Reader { return &capReader{r: r, left: max, max: max} }

type capReader struct {
	r         io.Reader
	left, max int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		var one [1]byte
		n, err := c.r.Read(one[:])
		if n > 0 {
			return 0, fmt.Errorf("%w (over %s)", ErrTooBig, size(c.max))
		}
		return 0, err
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

func size(n int64) string {
	if n >= 1<<20 && n%(1<<20) == 0 {
		return fmt.Sprintf("%d MiB", n>>20)
	}
	return fmt.Sprintf("%d bytes", n)
}
