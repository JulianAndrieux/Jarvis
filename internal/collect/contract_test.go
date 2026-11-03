package collect

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/news"
	"github.com/JulianAndrieux/Jarvis/internal/polymarket"
)

// storeContract : FakeStore et MongoStore passent le même contrat. stamp
// rend les identifiants uniques (collections de test partagées).
func storeContract(t *testing.T, s Store, stamp string) {
	t.Helper()
	ctx := context.Background()
	at := func(m int) time.Time { return time.Date(2026, 11, 3, 10, m, 0, 0, time.UTC) }
	slug := "contract-" + stamp

	// Cibles : création, puis mise à jour qui ne perd pas les mots-clés.
	tg := Target{Slug: slug, Question: "Fed cut in December?", TokenID: "9" + stamp, Category: "fed", UpdatedAt: at(0)}
	if err := s.UpsertTarget(ctx, tg); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKeywords(ctx, slug, []string{"Federal Reserve", "rate cut"}, "qwen3-8b", at(1)); err != nil {
		t.Fatal(err)
	}
	tg.Question = "Fed cut in December 2026?"
	tg.UpdatedAt = at(2)
	if err := s.UpsertTarget(ctx, tg); err != nil {
		t.Fatal(err)
	}
	got := findTarget(t, s, slug)
	if got.Question != "Fed cut in December 2026?" || !reflect.DeepEqual(got.Keywords, []string{"Federal Reserve", "rate cut"}) || got.KeywordsModel != "qwen3-8b" || !got.KeywordsAt.Equal(at(1)) {
		t.Fatalf("cible = %+v", got)
	}
	if err := s.SetGDELTChecked(ctx, slug, at(5)); err != nil {
		t.Fatal(err)
	}
	if got := findTarget(t, s, slug); !got.GDELTAt.Equal(at(5)) || len(got.Keywords) != 2 {
		t.Errorf("après SetGDELTChecked : %+v", got)
	}
	if err := s.SetKeywords(ctx, "absent-"+stamp, []string{"x"}, "m", at(0)); err == nil {
		t.Error("SetKeywords sur une cible inconnue doit échouer")
	}

	// Prix : idempotents, triés, bornés par la plage demandée, et à cheval
	// sur deux jours.
	tok := "9" + stamp
	day := time.Date(2026, 11, 3, 23, 58, 0, 0, time.UTC)
	pts := []polymarket.PricePoint{{At: day, Price: 0.41}, {At: day.Add(time.Minute), Price: 0.42}, {At: day.Add(2 * time.Minute), Price: 0.43}}
	if n, err := s.AddPrices(ctx, tok, pts); err != nil || n != 3 {
		t.Fatalf("AddPrices = %d, %v", n, err)
	}
	if n, err := s.AddPrices(ctx, tok, append(pts[1:], polymarket.PricePoint{At: day.Add(3 * time.Minute), Price: 0.44})); err != nil || n != 1 {
		t.Fatalf("AddPrices (rejeu) = %d, %v ; veut 1 nouveau point", n, err)
	}
	all, err := s.Prices(ctx, tok, day.Add(-time.Hour), day.Add(time.Hour))
	if err != nil || len(all) != 4 || all[0].Price != 0.41 || all[3].Price != 0.44 {
		t.Fatalf("Prices = %+v, %v", all, err)
	}
	part, _ := s.Prices(ctx, tok, day.Add(time.Minute), day.Add(2*time.Minute))
	if len(part) != 2 || part[0].Price != 0.42 {
		t.Errorf("Prices(plage) = %+v", part)
	}
	if last, ok, err := s.LastPrice(ctx, tok); err != nil || !ok || !last.Equal(day.Add(3*time.Minute)) {
		t.Errorf("LastPrice = %v %v %v", last, ok, err)
	}
	if _, ok, err := s.LastPrice(ctx, "0"+stamp); ok || err != nil {
		t.Errorf("LastPrice(inconnu) = %v %v", ok, err)
	}

	// Articles : dédoublonnés par identifiant, rattachements réunis, triés
	// du plus récent au plus ancien.
	a1 := news.Article{ID: "a1-" + stamp, URL: "https://x.example/1", Title: "Powell signals cut", Source: "x", Via: news.ViaRSS, Published: at(10), Seen: at(11), Markets: []string{slug}}
	a2 := news.Article{ID: "a2-" + stamp, URL: "https://x.example/2", Title: "Unrelated", Source: "x", Via: news.ViaRSS, Published: at(20), Seen: at(21)}
	if n, err := s.UpsertArticles(ctx, []news.Article{a1, a2}); err != nil || n != 2 {
		t.Fatalf("UpsertArticles = %d, %v", n, err)
	}
	// Le même article revu par GDELT, rattaché à un second marché.
	again := a1
	again.Via, again.Markets, again.Seen = news.ViaGDELT, []string{"other-" + stamp}, at(30)
	if n, err := s.UpsertArticles(ctx, []news.Article{again}); err != nil || n != 0 {
		t.Fatalf("UpsertArticles (déjà vu) = %d, %v ; veut 0 nouveau", n, err)
	}
	mine, err := s.Articles(ctx, ArticleQuery{Market: slug})
	if err != nil || len(mine) != 1 || mine[0].Title != "Powell signals cut" {
		t.Fatalf("Articles(marché) = %+v, %v", mine, err)
	}
	// La première vue fait foi (c'est elle qui dit quand on l'aurait su).
	sort.Strings(mine[0].Markets)
	if !mine[0].Seen.Equal(at(11)) || len(mine[0].Markets) != 2 {
		t.Errorf("article fusionné = %+v", mine[0])
	}
	if other, _ := s.Articles(ctx, ArticleQuery{Market: "other-" + stamp}); len(other) != 1 {
		t.Errorf("Articles(second marché) = %d", len(other))
	}
	if win, _ := s.Articles(ctx, ArticleQuery{Market: slug, From: at(15), To: at(40)}); len(win) != 0 {
		t.Errorf("Articles(plage) = %d, veut 0", len(win))
	}
	if err := s.LinkArticles(ctx, []string{a2.ID}, slug); err != nil {
		t.Fatal(err)
	}
	if mine, _ := s.Articles(ctx, ArticleQuery{Market: slug}); len(mine) != 2 || mine[0].ID != a2.ID {
		t.Errorf("après LinkArticles : %+v (le plus récent d'abord)", mine)
	}
	if since, _ := s.Articles(ctx, ArticleQuery{From: at(19), Limit: 10}); len(since) != 1 || since[0].ID != a2.ID {
		t.Errorf("Articles(depuis, tous marchés) = %+v", since)
	}
}

func findTarget(t *testing.T, s Store, slug string) Target {
	t.Helper()
	ts, err := s.Targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range ts {
		if x.Slug == slug {
			return x
		}
	}
	t.Fatalf("cible %s introuvable", slug)
	return Target{}
}

func TestFakeStore_Contract(t *testing.T) { storeContract(t, NewFakeStore(), "fake") }
