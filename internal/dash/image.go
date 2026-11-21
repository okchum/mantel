package dash

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"strings"
	"sync"
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/httpx"
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

// maxImage caps one upstream picture; a longer one is an error, never a
// truncated image cached for the widget's whole refresh.
const maxImage = 8 << 20

func fetchImage(ctx context.Context, w config.Widget) (cachedImage, error) {
	resp, err := httpx.Do(ctx, nil, httpx.Request{URL: w.URL, Headers: w.Headers})
	if err != nil {
		return cachedImage{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	ct := resp.Header.Get("Content-Type")
	if !allowedImage(ct) {
		return cachedImage{}, fmt.Errorf("not a raster image: %q", ct)
	}
	body, err := httpx.ReadAll(resp.Body, maxImage)
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
