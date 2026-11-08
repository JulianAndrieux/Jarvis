package polymarket

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// FakeClient : Client en mémoire pour les tests. Err, si non nil, est
// rendue par toutes les méthodes.
type FakeClient struct {
	mu        sync.Mutex
	Markets   map[string]Market
	Books     map[string]Book
	Histories map[string][]PricePoint
	Events    []Event
	TagEvents map[string][]Event
	Err       error
	calls     int
}

func NewFakeClient() *FakeClient {
	return &FakeClient{Markets: map[string]Market{}, Books: map[string]Book{}, Histories: map[string][]PricePoint{}, TagEvents: map[string][]Event{}}
}

// Calls : nombre d'appels reçus.
func (f *FakeClient) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *FakeClient) enter() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.Err
}

func (f *FakeClient) Search(ctx context.Context, query string) ([]Event, error) {
	if err := f.enter(); err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Event
	for _, e := range f.Events {
		if q != "" && strings.Contains(strings.ToLower(e.Title), q) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *FakeClient) Market(ctx context.Context, slug string) (Market, bool, error) {
	if err := f.enter(); err != nil {
		return Market{}, false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.Markets[slug]
	return m, ok, nil
}

func (f *FakeClient) Book(ctx context.Context, tokenID string) (Book, error) {
	if err := f.enter(); err != nil {
		return Book{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.Books[tokenID]
	if !ok {
		return Book{}, fmt.Errorf("polymarket: fake : pas de carnet pour %s", tokenID)
	}
	return b, nil
}

func (f *FakeClient) History(ctx context.Context, tokenID string, iv Interval) ([]PricePoint, error) {
	if err := f.enter(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PricePoint(nil), f.Histories[tokenID]...), nil
}

func (f *FakeClient) EventsByTag(ctx context.Context, tag string, limit int) ([]Event, error) {
	if err := f.enter(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	ev := f.TagEvents[tag]
	if limit > 0 && len(ev) > limit {
		ev = ev[:limit]
	}
	return append([]Event(nil), ev...), nil
}

// HistoryRange : les points de Histories compris dans [from, to].
func (f *FakeClient) HistoryRange(ctx context.Context, tokenID string, from, to time.Time) ([]PricePoint, error) {
	if err := f.enter(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []PricePoint
	for _, p := range f.Histories[tokenID] {
		if !p.At.Before(from) && !p.At.After(to) {
			out = append(out, p)
		}
	}
	return out, nil
}
