package polymarket

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// fixture : réponses réelles de l'API (novembre 2026), réduites — voir
// testdata/. Les tests ne dépendent jamais du réseau.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeAPI sert Gamma et le CLOB sur un seul serveur, et note chaque requête.
func fakeAPI(t *testing.T, routes map[string][]byte) (*HTTPClient, *[]*http.Request) {
	t.Helper()
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r)
		body, ok := routes[r.URL.Path]
		if !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return &HTTPClient{GammaURL: srv.URL + "/gamma", ClobURL: srv.URL + "/clob", HTTP: srv.Client()}, &seen
}

func TestHTTPClient_Market_DecodesStringEncodedFields(t *testing.T) {
	c, seen := fakeAPI(t, map[string][]byte{"/gamma/markets": fixture(t, "markets.json")})
	m, ok, err := c.Market(context.Background(), "xi-jinping-out-before-2027")
	if err != nil || !ok {
		t.Fatalf("Market = %v, %v", ok, err)
	}
	if got := (*seen)[0].URL.Query().Get("slug"); got != "xi-jinping-out-before-2027" {
		t.Errorf("slug envoyé = %q", got)
	}
	if m.Question != "Xi Jinping out before 2027?" || m.Slug != "xi-jinping-out-before-2027" {
		t.Errorf("market = %+v", m)
	}
	// outcomes, outcomePrices et clobTokenIds arrivent comme des chaînes
	// contenant un tableau JSON : ils doivent être réunis par issue.
	if len(m.Outcomes) != 2 || m.Outcomes[0].Name != "Yes" || m.Outcomes[1].Name != "No" {
		t.Fatalf("outcomes = %+v", m.Outcomes)
	}
	if m.Outcomes[0].Price != 0.0235 || m.Outcomes[1].Price != 0.9765 {
		t.Errorf("prix = %v / %v", m.Outcomes[0].Price, m.Outcomes[1].Price)
	}
	if !strings.HasPrefix(m.Outcomes[0].TokenID, "3233822019") || m.Outcomes[0].TokenID == m.Outcomes[1].TokenID {
		t.Errorf("token ids = %q / %q", m.Outcomes[0].TokenID, m.Outcomes[1].TokenID)
	}
	if m.Volume < 1e6 || m.Liquidity <= 0 || m.TickSize != 0.001 || m.MinOrderSize != 5 {
		t.Errorf("volume=%v liquidity=%v tick=%v min=%v", m.Volume, m.Liquidity, m.TickSize, m.MinOrderSize)
	}
	if !m.EndDate.Equal(time.Date(2027, 1, 1, 4, 59, 0, 0, time.UTC)) {
		t.Errorf("end = %v", m.EndDate)
	}
	if !m.Active || m.Closed || !m.AcceptingOrders {
		t.Errorf("états = active %v closed %v accepting %v", m.Active, m.Closed, m.AcceptingOrders)
	}
	if !m.Fees.Enabled || m.Fees.Rate != 0.04 || !m.Fees.TakerOnly {
		t.Errorf("frais = %+v", m.Fees)
	}
	if m.EventSlug != "xi-jinping-out-before-2027" || m.EventTitle == "" {
		t.Errorf("événement = %q / %q", m.EventSlug, m.EventTitle)
	}
	if m.Description == "" || m.BestBid != 0.023 || m.BestAsk != 0.024 {
		t.Errorf("description/bid/ask = %q %v %v", m.Description, m.BestBid, m.BestAsk)
	}
	if m.URL() != "https://polymarket.com/event/xi-jinping-out-before-2027" {
		t.Errorf("URL = %q", m.URL())
	}
}

func TestHTTPClient_Market_UnknownSlugIsNotFoundNotError(t *testing.T) {
	c, _ := fakeAPI(t, map[string][]byte{"/gamma/markets": []byte(`[]`)})
	_, ok, err := c.Market(context.Background(), "absent")
	if ok || err != nil {
		t.Fatalf("Market(absent) = %v, %v ; veut introuvable sans erreur", ok, err)
	}
}

