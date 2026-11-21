package dash

import (
	"maps"
	"sync"

	"github.com/okchum/mantel/internal/jpath"
	"github.com/okchum/mantel/internal/state"
)

// normCache keeps the JSON-normalized built-in records (servers, probes,
// quotas, weather, agenda) of one store generation: every screen polls the
// dashboard every couple of seconds, while the data changes far less often.
// Cached documents are shared between requests and must not be modified.
type normCache struct {
	mu  sync.Mutex
	gen uint64
	m   map[string]any
}

// normalize returns jpath.Normalize(v), reusing the result for the same key
// while the snapshot's store generation is unchanged. Snapshots that did not
// come from a store (Gen 0) are never cached.
func (d *Dash) normalize(snap state.Snapshot, key string, v any) any {
	if snap.Gen == 0 {
		return jpath.Normalize(v)
	}
	if snap.Redacted {
		key += "\x00viewer"
	}
	c := &d.norm
	c.mu.Lock()
	if c.gen == snap.Gen {
		if doc, ok := c.m[key]; ok {
			c.mu.Unlock()
			return doc
		}
	}
	c.mu.Unlock()
	doc := jpath.Normalize(v)
	c.mu.Lock()
	if snap.Gen > c.gen {
		c.gen, c.m = snap.Gen, map[string]any{}
	}
	if snap.Gen == c.gen {
		c.m[key] = doc
	}
	c.mu.Unlock()
	return doc
}

// withActions is a record's normalized document plus the actions its row
// offers. The cached map is copied before adding to it.
func (d *Dash) withActions(snap state.Snapshot, key string, v any, actions []string) any {
	m, _ := d.normalize(snap, key, v).(map[string]any)
	if m != nil && len(actions) > 0 {
		m = maps.Clone(m)
		m["actions"] = actions
	}
	return m
}
