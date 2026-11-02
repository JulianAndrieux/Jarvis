//go:build polymarket

package polymarket

import (
	"context"
	"testing"
	"time"
)

// Contre la vraie API publique (réseau requis) : go test -tags=polymarket
// ./internal/polymarket/. Vérifie que le format décodé par HTTPClient est
// toujours celui que Polymarket sert.
func TestLive_SearchMarketBookHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := &HTTPClient{}
	events, err := c.Search(ctx, "bitcoin")
	if err != nil || len(events) == 0 {
		t.Fatalf("Search = %d, %v", len(events), err)
	}
	var m Market
	for _, e := range events {
		for _, x := range e.Markets {
			if x.AcceptingOrders && len(x.Outcomes) == 2 && x.Outcomes[0].TokenID != "" {
				m = x
				break
			}
		}
		if m.Slug != "" {
			break
		}
	}
	if m.Slug == "" {
		t.Fatal("aucun marché ouvert dans les résultats")
	}
	got, ok, err := c.Market(ctx, m.Slug)
	if err != nil || !ok || got.Question == "" || len(got.Outcomes) != 2 {
		t.Fatalf("Market(%s) = %+v, %v, %v", m.Slug, got, ok, err)
	}
	b, err := c.Book(ctx, got.Outcomes[0].TokenID)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Bids) > 1 && b.Bids[0].Price < b.Bids[1].Price {
		t.Errorf("achats mal triés : %v", b.Bids[:2])
	}
	if len(b.Asks) > 1 && b.Asks[0].Price > b.Asks[1].Price {
		t.Errorf("ventes mal triées : %v", b.Asks[:2])
	}
	if s, ok := b.Spread(); ok && s < 0 {
		t.Errorf("carnet croisé : écart %v", s)
	}
	pts, err := c.History(ctx, got.Outcomes[0].TokenID, Interval1D)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s : %s %.3f, %d achats / %d ventes, %d points sur 24 h", got.Slug, got.Outcomes[0].Name, got.Outcomes[0].Price, len(b.Bids), len(b.Asks), len(pts))
}