// Un identifiant venu d'une URL de Jarvis ne doit jamais pouvoir changer
// la requête envoyée à Polymarket.
func TestHTTPClient_RejectsMalformedIdentifiers(t *testing.T) {
	c, seen := fakeAPI(t, nil)
	ctx := context.Background()
	for _, slug := range []string{"", "a/b", "../x", "a?b=c", "A B", "x&closed=true"} {
		if _, _, err := c.Market(ctx, slug); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Market(%q) = %v, veut ErrInvalidID", slug, err)
		}
	}
	for _, tok := range []string{"", "12a", "1 2", "-1"} {
		if _, err := c.Book(ctx, tok); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Book(%q) = %v, veut ErrInvalidID", tok, err)
		}
		if _, err := c.History(ctx, tok, Interval1W); !errors.Is(err, ErrInvalidID) {
			t.Errorf("History(%q) = %v, veut ErrInvalidID", tok, err)
		}
	}
	if len(*seen) != 0 {
		t.Errorf("%d requêtes envoyées, veut 0", len(*seen))
	}
}

func TestHTTPClient_Search_EventsWithTheirMarkets(t *testing.T) {
	c, seen := fakeAPI(t, map[string][]byte{"/gamma/public-search": fixture(t, "search.json")})
	events, err := c.Search(context.Background(), "bitcoin & co")
	if err != nil {
		t.Fatal(err)
	}
	q := (*seen)[0].URL.Query()
	if q.Get("q") != "bitcoin & co" {
		t.Errorf("q envoyé = %q (doit être échappé, pas coupé)", q.Get("q"))
	}
	if q.Get("events_status") != "active" {
		t.Errorf("events_status = %q, veut active : on ne trade pas un marché clos", q.Get("events_status"))
	}
	if len(events) != 2 {
		t.Fatalf("events = %d", len(events))
	}
	e := events[0]
	if e.Slug == "" || e.Title == "" || len(e.Markets) == 0 {
		t.Fatalf("event = %+v", e)
	}
	if m := e.Markets[0]; m.Question == "" || len(m.Outcomes) != 2 || m.EventSlug != e.Slug {
		t.Errorf("market = %+v", m)
	}
}

func TestHTTPClient_Search_EmptyQueryDoesNothing(t *testing.T) {
	c, seen := fakeAPI(t, nil)
	events, err := c.Search(context.Background(), "   ")
	if err != nil || len(events) != 0 || len(*seen) != 0 {
		t.Fatalf("Search(vide) = %v, %v, %d requêtes", events, err, len(*seen))
	}
}

// Le CLOB renvoie les achats du moins cher au plus cher et les ventes du
// plus cher au moins cher (constaté sur l'API réelle) : le meilleur prix
// est à la fin. Le Book de Jarvis met toujours le meilleur prix en tête.
func TestHTTPClient_Book_BestPriceFirst(t *testing.T) {
	c, seen := fakeAPI(t, map[string][]byte{"/clob/book": fixture(t, "book.json")})
	tok := "32338220190071351435772801779725302244575775216413325951443816017994629993401"
	b, err := c.Book(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if (*seen)[0].URL.Query().Get("token_id") != tok {
		t.Errorf("token_id envoyé = %q", (*seen)[0].URL.Query().Get("token_id"))
	}
	if len(b.Bids) != 6 || len(b.Asks) != 6 {
		t.Fatalf("niveaux = %d / %d", len(b.Bids), len(b.Asks))
	}
	if b.Bids[0].Price != 0.023 || b.Asks[0].Price != 0.024 {
		t.Errorf("meilleurs prix = %v / %v", b.Bids[0].Price, b.Asks[0].Price)
	}
	for i := 1; i < len(b.Bids); i++ {
		if b.Bids[i].Price >= b.Bids[i-1].Price {
			t.Errorf("achats non décroissants : %+v", b.Bids)
		}
	}
	for i := 1; i < len(b.Asks); i++ {
		if b.Asks[i].Price <= b.Asks[i-1].Price {
			t.Errorf("ventes non croissantes : %+v", b.Asks)
		}
	}
	if b.At.IsZero() || b.TokenID != tok {
		t.Errorf("at=%v token=%q", b.At, b.TokenID)
	}
}

func TestHTTPClient_History(t *testing.T) {
	c, seen := fakeAPI(t, map[string][]byte{"/clob/prices-history": fixture(t, "history.json")})
	pts, err := c.History(context.Background(), "123", Interval1W)
	if err != nil {
		t.Fatal(err)
	}
	q := (*seen)[0].URL.Query()
	if q.Get("market") != "123" || q.Get("interval") != "1w" || q.Get("fidelity") == "" {
		t.Errorf("requête = %v", q)
	}
	if len(pts) != 8 || pts[0].Price != 0.0285 || pts[0].At.Unix() != 1792994440 {
		t.Fatalf("points = %+v", pts)
	}
	if _, err := c.History(context.Background(), "123", Interval("5y")); !errors.Is(err, ErrInvalidID) {
		t.Errorf("intervalle inconnu = %v, veut ErrInvalidID", err)
	}
}

func TestHTTPClient_HTTPErrorIsExplicit(t *testing.T) {
	c, _ := fakeAPI(t, nil)
	_, err := c.Book(context.Background(), "123")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, veut le code HTTP", err)
	}
}

