package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCapReader(t *testing.T) {
	b, err := io.ReadAll(CapReader(strings.NewReader("hello"), 5))
	if err != nil || string(b) != "hello" {
		t.Fatalf("%q %v", b, err)
	}
	_, err = io.ReadAll(CapReader(strings.NewReader("hello!"), 5))
	if !errors.Is(err, ErrTooBig) {
		t.Fatalf("want ErrTooBig, got %v", err)
	}
	if b, err := ReadAll(strings.NewReader("hello!"), 5); !errors.Is(err, ErrTooBig) || b != nil {
		t.Fatalf("ReadAll must not return a cut-off body: %q %v", b, err)
	}
}

func TestStatusError(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", &StatusError{Code: 403, Body: "no"})
	if Status(err) != 403 || err.Error() != "wrapped: HTTP 403 no" {
		t.Fatalf("%v → %d", err, Status(err))
	}
	if (&StatusError{Code: 502}).Error() != "HTTP 502" {
		t.Fatal("no body, no trailing space")
	}
	if Status(errors.New("x")) != 0 || Status(nil) != 0 {
		t.Fatal("non-status errors have no code")
	}
}

func TestDo(t *testing.T) {
	var got *http.Request
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		switch r.URL.Path {
		case "/teapot":
			w.WriteHeader(http.StatusTeapot)
			_, _ = io.WriteString(w, "  short and stout\n")
		default:
			_, _ = io.WriteString(w, "ok")
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	b, err := Get(ctx, srv.Client(), Request{
		Method: http.MethodPost, URL: srv.URL, Body: `{"a":1}`, Accept: "application/json",
		Headers: map[string]string{"X-Token": "t"},
	}, 10)
	if err != nil || string(b) != "ok" {
		t.Fatalf("%q %v", b, err)
	}
	if got.Method != http.MethodPost || gotBody != `{"a":1}` || got.Header.Get("User-Agent") != UserAgent ||
		got.Header.Get("Accept") != "application/json" || got.Header.Get("Content-Type") != "application/json" ||
		got.Header.Get("X-Token") != "t" {
		t.Fatalf("request: %s %q %v", got.Method, gotBody, got.Header)
	}

	// Configured headers win over the defaults.
	if _, err := Get(ctx, srv.Client(), Request{URL: srv.URL, Headers: map[string]string{"User-Agent": "me"}}, 10); err != nil || got.Header.Get("User-Agent") != "me" {
		t.Fatalf("UA override: %v %q", err, got.Header.Get("User-Agent"))
	}
	if got.Method != http.MethodGet || got.Header.Get("Content-Type") != "" || got.Header.Get("Accept") != "" {
		t.Fatalf("plain GET: %s %v", got.Method, got.Header)
	}

	_, err = Get(ctx, srv.Client(), Request{URL: srv.URL + "/teapot"}, 10)
	if Status(err) != http.StatusTeapot || err.Error() != "HTTP 418 short and stout" {
		t.Fatalf("status: %v", err)
	}
	// OK widens what counts as success.
	resp, err := Do(ctx, srv.Client(), Request{URL: srv.URL + "/teapot", OK: func(int) bool { return true }})
	if err != nil || resp.StatusCode != http.StatusTeapot {
		t.Fatalf("OK: %v", err)
	}
	_ = resp.Body.Close()

	if _, err := Get(ctx, srv.Client(), Request{URL: srv.URL}, 1); !errors.Is(err, ErrTooBig) {
		t.Fatalf("want ErrTooBig, got %v", err)
	}
}

func TestDoDropsURLFromTransportErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL + "/?token=secret"
	srv.Close()
	_, err := Do(context.Background(), nil, Request{URL: u})
	var ue *url.Error
	if err == nil || errors.As(err, &ue) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("transport error must not quote the URL: %v", err)
	}
}
