package polymarket

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultGammaURL = "https://gamma-api.polymarket.com"
	DefaultClobURL  = "https://clob.polymarket.com"
	// maxBody : une réponse plus grosse est refusée plutôt que lue en
	// mémoire sans limite.
	maxBody = 16 << 20
)

// HTTPClient lit l'API publique de Polymarket. GammaURL et ClobURL vides
// prennent les adresses officielles.
type HTTPClient struct {
	GammaURL string
	ClobURL  string
	HTTP     *http.Client
}

func (c *HTTPClient) gamma() string {
	if c.GammaURL == "" {
		return DefaultGammaURL
	}
	return strings.TrimRight(c.GammaURL, "/")
}

func (c *HTTPClient) clob() string {
	if c.ClobURL == "" {
		return DefaultClobURL
	}
	return strings.TrimRight(c.ClobURL, "/")
}

func (c *HTTPClient) get(ctx context.Context, base, path string, q url.Values, out any) error {
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("polymarket: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("polymarket: GET %s : %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return fmt.Errorf("polymarket: GET %s : lecture : %w", path, err)
	}
	if len(body) > maxBody {
		return fmt.Errorf("polymarket: GET %s : réponse de plus de %d octets", path, maxBody)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		return fmt.Errorf("polymarket: GET %s : HTTP %d : %s", path, resp.StatusCode, snippet)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("polymarket: GET %s : réponse illisible : %w", path, err)
	}
	return nil
}

func (c *HTTPClient) Search(ctx context.Context, query string) ([]Event, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if len(query) > 200 {
		query = query[:200]
	}
	q := url.Values{"q": {query}, "limit_per_type": {"10"}, "events_status": {"active"}}
	var resp struct {
		Events []gammaEvent `json:"events"`
	}
	if err := c.get(ctx, c.gamma(), "/public-search", q, &resp); err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(resp.Events))
	for _, ge := range resp.Events {
		e := Event{ID: ge.ID, Slug: ge.Slug, Title: ge.Title, EndDate: parseTime(ge.EndDate), Volume: num(ge.Volume)}
		for _, gm := range ge.Markets {
			m, err := gm.market()
			if err != nil {
				return nil, err
			}
			if m.EventSlug == "" {
				m.EventSlug, m.EventTitle = e.Slug, e.Title
			}
			e.Markets = append(e.Markets, m)
		}
		out = append(out, e)
	}
	return out, nil
}

func (c *HTTPClient) Market(ctx context.Context, slug string) (Market, bool, error) {
	if !ValidSlug(slug) {
		return Market{}, false, fmt.Errorf("%w : slug %q", ErrInvalidID, slug)
	}
	var resp []gammaMarket
	if err := c.get(ctx, c.gamma(), "/markets", url.Values{"slug": {slug}}, &resp); err != nil {
		return Market{}, false, err
	}
	if len(resp) == 0 {
		return Market{}, false, nil
	}
	m, err := resp[0].market()
	if err != nil {
		return Market{}, false, err
	}
	return m, true, nil
}

func (c *HTTPClient) Book(ctx context.Context, tokenID string) (Book, error) {
	if !ValidTokenID(tokenID) {
		return Book{}, fmt.Errorf("%w : jeton %q", ErrInvalidID, tokenID)
	}
	var resp struct {
		AssetID   string       `json:"asset_id"`
		Timestamp string       `json:"timestamp"`
		Bids      []gammaLevel `json:"bids"`
		Asks      []gammaLevel `json:"asks"`
	}
	if err := c.get(ctx, c.clob(), "/book", url.Values{"token_id": {tokenID}}, &resp); err != nil {
		return Book{}, err
	}
	b := Book{TokenID: tokenID, Bids: levels(resp.Bids), Asks: levels(resp.Asks)}
	if ms, err := strconv.ParseInt(resp.Timestamp, 10, 64); err == nil {
		b.At = time.UnixMilli(ms).UTC()
	}
	// L'ordre renvoyé par le CLOB n'est pas documenté comme garanti : on
	// trie plutôt que de s'y fier.
	sort.SliceStable(b.Bids, func(i, j int) bool { return b.Bids[i].Price > b.Bids[j].Price })
	sort.SliceStable(b.Asks, func(i, j int) bool { return b.Asks[i].Price < b.Asks[j].Price })
	return b, nil
}