func TestHTTPClient_MalformedJSONIsAnError(t *testing.T) {
	c, _ := fakeAPI(t, map[string][]byte{"/gamma/markets": []byte(`[{"outcomes":"pas du json"}]`)})
	if _, _, err := c.Market(context.Background(), "x"); err == nil {
		t.Fatal("veut une erreur")
	}
	// Des issues et des prix qui ne se correspondent pas : on refuse plutôt
	// que d'associer un prix à la mauvaise issue.
	c, _ = fakeAPI(t, map[string][]byte{"/gamma/markets": []byte(`[{"slug":"x","outcomes":"[\"Yes\",\"No\"]","outcomePrices":"[\"0.5\"]","clobTokenIds":"[\"1\",\"2\"]"}]`)})
	if _, _, err := c.Market(context.Background(), "x"); err == nil {
		t.Fatal("issues et prix de longueurs différentes : veut une erreur")
	}
}

func TestBook_MidAndSpread(t *testing.T) {
	b := Book{Bids: []Level{{0.40, 10}, {0.39, 5}}, Asks: []Level{{0.44, 3}}}
	mid, ok := b.Mid()
	if !ok || mid < 0.4199 || mid > 0.4201 {
		t.Errorf("Mid = %v, %v", mid, ok)
	}
	if s, ok := b.Spread(); !ok || s < 0.0399 || s > 0.0401 {
		t.Errorf("Spread = %v, %v", s, ok)
	}
	if _, ok := (Book{Bids: b.Bids}).Mid(); ok {
		t.Error("sans vente, pas de milieu")
	}
}

func TestHTTPClient_EventsByTag(t *testing.T) {
	c, seen := fakeAPI(t, map[string][]byte{"/gamma/events": fixture(t, "events.json")})
	events, err := c.EventsByTag(context.Background(), "fed", 5)
	if err != nil {
		t.Fatal(err)
	}
	q := (*seen)[0].URL.Query()
	if q.Get("tag_slug") != "fed" || q.Get("active") != "true" || q.Get("closed") != "false" || q.Get("order") != "volume24hr" || q.Get("ascending") != "false" || q.Get("limit") != "5" {
		t.Errorf("requête = %v", q)
	}
	if len(events) != 2 || len(events[0].Markets) != 4 || events[0].Markets[0].EventSlug != events[0].Slug || events[0].Markets[0].Volume24h == 0 {
		t.Fatalf("events = %+v", events)
	}
	if _, err := c.EventsByTag(context.Background(), "Pas Valide", 5); !errors.Is(err, ErrInvalidID) {
		t.Errorf("tag invalide = %v", err)
	}
}

// L'historique à la minute d'une plage (constaté sur l'API réelle : une
// minute de résolution jusqu'à 60 jours en arrière, plage de 7 jours au
// plus, refus au-delà).
func TestHTTPClient_HistoryRange(t *testing.T) {
	c, seen := fakeAPI(t, map[string][]byte{"/clob/prices-history": fixture(t, "history.json")})
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	pts, err := c.HistoryRange(context.Background(), "123", from, from.Add(24*time.Hour))
	if err != nil || len(pts) != 8 {
		t.Fatalf("%d %v", len(pts), err)
	}
	q := (*seen)[0].URL.Query()
	if q.Get("market") != "123" || q.Get("fidelity") != "1" || q.Get("startTs") != fmt.Sprint(from.Unix()) || q.Get("endTs") != fmt.Sprint(from.Add(24*time.Hour).Unix()) {
		t.Errorf("requête = %v", q)
	}
	if _, err := c.HistoryRange(context.Background(), "123", from, from.Add(8*24*time.Hour)); err == nil {
		t.Error("une plage de plus de 7 jours doit être refusée avant l'appel")
	}
	if _, err := c.HistoryRange(context.Background(), "123", from, from); err == nil {
		t.Error("une plage vide doit être refusée")
	}
}
