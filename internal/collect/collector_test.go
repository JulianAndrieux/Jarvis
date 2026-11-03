package collect

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/news"
	"github.com/JulianAndrieux/Jarvis/internal/polymarket"
	"github.com/JulianAndrieux/Jarvis/internal/trading"
)

var collectNow = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func mkt(slug, q, tok string, vol24 float64, closed bool) polymarket.Market {
	return polymarket.Market{Slug: slug, Question: q, Volume24h: vol24, Closed: closed, AcceptingOrders: !closed,
		Outcomes: []polymarket.Outcome{{Name: "Yes", TokenID: tok, Price: 0.4}, {Name: "No", TokenID: tok + "0", Price: 0.6}}}
}

type fakeFeeds struct {
	arts []news.Article
	errs []error
}

func (f *fakeFeeds) FetchAll(ctx context.Context) ([]news.Article, []error) { return f.arts, f.errs }

type fakeGDELT struct {
	mu     sync.Mutex
	calls  [][]string
	since  []time.Time
	result map[string][]news.Article // clé : première phrase
	err    error
}

func (g *fakeGDELT) Search(ctx context.Context, phrases []string, since time.Time) ([]news.Article, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, phrases)
	g.since = append(g.since, since)
	if g.err != nil {
		return nil, g.err
	}
	return g.result[phrases[0]], nil
}

type fakeKeywords struct{ kw map[string][]string }

func (f fakeKeywords) Keywords(ctx context.Context, t Target, description string) ([]string, string, error) {
	kw, ok := f.kw[t.Slug]
	if !ok {
		return nil, "", errors.New("modèle indisponible")
	}
	return kw, "fake-model", nil
}

func newCollector(t *testing.T) (*Collector, *FakeStore, *polymarket.FakeClient) {
	t.Helper()
	pm := polymarket.NewFakeClient()
	// « fed » : un événement à trois branches (la plus active ouverte est
	// gardée), un événement clos.
	pm.TagEvents["fed"] = []polymarket.Event{
		{Slug: "fed-dec", Title: "Fed decision in December?", Markets: []polymarket.Market{
			mkt("fed-dec-cut25", "Fed cuts 25 bps?", "11", 500, false),
			mkt("fed-dec-hold", "Fed holds?", "12", 900, false),
			mkt("fed-dec-cut50", "Fed cuts 50 bps?", "13", 5000, true), // la plus active, mais close
		}},
		{Slug: "fed-old", Title: "Old", Markets: []polymarket.Market{mkt("fed-old-m", "Old?", "14", 100, true)}},
	}
	pm.TagEvents["crypto"] = []polymarket.Event{{Slug: "btc", Title: "BTC 100k?", Markets: []polymarket.Market{mkt("btc-100k", "Bitcoin above 100k?", "21", 50, false)}}}
	pm.Markets["xi-out"] = mkt("xi-out", "Xi Jinping out before 2027?", "31", 10, false)
	pm.Markets["xi-out"] = func(m polymarket.Market) polymarket.Market { m.EventTitle = "Xi"; return m }(pm.Markets["xi-out"])
	store := NewFakeStore()
	c := &Collector{
		Store: store, Market: pm, Tags: []string{"fed", "crypto"}, PerTag: 5,
		Watched: func(ctx context.Context) ([]trading.Watch, error) {
			return []trading.Watch{{Slug: "xi-out", Question: "Xi Jinping out before 2027?"}}, nil
		},
		Now: func() time.Time { return collectNow }, Backfill: 3 * 24 * time.Hour,
	}
	return c, store, pm
}

func TestCollector_RefreshTargets(t *testing.T) {
	c, store, _ := newCollector(t)
	n, err := c.RefreshTargets(context.Background())
	if err != nil || n != 3 {
		t.Fatalf("RefreshTargets = %d, %v ; veut 3 (une branche fed, btc, le suivi)", n, err)
	}
	ts, _ := store.Targets(context.Background())
	got := map[string]Target{}
	for _, x := range ts {
		got[x.Slug] = x
	}
	// La branche ouverte la plus active de l'événement, pas la close.
	if f, ok := got["fed-dec-hold"]; !ok || f.Category != "fed" || f.TokenID != "12" || f.EventTitle != "Fed decision in December?" {
		t.Errorf("fed = %+v (cibles %v)", f, keys(got))
	}
	if _, ok := got["fed-dec-cut50"]; ok {
		t.Error("une branche close n'est pas une cible")
	}
	if x := got["xi-out"]; x.Category != CategoryWatched || x.TokenID != "31" {
		t.Errorf("suivi = %+v", x)
	}
	if !got["btc-100k"].UpdatedAt.Equal(collectNow) {
		t.Errorf("UpdatedAt = %v", got["btc-100k"].UpdatedAt)
	}
}

