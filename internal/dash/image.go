package dash

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/okchum/mantel/internal/config"
)

// imageCache holds the last fetched picture per image widget, so several
// displays polling the same camera cause one upstream request. Concurrent
// misses for the same widget share one fetch (inflight).
type imageCache struct {
	mu       sync.Mutex
	m        map[string]cachedImage
	inflight map[string]*imageCall
}

type cachedImage struct {
	body  []byte
	ctype string
	at    time.Time
}

// imageCall is one upstream fetch; img and err are set before done closes.
type imageCall struct {
	done chan struct{}
	img  cachedImage
	err  error
}

func newImageCache() *imageCache {
	return &imageCache{m: map[string]cachedImage{}, inflight: map[string]*imageCall{}}
}

var ErrNoWidget = errors.New("no such image widget")

// imageTimeout bounds one upstream picture fetch.
const imageTimeout = 15 * time.Second

// Image fetches (or serves from cache) the picture of image widget id. The
// server does the fetching so LAN cameras and credentials work from anywhere.
// SVG is refused: served from Mantel's origin it could run script.
func (d *Dash) Image(ctx context.Context, id string) ([]byte, string, error) {
	w, ok := d.Widget(id)
	if !ok || w.Type != "image" {
		return nil, "", ErrNoWidget
	}
	fresh := w.Refresh.Or(time.Minute) / 2
	c := d.images
	c.mu.Lock()
	if img, ok := c.m[id]; ok && time.Since(img.at) < fresh {
		c.mu.Unlock()
		return img.body, img.ctype, nil
	}
	call, ok := c.inflight[id]
	if !ok {
		call = &imageCall{done: make(chan struct{})}
		c.inflight[id] = call
		// Detached from this request: other waiters share the result, and
		// one display going away must not fail the fetch for the rest.
		go c.fetch(context.WithoutCancel(ctx), w, call)
	}
	c.mu.Unlock()
	select {
	case <-call.done:
		return call.img.body, call.img.ctype, call.err
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
}

func (c *imageCache) fetch(ctx context.Context, w config.Widget, call *imageCall) {
	ctx, cancel := context.WithTimeout(ctx, imageTimeout)
	defer cancel()
	call.img, call.err = fetchImage(ctx, w)
	c.mu.Lock()
	delete(c.inflight, w.ID)
	if call.err == nil {
		c.m[w.ID] = call.img
	}
	c.mu.Unlock()
	close(call.done)
}

func fetchImage(ctx context.Context, w config.Widget) (cachedImage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.URL, nil)
	if err != nil {
		return cachedImage{}, err
	}
	for k, v := range w.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// *url.Error quotes the URL, query string (camera tokens) included.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return cachedImage{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return cachedImage{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !allowedImage(ct) {
		return cachedImage{}, fmt.Errorf("not a raster image: %q", ct)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return cachedImage{}, err
	}
	return cachedImage{body, ct, time.Now()}, nil
}

// allowedImage accepts image/* content types except SVG, which is a document
// that can carry script.
func allowedImage(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return strings.HasPrefix(mt, "image/") && !strings.Contains(mt, "svg")
}
