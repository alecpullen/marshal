package app

import (
	"container/list"
	"strings"
	"sync"
)

const (
	workOutputCacheEntries = 64
	workOutputCacheBytes   = 4 << 20
)

// workOutputCache owns only derived terminal output, never lifecycle/action
// state. Result revisions are not output hashes: corrections with the same
// revision must still compare the exact raw input. Reconciles may overlap.
type workOutputCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     list.List
	used    int
}

type workOutputEntry struct {
	key, revision, raw, clean string
}

func (e workOutputEntry) size() int {
	return len(e.key) + len(e.revision) + len(e.raw) + len(e.clean)
}

func (c *workOutputCache) clean(key, revision, raw string) string {
	c.mu.Lock()
	if el := c.entries[key]; el != nil {
		e := el.Value.(workOutputEntry)
		if e.revision == revision && e.raw == raw {
			c.lru.MoveToFront(el)
			c.mu.Unlock()
			return e.clean
		}
	}
	c.mu.Unlock()

	// Redaction can be expensive. Do it without holding source/coordinator or
	// cache locks. A late insert from an older snapshot only causes a future
	// miss: every lookup validates the caller's exact input again.
	clean := cleanWorkTail(raw)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el := c.entries[key]; el != nil {
		c.remove(el)
	}
	// Subtract rather than sum untrusted lengths before the admission check.
	remaining := workOutputCacheBytes
	for _, s := range []string{key, revision, raw, clean} {
		if len(s) > remaining {
			return clean
		}
		remaining -= len(s)
	}
	// Own each backing allocation: a tiny substring must not pin a huge log.
	e := workOutputEntry{strings.Clone(key), strings.Clone(revision), strings.Clone(raw), strings.Clone(clean)}
	for c.lru.Len() >= workOutputCacheEntries || c.used+e.size() > workOutputCacheBytes {
		c.remove(c.lru.Back())
	}
	if c.entries == nil {
		c.entries = make(map[string]*list.Element)
	}
	c.entries[e.key] = c.lru.PushFront(e)
	c.used += e.size()
	return clean
}

func (c *workOutputCache) remove(el *list.Element) {
	e := el.Value.(workOutputEntry)
	delete(c.entries, e.key)
	c.used -= e.size()
	c.lru.Remove(el)
}

func (c *workOutputCache) prune(present map[string]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, el := range c.entries {
		if _, ok := present[key]; !ok {
			c.remove(el)
		}
	}
}

func (wc *workCoordinator) commandOutputTail(key, revision, raw, state string) string {
	if !workItemTerminal(state) {
		return cleanWorkTail(raw)
	}
	return wc.outputCache.clean(key, revision, raw)
}