func keys(m map[string]Target) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Une catégorie en panne n'empêche pas les autres.
func TestCollector_RefreshTargets_PartialFailure(t *testing.T) {
	c, _, _ := newCollector(t)
	c.Tags = []string{"Pas Valide", "crypto"}
	c.Market = &failingTag{FakeClient: c.Market.(*polymarket.FakeClient), bad: "Pas Valide"}
	n, err := c.RefreshTargets(context.Background())
	if n != 2 || err == nil || !strings.Contains(err.Error(), "Pas Valide") {
		t.Errorf("RefreshTargets = %d, %v", n, err)
	}
}

type failingTag struct {
	*polymarket.FakeClient
	bad string
}

func (f *failingTag) EventsByTag(ctx context.Context, tag string, limit int) ([]polymarket.Event, error) {
	if tag == f.bad {
		return nil, errors.New("tag refusé")
	}
	return f.FakeClient.EventsByTag(ctx, tag, limit)
}

func TestCollector_CollectPrices_BackfillThenIncremental(t *testing.T) {
	c, store, pm := newCollector(t)
	ctx := context.Background()
	c.RefreshTargets(ctx)
	// Historique sur 10 jours pour le jeton « 12 » : seuls les 3 jours de
	// Backfill sont repris la première fois.
	var pts []polymarket.PricePoint
	for h := 0; h < 240; h++ {
		pts = append(pts, polymarket.PricePoint{At: collectNow.Add(-time.Duration(240-h) * time.Hour), Price: 0.5})
	}
	pm.Histories["12"] = pts
	n, err := c.CollectPrices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 72 {
		t.Errorf("points = %d, veut 72 (3 jours d'heures)", n)
	}
	got, _ := store.Prices(ctx, "12", collectNow.Add(-30*24*time.Hour), collectNow)
	if len(got) != 72 || got[0].At.Before(collectNow.Add(-3*24*time.Hour)) {
		t.Errorf("stockés = %d, premier %v", len(got), got[0].At)
	}
	// Ensuite, seulement ce qui suit le dernier point connu.
	pm.Histories["12"] = append(pm.Histories["12"], polymarket.PricePoint{At: collectNow.Add(30 * time.Minute), Price: 0.6})
	c.Now = func() time.Time { return collectNow.Add(time.Hour) }
	if n, err := c.CollectPrices(ctx); err != nil || n != 1 {
		t.Errorf("second passage = %d, %v ; veut 1", n, err)
	}
}

// Une plage de rattrapage plus longue que ce que le CLOB accepte est
// découpée.
func TestCollector_CollectPrices_SplitsLongRanges(t *testing.T) {
	c, _, pm := newCollector(t)
	ctx := context.Background()
	c.Backfill = 20 * 24 * time.Hour
	c.Tags = nil
	c.RefreshTargets(ctx) // le seul suivi : jeton 31
	rec := &rangeRecorder{FakeClient: pm}
	c.Market = rec
	c.CollectPrices(ctx)
	if len(rec.spans) != 3 {
		t.Fatalf("requêtes = %d, veut 3 (20 jours en tranches de 7)", len(rec.spans))
	}
	for _, s := range rec.spans {
		if s > polymarket.MaxRangeSpan {
			t.Errorf("tranche de %s", s)
		}
	}
}

type rangeRecorder struct {
	*polymarket.FakeClient
	spans []time.Duration
}

func (r *rangeRecorder) HistoryRange(ctx context.Context, tok string, from, to time.Time) ([]polymarket.PricePoint, error) {
	r.spans = append(r.spans, to.Sub(from))
	return r.FakeClient.HistoryRange(ctx, tok, from, to)
}

func TestCleanKeywords(t *testing.T) {
	got := CleanKeywords([]string{`  "Federal  Reserve" `, "federal reserve", "rate cut", "a", "", "one two three four five six seven", "Powell", "FOMC", "December", "extra"})
	want := []string{"Federal Reserve", "rate cut", "Powell", "FOMC", "December"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CleanKeywords = %q, veut %q", got, want)
	}
}