func (c *HTTPClient) History(ctx context.Context, tokenID string, iv Interval) ([]PricePoint, error) {
	if !ValidTokenID(tokenID) {
		return nil, fmt.Errorf("%w : jeton %q", ErrInvalidID, tokenID)
	}
	if !ValidInterval(iv) {
		return nil, fmt.Errorf("%w : intervalle %q", ErrInvalidID, iv)
	}
	var resp struct {
		History []struct {
			T int64   `json:"t"`
			P float64 `json:"p"`
		} `json:"history"`
	}
	q := url.Values{"market": {tokenID}, "interval": {string(iv)}, "fidelity": {fidelity[iv]}}
	if err := c.get(ctx, c.clob(), "/prices-history", q, &resp); err != nil {
		return nil, err
	}
	out := make([]PricePoint, 0, len(resp.History))
	for _, h := range resp.History {
		out = append(out, PricePoint{At: time.Unix(h.T, 0).UTC(), Price: h.P})
	}
	return out, nil
}

// --- Décodage des réponses -------------------------------------------------

// flexNum : Gamma écrit certains nombres en chaîne (« "327952.64" ») et
// d'autres en nombre ; les deux sont acceptés.
type flexNum float64

func (f *flexNum) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("nombre illisible %s", b)
	}
	*f = flexNum(v)
	return nil
}

func num(f flexNum) float64 { return float64(f) }

// jsonList : Gamma encode outcomes, outcomePrices et clobTokenIds comme une
// chaîne contenant un tableau JSON.
type jsonList []string

func (l *jsonList) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*l = nil
		return nil
	}
	var raw string
	if err := json.Unmarshal(b, &raw); err != nil {
		// Déjà un vrai tableau : accepté aussi.
		var arr []string
		if err2 := json.Unmarshal(b, &arr); err2 != nil {
			return fmt.Errorf("liste illisible %s", b)
		}
		*l = arr
		return nil
	}
	if raw == "" {
		*l = nil
		return nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return fmt.Errorf("liste illisible %q", raw)
	}
	*l = arr
	return nil
}

type gammaLevel struct {
	Price flexNum `json:"price"`
	Size  flexNum `json:"size"`
}

func levels(in []gammaLevel) []Level {
	out := make([]Level, 0, len(in))
	for _, l := range in {
		out = append(out, Level{Price: num(l.Price), Size: num(l.Size)})
	}
	return out
}

type gammaEvent struct {
	ID      string        `json:"id"`
	Slug    string        `json:"slug"`
	Title   string        `json:"title"`
	EndDate string        `json:"endDate"`
	Volume  flexNum       `json:"volume"`
	Markets []gammaMarket `json:"markets"`
}

