//go:build polymarket

package collect

import (
	"context"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/news"
	"github.com/JulianAndrieux/Jarvis/internal/polymarket"
)

// byCategory : des mots-clés fixes par catégorie, à la place du modèle
// local (absent de l'environnement de test).
type byCategory map[string][]string

func (b byCategory) Keywords(ctx context.Context, t Target, _ string) ([]string, string, error) {
	return b[t.Category], "fixe", nil
}

// Collecte réelle (réseau requis), stockage en mémoire :
// go test -tags=polymarket -run Live ./internal/collect/
func TestLive_Collector(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	store := NewFakeStore()
	c := &Collector{
		Store: store, Market: &polymarket.HTTPClient{}, Feeds: &news.RSS{Feeds: news.DefaultFeeds},
		Keywords: byCategory{"fed": {"Federal Reserve", "FOMC", "Powell"}, "crypto": {"bitcoin"}, "economy": {"inflation", "Federal Reserve"}, "politics": {"Trump", "Senate"}, "geopolitics": {"Ukraine", "Russia", "China"}},
		Tags:     DefaultTags, PerTag: 3, Backfill: 2 * 24 * time.Hour, Log: t.Logf,
	}
	n, err := c.RefreshTargets(ctx)
	if err != nil || n == 0 {
		t.Fatalf("cibles = %d, %v", n, err)
	}
	if _, err := c.FillKeywords(ctx); err != nil {
		t.Logf("mots-clés : %v", err)
	}
	pts, err := c.CollectPrices(ctx)
	if err != nil {
		t.Errorf("prix : %v", err)
	}
	arts, err := c.CollectFeeds(ctx)
	if err != nil {
		t.Logf("flux : %v", err)
	}
	ts, _ := store.Targets(ctx)
	linked := 0
	for _, tg := range ts {
		a, _ := store.Articles(ctx, ArticleQuery{Market: tg.Slug})
		p, _ := store.Prices(ctx, tg.TokenID, time.Now().Add(-49*time.Hour), time.Now())
		linked += len(a)
		t.Logf("%-11s %-60.60s %5d points, %2d articles", tg.Category, tg.Question, len(p), len(a))
	}
	t.Logf("%d cibles, %d points de prix, %d articles (%d rattachements)", n, pts, arts, linked)
	if pts == 0 {
		t.Error("aucun point de prix collecté")
	}
}
