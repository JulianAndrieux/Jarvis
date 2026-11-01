package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/polymarket"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
	"github.com/JulianAndrieux/Jarvis/internal/trading"
)

// Jalon 53 : marchés Polymarket en lecture seule — recherche, suivi, fiche
// (carnet, courbe, règles), alertes dans la barre latérale.

func xiMarket() polymarket.Market {
	return polymarket.Market{
		Slug: "xi-out", Question: "Xi Jinping out before 2027?", EventSlug: "xi-out", EventTitle: "Xi Jinping out before 2027?",
		Description: "Resolves <b>Yes</b> if removed.", EndDate: time.Date(2027, 1, 1, 4, 59, 0, 0, time.UTC),
		Outcomes: []polymarket.Outcome{{Name: "Yes", TokenID: "111", Price: 0.0235}, {Name: "No", TokenID: "222", Price: 0.9765}},
		Volume:   16786286, Liquidity: 327952, DayChange: -0.0045, Active: true, AcceptingOrders: true, TickSize: 0.001, MinOrderSize: 5,
	}
}

func btcMarket() polymarket.Market {
	return polymarket.Market{
		Slug: "btc-94k", Question: "Will Bitcoin reach $94,000?", GroupItemTitle: "↑ 94 000 $", EventSlug: "btc-week", EventTitle: "What price will Bitcoin hit?",
		Outcomes: []polymarket.Outcome{{Name: "Yes", TokenID: "333", Price: 0.31}, {Name: "No", TokenID: "444", Price: 0.69}},
		Volume:   1922409, DayChange: 0.12, Active: true, AcceptingOrders: true,
	}
}

func newMarketsServer(t *testing.T) (*Server, *polymarket.FakeClient, *trading.FakeStore) {
	t.Helper()
	s, _ := newTestServer(t, &blockingRunner{})
	f := polymarket.NewFakeClient()
	f.Markets["xi-out"] = xiMarket()
	f.Markets["btc-94k"] = btcMarket()
	f.Events = []polymarket.Event{{Slug: "btc-week", Title: "What price will Bitcoin hit?", Markets: []polymarket.Market{btcMarket()}}}
	f.Books["111"] = polymarket.Book{TokenID: "111", Bids: []polymarket.Level{{Price: 0.023, Size: 2884}, {Price: 0.022, Size: 1468}}, Asks: []polymarket.Level{{Price: 0.024, Size: 500}}}
	f.Books["222"] = polymarket.Book{TokenID: "222", Bids: []polymarket.Level{{Price: 0.976, Size: 500}}, Asks: []polymarket.Level{{Price: 0.977, Size: 2884}}}
	at := func(h int) time.Time { return time.Date(2026, 10, 30, h, 0, 0, 0, time.UTC) }
	f.Histories["111"] = []polymarket.PricePoint{{At: at(0), Price: 0.0285}, {At: at(6), Price: 0.046}, {At: at(12), Price: 0.0235}}
	store := trading.NewFakeStore()
	s.Market, s.Trading = f, store
	return s, f, store
}

func TestMarkets_DisabledWithoutClient(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})
	if rec := do(s, http.MethodGet, "/markets", nil); rec.Code != http.StatusNotFound {
		t.Errorf("/markets sans client = %d, veut 404", rec.Code)
	}
}

func TestMarkets_NavLink(t *testing.T) {
	s, _, _ := newMarketsServer(t)
	page := do(s, http.MethodGet, "/markets", nil).Body.String()
	if !strings.Contains(page, `href="/markets" class="active"`) {
		t.Error("l'onglet Marchés doit être actif sur sa page")
	}
	if !strings.Contains(page, "Aucun marché suivi") {
		t.Error("liste vide : message attendu")
	}
}

func TestMarkets_SearchShowsResultsAndWatchButton(t *testing.T) {
	s, _, _ := newMarketsServer(t)
	rec := do(s, http.MethodGet, "/markets?q=bitcoin", nil)
	page := rec.Body.String()
	for _, want := range []string{"What price will Bitcoin hit?", "↑ 94 000 $", "Yes 31,0 %", "☆ Suivre", `action="/markets/btc-94k/watch"`, `value="/markets?q=bitcoin"`} {
		if !strings.Contains(page, want) {
			t.Errorf("recherche : manque %q", want)
		}
	}
	if page := do(s, http.MethodGet, "/markets?q=rien", nil).Body.String(); !strings.Contains(page, "Aucun marché actif ne correspond") {
		t.Error("recherche sans résultat : message attendu")
	}
}

func TestMarkets_SearchErrorIsShownNotFatal(t *testing.T) {
	s, f, _ := newMarketsServer(t)
	f.Err = errors.New("polymarket injoignable")
	rec := do(s, http.MethodGet, "/markets?q=bitcoin", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "polymarket injoignable") {
		t.Errorf("code %d, page sans l'erreur", rec.Code)
	}
}

