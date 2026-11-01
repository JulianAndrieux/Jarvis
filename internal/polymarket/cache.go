package polymarket

import (
	"context"
	"sync"
	"time"
)

// DefaultCacheTTL : assez court pour que les prix affichés restent
// actuels, assez long pour que la barre latérale (rafraîchie toutes les
// 15 s) n'interroge pas Polymarket à chaque fois.
const DefaultCacheTTL = 60 * time.Second

// CachedClient garde les réponses de Client pendant TTL. Les erreurs ne
// sont jamais gardées. Sûr en concurrence.
//
// Ce cache sert l'affichage. Le modèle de risque et l'envoi d'un ordre
// (jalons 54-55) liront le carnet sans cache : un prix vieux d'une minute
// suffit pour regarder, pas pour décider.
type CachedClient struct {
	Client Client
	TTL    time.Duration
	Now    func() time.Time

	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	at    time.Time
	value any
	ok    bool
}

func (c *CachedClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *CachedClient) ttl() time.Duration {
	if c.TTL <= 0 {
		return DefaultCacheTTL
	}
	return c.TTL
}

func (c *CachedClient) lookup(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || c.now().Sub(e.at) >= c.ttl() {
		return cacheEntry{}, false
	}
	return e, true
}

func (c *CachedClient) store(key string, value any, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]cacheEntry{}
	}
	now := c.now()
	for k, e := range c.entries {
		if now.Sub(e.at) >= c.ttl() {
			delete(c.entries, k)
		}
	}
	c.entries[key] = cacheEntry{at: now, value: value, ok: ok}
}

func (c *CachedClient) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *CachedClient) Search(ctx context.Context, query string) ([]Event, error) {
	key := "s\x00" + query
	if e, ok := c.lookup(key); ok {
		return e.value.([]Event), nil
	}
	v, err := c.Client.Search(ctx, query)
	if err != nil {
		return nil, err
	}
	c.store(key, v, true)
	return v, nil
}

func (c *CachedClient) Market(ctx context.Context, slug string) (Market, bool, error) {
	key := "m\x00" + slug
	if e, ok := c.lookup(key); ok {
		return e.value.(Market), e.ok, nil
	}
	m, ok, err := c.Client.Market(ctx, slug)
	if err != nil {
		return Market{}, false, err
	}
	c.store(key, m, ok)
	return m, ok, nil
}

func (c *CachedClient) Book(ctx context.Context, tokenID string) (Book, error) {
	key := "b\x00" + tokenID
	if e, ok := c.lookup(key); ok {
		return e.value.(Book), nil
	}
	b, err := c.Client.Book(ctx, tokenID)
	if err != nil {
		return Book{}, err
	}
	c.store(key, b, true)
	return b, nil
}

func (c *CachedClient) History(ctx context.Context, tokenID string, iv Interval) ([]PricePoint, error) {
	key := "h\x00" + tokenID + "\x00" + string(iv)
	if e, ok := c.lookup(key); ok {
		return e.value.([]PricePoint), nil
	}
	v, err := c.Client.History(ctx, tokenID, iv)
	if err != nil {
		return nil, err
	}
	c.store(key, v, true)
	return v, nil
}