func TestCollector_FillKeywords(t *testing.T) {
	c, store, _ := newCollector(t)
	ctx := context.Background()
	c.RefreshTargets(ctx)
	acquired := 0
	c.Acquire = func(ctx context.Context) (func(), error) { acquired++; return func() {}, nil }
	c.Keywords = fakeKeywords{kw: map[string][]string{"fed-dec-hold": {"Federal Reserve", "FOMC"}, "xi-out": {"Xi Jinping"}}}
	// Un article déjà enregistré qui parle de la Fed : rattaché dès que la
	// cible a ses mots-clés.
	store.UpsertArticles(ctx, []news.Article{{ID: "old", Title: "Federal Reserve holds", Published: collectNow.Add(-time.Hour), Via: news.ViaRSS}})
	n, err := c.FillKeywords(ctx)
	if n != 2 || err == nil {
		t.Errorf("FillKeywords = %d, %v ; veut 2 et l'erreur de btc", n, err)
	}
	if acquired != 3 {
		t.Errorf("file des modèles prise %d fois, veut 3 (une par cible)", acquired)
	}
	tg := findTarget(t, store, "fed-dec-hold")
	if !reflect.DeepEqual(tg.Keywords, []string{"Federal Reserve", "FOMC"}) || tg.KeywordsModel != "fake-model" {
		t.Errorf("cible = %+v", tg)
	}
	if arts, _ := store.Articles(ctx, ArticleQuery{Market: "fed-dec-hold"}); len(arts) != 1 {
		t.Errorf("article ancien non rattaché : %d", len(arts))
	}
	// Une cible qui a déjà ses mots-clés n'est pas redemandée.
	acquired = 0
	c.FillKeywords(ctx)
	if acquired != 1 {
		t.Errorf("second passage : %d demandes, veut 1 (btc, toujours sans)", acquired)
	}
}

func TestCollector_CollectFeeds_MatchesTargets(t *testing.T) {
	c, store, _ := newCollector(t)
	ctx := context.Background()
	c.RefreshTargets(ctx)
	store.SetKeywords(ctx, "fed-dec-hold", []string{"Federal Reserve"}, "m", collectNow)
	store.SetKeywords(ctx, "btc-100k", []string{"bitcoin"}, "m", collectNow)
	c.Feeds = &fakeFeeds{
		arts: []news.Article{
			{ID: "f1", Title: "Federal Reserve keeps rates", Published: collectNow, Via: news.ViaRSS},
			{ID: "f2", Title: "Bitcoin and the Federal Reserve", Published: collectNow, Via: news.ViaRSS},
			{ID: "f3", Title: "Football", Published: collectNow, Via: news.ViaRSS},
		},
		errs: []error{errors.New("flux Cassé : HTTP 500")},
	}
	n, err := c.CollectFeeds(ctx)
	if n != 3 || err == nil {
		t.Errorf("CollectFeeds = %d, %v", n, err)
	}
	fed, _ := store.Articles(ctx, ArticleQuery{Market: "fed-dec-hold"})
	btc, _ := store.Articles(ctx, ArticleQuery{Market: "btc-100k"})
	if len(fed) != 2 || len(btc) != 1 {
		t.Errorf("fed=%d btc=%d", len(fed), len(btc))
	}
	if st := c.Status(); st.LastFeeds.IsZero() || len(st.Errors) == 0 || !strings.Contains(st.Errors[0].Message, "Cassé") {
		t.Errorf("état = %+v", st)
	}
}

func TestCollector_CollectGDELT(t *testing.T) {
	c, store, _ := newCollector(t)
	ctx := context.Background()
	c.RefreshTargets(ctx)
	store.SetKeywords(ctx, "fed-dec-hold", []string{"Federal Reserve", "FOMC"}, "m", collectNow)
	g := &fakeGDELT{result: map[string][]news.Article{"Federal Reserve": {{ID: "g1", Title: "Fed", Published: collectNow.Add(-time.Hour), Via: news.ViaGDELT}}}}
	c.GDELT = g
	n, err := c.CollectGDELT(ctx)
	if err != nil || n != 1 {
		t.Fatalf("CollectGDELT = %d, %v", n, err)
	}
	// Seules les cibles qui ont des mots-clés sont cherchées ; la première
	// fois depuis Backfill.
	if len(g.calls) != 1 || !g.since[0].Equal(collectNow.Add(-3*24*time.Hour)) {
		t.Errorf("appels = %v depuis %v", g.calls, g.since)
	}
	if arts, _ := store.Articles(ctx, ArticleQuery{Market: "fed-dec-hold"}); len(arts) != 1 {
		t.Errorf("articles rattachés = %d", len(arts))
	}
	// La fois suivante : depuis le dernier passage, moins le délai
	// d'indexation de GDELT (recouvrement sans doublon).
	c.Now = func() time.Time { return collectNow.Add(time.Hour) }
	c.CollectGDELT(ctx)
	if want := collectNow.Add(-gdeltOverlap); !g.since[1].Equal(want) {
		t.Errorf("depuis = %v, veut %v", g.since[1], want)
	}
}

