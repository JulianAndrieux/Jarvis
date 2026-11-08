//go:build news

package news

import (
	"context"
	"testing"
	"time"
)

// Contre les vrais flux et la vraie API GDELT (réseau requis) :
// go test -tags=news ./internal/news/. GDELT peut répondre 429 depuis une
// adresse partagée : le test le dit sans échouer.
func TestLive_DefaultFeeds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	r := &RSS{Feeds: DefaultFeeds}
	arts, errs := r.FetchAll(ctx)
	for _, err := range errs {
		t.Errorf("%v", err)
	}
	per := map[string]int{}
	guessed := 0
	for _, a := range arts {
		per[a.Source]++
		if a.DateGuessed {
			guessed++
		}
	}
	for _, f := range DefaultFeeds {
		if per[f.Name] == 0 {
			t.Errorf("flux %s : aucun article", f.Name)
		}
	}
	t.Logf("%d articles, %d sans date fiable : %v", len(arts), guessed, per)
}

func TestLive_GDELT(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	arts, err := (&GDELT{}).Search(ctx, []string{"Federal Reserve"}, time.Now().Add(-6*time.Hour))
	if err == ErrRateLimited {
		t.Skip("GDELT : trop de requêtes depuis cette adresse")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d articles ; premier : %+v", len(arts), first(arts))
}

func first(a []Article) any {
	if len(a) == 0 {
		return nil
	}
	return a[0]
}
