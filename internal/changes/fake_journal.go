package changes

import (
	"context"
	"sort"
	"sync"
)

// FakeJournal est un Journal en mémoire, pour les tests.
type FakeJournal struct {
	mu  sync.Mutex
	ops []Op
}

func NewFakeJournal() *FakeJournal { return &FakeJournal{} }

func (j *FakeJournal) Append(ctx context.Context, ops ...Op) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.ops = append(j.ops, ops...)
	return nil
}

func (j *FakeJournal) List(ctx context.Context, q Query) ([]Op, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	limit := q.Limit
	if limit == 0 {
		limit = DefaultLimit
	}
	var out []Op
	for _, o := range j.ops {
		if q.Matches(o) {
			out = append(out, o)
		}
	}
	// Du plus récent au plus ancien ; à instant égal, le dernier écrit
	// d'abord (l'ordre d'arrivée fait foi).
	sort.SliceStable(out, func(i, k int) bool { return out[i].At.After(out[k].At) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