func TestMarkets_WatchListUnwatch(t *testing.T) {
	s, _, store := newMarketsServer(t)
	rec := do(s, http.MethodPost, "/markets/xi-out/watch", url.Values{"back": {"/markets?q=xi"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/markets?q=xi" {
		t.Fatalf("watch = %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	w, ok, _ := store.For(tenancy.LocalScope()).GetWatch(context.Background(), "xi-out")
	if !ok || w.Question != "Xi Jinping out before 2027?" || w.AddedAt.IsZero() {
		t.Fatalf("suivi = %+v, %v", w, ok)
	}
	// Suivre deux fois : sans erreur, sans doublon.
	if rec := do(s, http.MethodPost, "/markets/xi-out/watch", nil); rec.Code != http.StatusSeeOther {
		t.Errorf("second suivi = %d", rec.Code)
	}
	page := do(s, http.MethodGet, "/markets", nil).Body.String()
	for _, want := range []string{"Xi Jinping out before 2027?", "2,4 %", "−0,5 pt", "16,8 M $", "01/01/2027"} {
		if !strings.Contains(page, want) {
			t.Errorf("liste de suivi : manque %q", want)
		}
	}
	if strings.Count(page, `href="/markets/xi-out"`) != 1 {
		t.Error("le marché doit apparaître une seule fois")
	}
	if rec := do(s, http.MethodPost, "/markets/xi-out/unwatch", url.Values{"back": {"https://ailleurs.example"}}); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/markets" {
		t.Errorf("unwatch = %d → %q (une adresse externe ne doit jamais servir de retour)", rec.Code, rec.Header().Get("Location"))
	}
	if _, ok, _ := store.For(tenancy.LocalScope()).GetWatch(context.Background(), "xi-out"); ok {
		t.Error("toujours suivi")
	}
}

func TestMarkets_WatchUnknownOrInvalidMarket(t *testing.T) {
	s, _, _ := newMarketsServer(t)
	if rec := do(s, http.MethodPost, "/markets/absent/watch", nil); rec.Code != http.StatusNotFound {
		t.Errorf("marché inconnu = %d, veut 404", rec.Code)
	}
	if rec := do(s, http.MethodPost, "/markets/Pas-Valide!/watch", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("slug invalide = %d, veut 400", rec.Code)
	}
}

// Polymarket injoignable : la liste reste lisible avec ce qui a été
// recopié au moment du suivi.
func TestMarkets_WatchListSurvivesPolymarketOutage(t *testing.T) {
	s, f, _ := newMarketsServer(t)
	do(s, http.MethodPost, "/markets/xi-out/watch", nil)
	f.Err = errors.New("hors ligne")
	rec := do(s, http.MethodGet, "/markets", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Xi Jinping out before 2027?") || !strings.Contains(rec.Body.String(), "Prix indisponibles") {
		t.Errorf("code %d ; page doit garder la question et dire que les prix manquent", rec.Code)
	}
}

func TestMarketDetail(t *testing.T) {
	s, _, _ := newMarketsServer(t)
	rec := do(s, http.MethodGet, "/markets/xi-out", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	page := rec.Body.String()
	for _, want := range []string{
		"Xi Jinping out before 2027?", "2,4 %", "97,7 %",
		"Carnet « Yes »", "Carnet « No »", "2,3 ¢", "2,4 ¢", "2,9 k",
		`class="chart-line"`, `d="M0.0,`,
		"https://polymarket.com/event/xi-out", `rel="noopener noreferrer"`,
		"☆ Suivre ce marché", "Suis ce marché pour y attacher une note",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("fiche : manque %q", want)
		}
	}
	// Les règles de résolution sont du texte d'un tiers : échappées.
	if strings.Contains(page, "<b>Yes</b>") || !strings.Contains(page, "&lt;b&gt;Yes&lt;/b&gt;") {
		t.Error("la description de Polymarket doit être échappée")
	}
}

func TestMarketDetail_IntervalAndErrors(t *testing.T) {
	s, f, _ := newMarketsServer(t)
	if page := do(s, http.MethodGet, "/markets/xi-out?iv=1m", nil).Body.String(); !strings.Contains(page, `<span class="chip active">30 jours</span>`) {
		t.Error("l'intervalle demandé doit être actif")
	}
	if page := do(s, http.MethodGet, "/markets/xi-out?iv=5y", nil).Body.String(); !strings.Contains(page, `<span class="chip active">7 jours</span>`) {
		t.Error("un intervalle inconnu retombe sur 7 jours")
	}
	if rec := do(s, http.MethodGet, "/markets/absent", nil); rec.Code != http.StatusNotFound {
		t.Errorf("marché inconnu = %d", rec.Code)
	}
	if rec := do(s, http.MethodGet, "/markets/A%20B", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("slug invalide = %d", rec.Code)
	}
	delete(f.Books, "222")
	if page := do(s, http.MethodGet, "/markets/xi-out", nil).Body.String(); !strings.Contains(page, "Carnet indisponible") || !strings.Contains(page, "Carnet « Yes »") {
		t.Error("un carnet manquant ne doit pas empêcher d'afficher l'autre")
	}
	f.Err = errors.New("hors ligne")
	if rec := do(s, http.MethodGet, "/markets/xi-out", nil); rec.Code != http.StatusBadGateway {
		t.Errorf("Polymarket injoignable = %d, veut 502", rec.Code)
	}
}

func TestMarketNote(t *testing.T) {
	s, _, store := newMarketsServer(t)
	if rec := do(s, http.MethodPost, "/markets/xi-out/note", url.Values{"note": {"x"}}); rec.Code != http.StatusNotFound {
		t.Errorf("note sur un marché non suivi = %d, veut 404", rec.Code)
	}
	do(s, http.MethodPost, "/markets/xi-out/watch", nil)
	rec := do(s, http.MethodPost, "/markets/xi-out/note", url.Values{"note": {"  j'attends le plénum  "}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/markets/xi-out" {
		t.Fatalf("note = %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	w, _, _ := store.For(tenancy.LocalScope()).GetWatch(context.Background(), "xi-out")
	if w.Note != "j'attends le plénum" {
		t.Errorf("note = %q", w.Note)
	}
	page := do(s, http.MethodGet, "/markets/xi-out", nil).Body.String()
	if !strings.Contains(page, "j&#39;attends le plénum") || !strings.Contains(page, "★ Suivi") {
		t.Error("la fiche doit montrer la note et l'état suivi")
	}
}

// Un marché suivi qui a bougé au-delà de son seuil apparaît dans la barre
// latérale ; les autres non.
func TestSidebar_MarketsThatMoved(t *testing.T) {
	s, _, _ := newMarketsServer(t)
	do(s, http.MethodPost, "/markets/xi-out/watch", nil)  // −0,45 pt : sous le seuil
	do(s, http.MethodPost, "/markets/btc-94k/watch", nil) // +12 pt : au-dessus
	side := do(s, http.MethodGet, "/sidebar", nil).Body.String()
	if !strings.Contains(side, "Marchés qui bougent") || !strings.Contains(side, "Will Bitcoin reach $94,000?") || !strings.Contains(side, "+12,0 pt") {
		t.Errorf("barre latérale sans l'alerte bitcoin")
	}
	if strings.Contains(side, "Xi Jinping") {
		t.Error("un marché sous son seuil ne doit pas être signalé")
	}
}

func TestBuildMarketAlerts(t *testing.T) {
	ws := []trading.Watch{{Slug: "a", Question: "A"}, {Slug: "b", Question: "B", AlertMove: 0.2}, {Slug: "c", Question: "C"}, {Slug: "d", Question: "D"}}
	ms := map[string]polymarket.Market{
		"a": {DayChange: -0.07, Outcomes: []polymarket.Outcome{{Price: 0.4}}},
		"b": {DayChange: 0.1},  // seuil personnalisé de 20 points
		"c": {DayChange: 0.02}, // sous le seuil par défaut
		// d : prix indisponibles, pas d'alerte inventée
	}
	got := buildMarketAlerts(ws, ms)
	if len(got) != 1 || got[0].Title != "A" || got[0].Href != "/markets/a" || !strings.Contains(got[0].Detail, "−7,0 pt") || !strings.Contains(got[0].Detail, "40,0 %") {
		t.Errorf("alertes = %+v", got)
	}
}

// Résultats de recherche : les branches closes sont retirées, les plus
// actives d'abord, 6 au plus par événement (le reste compté).
func TestMarkets_SearchKeepsOpenMostActiveBranches(t *testing.T) {
	s, f, _ := newMarketsServer(t)
	var ms []polymarket.Market
	for i := 0; i < 9; i++ {
		ms = append(ms, polymarket.Market{Slug: fmt.Sprintf("b-%d", i), GroupItemTitle: fmt.Sprintf("Branche %d", i), Volume: float64(i * 1000), Active: true})
	}
	ms[8].Closed = true // la plus active, mais close
	f.Events = []polymarket.Event{{Slug: "fed", Title: "Fed decision", Markets: ms}}
	page := do(s, http.MethodGet, "/markets?q=fed", nil).Body.String()
	if strings.Contains(page, "Branche 8") {
		t.Error("une branche close ne doit pas être proposée")
	}
	if !strings.Contains(page, "Branche 7") || !strings.Contains(page, "Branche 2") || strings.Contains(page, "Branche 1<") || strings.Contains(page, "Branche 0<") {
		t.Error("veut les 6 branches ouvertes les plus actives (7 à 2)")
	}
	if strings.Index(page, "Branche 7") > strings.Index(page, "Branche 2") {
		t.Error("la plus active d'abord")
	}
	if !strings.Contains(page, "+ 2 autres branches") {
		t.Error("les branches masquées doivent être comptées")
	}
}

// La liste de suivi passe avant les résultats de recherche.
func TestMarkets_WatchesBeforeResults(t *testing.T) {
	s, _, _ := newMarketsServer(t)
	do(s, http.MethodPost, "/markets/xi-out/watch", nil)
	page := do(s, http.MethodGet, "/markets?q=bitcoin", nil).Body.String()
	if i, j := strings.Index(page, "Suivis"), strings.Index(page, "Résultats pour"); i < 0 || j < 0 || i > j {
		t.Error("les suivis doivent apparaître avant les résultats")
	}
}