type gammaMarket struct {
	ID               string   `json:"id"`
	Slug             string   `json:"slug"`
	Question         string   `json:"question"`
	ConditionID      string   `json:"conditionId"`
	Description      string   `json:"description"`
	ResolutionSource string   `json:"resolutionSource"`
	GroupItemTitle   string   `json:"groupItemTitle"`
	EndDate          string   `json:"endDate"`
	Outcomes         jsonList `json:"outcomes"`
	OutcomePrices    jsonList `json:"outcomePrices"`
	ClobTokenIDs     jsonList `json:"clobTokenIds"`
	VolumeNum        flexNum  `json:"volumeNum"`
	Volume           flexNum  `json:"volume"`
	Volume24h        flexNum  `json:"volume24hr"`
	LiquidityNum     flexNum  `json:"liquidityNum"`
	BestBid          flexNum  `json:"bestBid"`
	BestAsk          flexNum  `json:"bestAsk"`
	LastTrade        flexNum  `json:"lastTradePrice"`
	Spread           flexNum  `json:"spread"`
	DayChange        flexNum  `json:"oneDayPriceChange"`
	Active           bool     `json:"active"`
	Closed           bool     `json:"closed"`
	AcceptingOrders  bool     `json:"acceptingOrders"`
	NegRisk          bool     `json:"negRisk"`
	Restricted       bool     `json:"restricted"`
	TickSize         flexNum  `json:"orderPriceMinTickSize"`
	MinOrderSize     flexNum  `json:"orderMinSize"`
	FeesEnabled      bool     `json:"feesEnabled"`
	FeeSchedule      *struct {
		Exponent  flexNum `json:"exponent"`
		Rate      flexNum `json:"rate"`
		TakerOnly bool    `json:"takerOnly"`
	} `json:"feeSchedule"`
	Events []struct {
		Slug  string `json:"slug"`
		Title string `json:"title"`
	} `json:"events"`
}

func (g gammaMarket) market() (Market, error) {
	m := Market{
		ID: g.ID, Slug: g.Slug, Question: g.Question, ConditionID: g.ConditionID,
		Description: g.Description, ResolutionSource: g.ResolutionSource, GroupItemTitle: g.GroupItemTitle,
		EndDate:   parseTime(g.EndDate),
		Volume:    num(g.VolumeNum),
		Volume24h: num(g.Volume24h), Liquidity: num(g.LiquidityNum),
		BestBid: num(g.BestBid), BestAsk: num(g.BestAsk), LastTrade: num(g.LastTrade),
		Spread: num(g.Spread), DayChange: num(g.DayChange),
		Active: g.Active, Closed: g.Closed, AcceptingOrders: g.AcceptingOrders, NegRisk: g.NegRisk, Restricted: g.Restricted,
		TickSize: num(g.TickSize), MinOrderSize: num(g.MinOrderSize),
	}
	if m.Volume == 0 {
		m.Volume = num(g.Volume)
	}
	if g.FeeSchedule != nil {
		m.Fees = FeeSchedule{Enabled: g.FeesEnabled, Rate: num(g.FeeSchedule.Rate), Exponent: num(g.FeeSchedule.Exponent), TakerOnly: g.FeeSchedule.TakerOnly}
	} else {
		m.Fees.Enabled = g.FeesEnabled
	}
	if len(g.Events) > 0 {
		m.EventSlug, m.EventTitle = g.Events[0].Slug, g.Events[0].Title
	}
	// Issues, prix et jetons vont par position : s'ils ne correspondent pas,
	// un prix serait attribué à la mauvaise issue — on refuse.
	if len(g.OutcomePrices) != 0 && len(g.OutcomePrices) != len(g.Outcomes) {
		return Market{}, fmt.Errorf("polymarket: marché %s : %d issues pour %d prix", g.Slug, len(g.Outcomes), len(g.OutcomePrices))
	}
	if len(g.ClobTokenIDs) != 0 && len(g.ClobTokenIDs) != len(g.Outcomes) {
		return Market{}, fmt.Errorf("polymarket: marché %s : %d issues pour %d jetons", g.Slug, len(g.Outcomes), len(g.ClobTokenIDs))
	}
	for i, name := range g.Outcomes {
		o := Outcome{Name: name}
		if i < len(g.OutcomePrices) {
			p, err := strconv.ParseFloat(g.OutcomePrices[i], 64)
			if err != nil {
				return Market{}, fmt.Errorf("polymarket: marché %s : prix illisible %q", g.Slug, g.OutcomePrices[i])
			}
			o.Price = p
		}
		if i < len(g.ClobTokenIDs) {
			o.TokenID = g.ClobTokenIDs[i]
		}
		m.Outcomes = append(m.Outcomes, o)
	}
	return m, nil
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05Z07:00", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
