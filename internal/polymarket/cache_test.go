package polymarket

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestFakeClient(t *testing.T) {
	f := NewFakeClient()
	f.Markets["a"] = Market{Slug: "a", Question: "A ?"}
	f.Books["1"] = Book{TokenID: "1", Bids: []Level{{0.4, 1}}}
	f.Histories["1"] = []PricePoint{{Price: 0.4}}
	f.Events = []Event{{Slug: "e", Title: "Élection", Markets: []Market{{Slug: "a"}}}}
	ctx := context.Background()
	if m, ok, err := f.Market(ctx, "a"); !ok || err != nil || m.Question != "A ?" {
		t.Errorf("Market = %+v %v %v", m, ok, err)
	}
	if _, ok, err := f.Market(ctx, "b"); ok || err != nil {
		t.Errorf("Market(absent) = %v %v", ok, err)
	}
	if ev, _ := f.Search(ctx, "élect"); len(ev) != 1 {
		t.Errorf("Search = %d", len(ev))
	}
	if ev, _ := f.Search(ctx, "rien"); len(ev) != 0 {
		t.Errorf("Search(rien) = %d", len(ev))
	}
	if _, err := f.Book(ctx, "2"); err == nil {
		t.Error("Book(absent) doit échouer")
	}
	f.Err = errors.New("hors ligne")
	if _, _, err := f.Market(ctx, "a"); err == nil {
		t.Error("Err doit être rendue")
	}
	if f.Calls() == 0 {
		t.Error("les appels doivent être comptés")
	}
}

func TestCachedClient_ReusesWithinTTLThenRefreshes(t *testing.T) {
	f := NewFakeClient()
	f.Markets["a"] = Market{Slug: "a", Question: "v1"}
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	c := &CachedClient{Client: f, TTL: time.Minute, Now: func() time.Time { return now }}
	ctx := context.Background()

	m, _, _ := c.Market(ctx, "a")
	f.Markets["a"] = Market{Slug: "a", Question: "v2"}
	m2, _, _ := c.Market(ctx, "a")
	if m.Question != "v1" || m2.Question != "v1" || f.Calls() != 1 {
		t.Fatalf("dans le délai : %q %q, %d appels", m.Question, m2.Question, f.Calls())
	}
	now = now.Add(61 * time.Second)
	if m3, _, _ := c.Market(ctx, "a"); m3.Question != "v2" || f.Calls() != 2 {
		t.Errorf("après le délai : %q, %d appels", m3.Question, f.Calls())
	}
}

// Une erreur n'est jamais mise en cache : Polymarket injoignable une fois ne
// doit pas le rester une minute.
func TestCachedClient_DoesNotCacheErrors(t *testing.T) {
	f := NewFakeClient()
	f.Err = errors.New("hors ligne")
	c := &CachedClient{Client: f, TTL: time.Minute}
	ctx := context.Background()
	if _, _, err := c.Market(ctx, "a"); err == nil {
		t.Fatal("veut l'erreur")
	}
	f.Err = nil
	f.Markets["a"] = Market{Slug: "a"}
	if _, ok, err := c.Market(ctx, "a"); !ok || err != nil {
		t.Errorf("après rétablissement : %v %v", ok, err)
	}
}

func TestCachedClient_BookHistorySearchAreCachedPerKey(t *testing.T) {
	f := NewFakeClient()
	f.Books["1"] = Book{TokenID: "1"}
	f.Books["2"] = Book{TokenID: "2"}
	f.Histories["1"] = []PricePoint{{Price: 0.1}}
	c := &CachedClient{Client: f, TTL: time.Minute}
	ctx := context.Background()
	c.Book(ctx, "1")
	c.Book(ctx, "1")
	c.Book(ctx, "2")
	c.History(ctx, "1", Interval1W)
	c.History(ctx, "1", Interval1W)
	c.History(ctx, "1", Interval1D)
	c.Search(ctx, "x")
	c.Search(ctx, "x")
	if f.Calls() != 5 {
		t.Errorf("appels = %d, veut 5 (2 carnets, 2 historiques, 1 recherche)", f.Calls())
	}
}

func TestCachedClient_ConcurrentUse(t *testing.T) {
	f := NewFakeClient()
	f.Markets["a"] = Market{Slug: "a"}
	c := &CachedClient{Client: f, TTL: time.Minute}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Market(context.Background(), "a")
		}()
	}
	wg.Wait()
}

// Les entrées expirées sont retirées : le cache ne grossit pas sans fin au
// fil des recherches.
func TestCachedClient_PrunesExpiredEntries(t *testing.T) {
	f := NewFakeClient()
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	c := &CachedClient{Client: f, TTL: time.Minute, Now: func() time.Time { return now }}
	ctx := context.Background()
	for _, q := range []string{"a", "b", "c"} {
		c.Search(ctx, q)
	}
	now = now.Add(2 * time.Minute)
	c.Search(ctx, "d")
	if n := c.size(); n != 1 {
		t.Errorf("entrées = %d, veut 1", n)
	}
}