// GDELT qui refuse (trop de requêtes) : on arrête le passage, on reprendra
// au suivant, sans marquer la cible comme vérifiée.
func TestCollector_CollectGDELT_RateLimited(t *testing.T) {
	c, store, _ := newCollector(t)
	ctx := context.Background()
	c.RefreshTargets(ctx)
	store.SetKeywords(ctx, "fed-dec-hold", []string{"Fed"}, "m", collectNow)
	store.SetKeywords(ctx, "btc-100k", []string{"bitcoin"}, "m", collectNow)
	g := &fakeGDELT{err: news.ErrRateLimited}
	c.GDELT = g
	if _, err := c.CollectGDELT(ctx); !errors.Is(err, news.ErrRateLimited) {
		t.Errorf("err = %v", err)
	}
	if len(g.calls) != 1 {
		t.Errorf("appels = %d, veut 1 (arrêt au premier refus)", len(g.calls))
	}
	if tg := findTarget(t, store, "btc-100k"); !tg.GDELTAt.IsZero() {
		t.Error("une cible non interrogée ne doit pas être marquée")
	}
}

// Une cible qui n'a plus été vue dans les catégories (marché fini, sorti
// du classement) n'est plus collectée, mais son historique reste.
func TestCollector_StaleTargetsAreSkipped(t *testing.T) {
	c, store, pm := newCollector(t)
	ctx := context.Background()
	store.UpsertTarget(ctx, Target{Slug: "ancien", TokenID: "99", UpdatedAt: collectNow.Add(-7 * 24 * time.Hour)})
	store.SetKeywords(ctx, "ancien", []string{"x"}, "m", collectNow)
	pm.Histories["99"] = []polymarket.PricePoint{{At: collectNow.Add(-time.Hour), Price: 0.1}}
	g := &fakeGDELT{}
	c.GDELT = g
	if n, _ := c.CollectPrices(ctx); n != 0 {
		t.Errorf("prix collectés pour une cible périmée : %d", n)
	}
	c.CollectGDELT(ctx)
	if len(g.calls) != 0 {
		t.Error("GDELT interrogé pour une cible périmée")
	}
}

func TestLLMKeywords(t *testing.T) {
	f := &llm.FakeClient{Results: map[int]llm.ExtractResult{0: {JSON: json.RawMessage(`{"keywords":["Federal Reserve","rate cut","Federal Reserve"]}`)}}}
	k := LLMKeywords{LLM: f, Model: "qwen3-8b"}
	kw, model, err := k.Keywords(context.Background(), Target{Question: "Fed cut in December?", EventTitle: "Fed decision"}, strings.Repeat("règles ", 1000))
	if err != nil || model != "qwen3-8b" || !reflect.DeepEqual(kw, []string{"Federal Reserve", "rate cut"}) {
		t.Fatalf("%q %q %v", kw, model, err)
	}
	req := f.Calls[0]
	if !strings.Contains(req.Text, "Fed cut in December?") || !strings.Contains(req.Text, "Fed decision") || len(req.Text) > 3000 || !strings.Contains(string(req.Schema), "keywords") || req.Prompt != DefaultKeywordsPrompt {
		t.Errorf("requête = %+v", req)
	}
	// Une réponse sans mot-clé utilisable est une erreur, pas une cible
	// silencieusement sans actualités.
	f.Results[0] = llm.ExtractResult{JSON: json.RawMessage(`{"keywords":["a",""]}`)}
	if _, _, err := k.Keywords(context.Background(), Target{Question: "Q"}, ""); err == nil {
		t.Error("veut une erreur")
	}
	k.Prompt = func() string { return "consigne modifiée" }
	f.Results[0] = llm.ExtractResult{JSON: json.RawMessage(`{"keywords":["Powell"]}`)}
	k.Keywords(context.Background(), Target{Question: "Q"}, "")
	if f.Calls[len(f.Calls)-1].Prompt != "consigne modifiée" {
		t.Error("le prompt modifiable doit être utilisé")
	}
}

// Un marché dont l'échéance est passée (en attente de résolution) n'est
// plus une cible : il n'y a plus rien à étudier.
func TestCollector_RefreshTargets_SkipsExpiredMarkets(t *testing.T) {
	c, store, pm := newCollector(t)
	expired := mkt("old-ceasefire", "Ceasefire through October 31?", "41", 99999, false)
	expired.EndDate = collectNow.Add(-24 * time.Hour)
	live := mkt("new-ceasefire", "Ceasefire through November 30?", "42", 10, false)
	live.EndDate = collectNow.Add(24 * time.Hour)
	pm.TagEvents["geopolitics"] = []polymarket.Event{{Slug: "cf", Title: "Ceasefire", Markets: []polymarket.Market{expired, live}}}
	c.Tags, c.Watched = []string{"geopolitics"}, nil
	c.RefreshTargets(context.Background())
	ts, _ := store.Targets(context.Background())
	if len(ts) != 1 || ts[0].Slug != "new-ceasefire" {
		t.Errorf("cibles = %+v, veut new-ceasefire seulement", ts)
	}
}
