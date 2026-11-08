package collect

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/news"
	"github.com/JulianAndrieux/Jarvis/internal/polymarket"
)

// FakeStore : Store en mémoire, même sémantique que MongoStore (seaux de
// prix par jour compris).
type FakeStore struct {
	mu       sync.Mutex
	targets  map[string]Target
	buckets  map[string][]polymarket.PricePoint
	articles map[string]news.Article
}

func NewFakeStore() *FakeStore {
	return &FakeStore{targets: map[string]Target{}, buckets: map[string][]polymarket.PricePoint{}, articles: map[string]news.Article{}}
}

func (s *FakeStore) UpsertTarget(ctx context.Context, t Target) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.targets[t.Slug]; ok {
		t.Keywords, t.KeywordsModel, t.KeywordsAt, t.GDELTAt = old.Keywords, old.KeywordsModel, old.KeywordsAt, old.GDELTAt
	}
	t.Keywords = slices.Clone(t.Keywords)
	s.targets[t.Slug] = t
	return nil
}

func (s *FakeStore) Targets(ctx context.Context) ([]Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Target, 0, len(s.targets))
	for _, t := range s.targets {
		t.Keywords = slices.Clone(t.Keywords)
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

func (s *FakeStore) SetKeywords(ctx context.Context, slug string, kw []string, model string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.targets[slug]
	if !ok {
		return fmt.Errorf("%w : %s", ErrUnknownTarget, slug)
	}
	t.Keywords, t.KeywordsModel, t.KeywordsAt = slices.Clone(kw), model, at
	s.targets[slug] = t
	return nil
}

func (s *FakeStore) SetGDELTChecked(ctx context.Context, slug string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.targets[slug]
	if !ok {
		return fmt.Errorf("%w : %s", ErrUnknownTarget, slug)
	}
	t.GDELTAt = at
	s.targets[slug] = t
	return nil
}

func (s *FakeStore) AddPrices(ctx context.Context, tokenID string, pts []polymarket.PricePoint) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for k, add := range groupByDay(tokenID, pts) {
		merged, n := mergePoints(s.buckets[k], add)
		s.buckets[k] = merged
		total += n
	}
	return total, nil
}

func (s *FakeStore) Prices(ctx context.Context, tokenID string, from, to time.Time) ([]polymarket.PricePoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []polymarket.PricePoint
	for _, k := range dayKeys(tokenID, from, to) {
		out = append(out, inRange(s.buckets[k], from, to)...)
	}
	return out, nil
}

func (s *FakeStore) LastPrice(ctx context.Context, tokenID string) (time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var last time.Time
	prefix := tokenID + ":"
	for k, pts := range s.buckets {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix && len(pts) > 0 && pts[len(pts)-1].At.After(last) {
			last = pts[len(pts)-1].At
		}
	}
	return last, !last.IsZero(), nil
}

func (s *FakeStore) UpsertArticles(ctx context.Context, arts []news.Article) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range arts {
		if old, ok := s.articles[a.ID]; ok {
			old.Markets = union(old.Markets, a.Markets)
			s.articles[a.ID] = old
			continue
		}
		a.Markets = union(nil, a.Markets)
		s.articles[a.ID] = a
		n++
	}
	return n, nil
}

func (s *FakeStore) LinkArticles(ctx context.Context, ids []string, slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if a, ok := s.articles[id]; ok {
			a.Markets = union(a.Markets, []string{slug})
			s.articles[id] = a
		}
	}
	return nil
}

func (s *FakeStore) Articles(ctx context.Context, q ArticleQuery) ([]news.Article, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []news.Article
	for _, a := range s.articles {
		if q.Market != "" && !slices.Contains(a.Markets, q.Market) {
			continue
		}
		if (!q.From.IsZero() && a.Published.Before(q.From)) || (!q.To.IsZero() && a.Published.After(q.To)) {
			continue
		}
		a.Markets = slices.Clone(a.Markets)
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Published.Equal(out[j].Published) {
			return out[i].Published.After(out[j].Published)
		}
		return out[i].ID < out[j].ID
	})
	limit := q.Limit
	if limit <= 0 {
		limit = defaultArticleLimit
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
