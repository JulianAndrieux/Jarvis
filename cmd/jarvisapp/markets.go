package main

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/polymarket"
	"github.com/JulianAndrieux/Jarvis/internal/trading"
)

// Jalon 53 : marchés Polymarket en lecture seule. Rien ici ne passe un
// ordre ; Jarvis n'a ni compte ni clé Polymarket.

// marketsFetchTimeout : au-delà, une ligne de la liste de suivi s'affiche
// « prix indisponibles » plutôt que de faire attendre toute la page.
const marketsFetchTimeout = 8 * time.Second

// sidebarMarketsTimeout : la barre latérale passe avant tout le reste de
// la page ; un Polymarket lent ne doit pas la retenir.
const sidebarMarketsTimeout = 3 * time.Second

// maxMarketNote : une note de suivi est un pense-bête, pas un document.
const maxMarketNote = 2000

func (s *Server) marketRoutes(r chi.Router) {
	r.Get("/markets", s.handleMarkets)
	r.Get("/markets/{slug}", s.handleMarketDetail)
	r.Post("/markets/{slug}/watch", s.handleMarketWatch)
	r.Post("/markets/{slug}/unwatch", s.handleMarketUnwatch)
	r.Post("/markets/{slug}/note", s.handleMarketNote)
}

func (s *Server) marketsEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.Market == nil || s.Trading == nil {
		http.NotFound(w, r)
		return false
	}
	return true
}

func (s *Server) watches(r *http.Request) trading.Store { return s.Trading.For(scopeOf(r)) }

// slugParam : le slug de l'URL, refusé (400) s'il n'a pas la forme d'un
// slug Polymarket — il partirait sinon tel quel vers l'API.
func slugParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	slug := chi.URLParam(r, "slug")
	if !polymarket.ValidSlug(slug) {
		http.Error(w, "identifiant de marché invalide", http.StatusBadRequest)
		return "", false
	}
	return slug, true
}

func (s *Server) handleMarkets(w http.ResponseWriter, r *http.Request) {
	if !s.marketsEnabled(w, r) {
		return
	}
	ctx := r.Context()
	v := templates.MarketsView{Query: strings.TrimSpace(r.URL.Query().Get("q")), Watched: map[string]bool{}}
	ws, err := s.watches(r).ListWatches(ctx)
	if err != nil {
		v.Error = "Liste de suivi illisible : " + err.Error()
	}
	markets, errs := s.fetchMarkets(ctx, ws, marketsFetchTimeout)
	for _, wt := range ws {
		v.Watched[wt.Slug] = true
		row := templates.WatchRow{Slug: wt.Slug, Question: wt.Question, EventTitle: wt.EventTitle, Note: wt.Note}
		if m, ok := markets[wt.Slug]; ok {
			row.Market = m
			row.Alert = moved(wt, m)
		} else {
			row.Err = errs[wt.Slug]
		}
		v.Watches = append(v.Watches, row)
	}
	if v.Query != "" {
		v.Searched = true
		events, err := s.Market.Search(ctx, v.Query)
		if err != nil {
			v.SearchError = err.Error()
		}
		v.Results = searchResults(events, maxBranches)
	}
	renderPage(w, r, http.StatusOK, templates.MarketsPage(v), "markets")
}

// maxBranches : branches affichées par événement trouvé. Un événement
// « combien de baisses de taux » en a quinze ; on ne trade que les plus
// actives, les autres restent sur Polymarket.
const maxBranches = 6

// searchResults retire les branches closes (rien à y trader), garde les
// plus actives d'abord et compte celles qui ne sont pas affichées. Un
// événement sans branche ouverte disparaît.
func searchResults(events []polymarket.Event, max int) []templates.ResultEvent {
	var out []templates.ResultEvent
	for _, e := range events {
		var open []polymarket.Market
		for _, m := range e.Markets {
			if !m.Closed {
				open = append(open, m)
			}
		}
		if len(open) == 0 {
			continue
		}
		sort.SliceStable(open, func(i, j int) bool { return open[i].Volume > open[j].Volume })
		r := templates.ResultEvent{Event: e}
		if len(open) > max {
			r.More = len(open) - max
			open = open[:max]
		}
		r.Event.Markets = open
		out = append(out, r)
	}
	return out
}

// fetchMarkets relit les marchés suivis en parallèle (6 à la fois), dans
// la limite du délai. Un marché absent du résultat a son erreur dans errs.
func (s *Server) fetchMarkets(ctx context.Context, ws []trading.Watch, timeout time.Duration) (map[string]polymarket.Market, map[string]string) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		markets = map[string]polymarket.Market{}
		errs    = map[string]string{}
		sem     = make(chan struct{}, 6)
	)
	for _, wt := range ws {
		wg.Add(1)
		go func(slug string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			m, ok, err := s.Market.Market(ctx, slug)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				errs[slug] = err.Error()
			case !ok:
				errs[slug] = "marché introuvable sur Polymarket"
			default:
				markets[slug] = m
			}
		}(wt.Slug)
	}
	wg.Wait()
	return markets, errs
}

// moved : la variation sur 24 h dépasse le seuil du suivi.
func moved(w trading.Watch, m polymarket.Market) bool {
	d := m.DayChange
	if d < 0 {
		d = -d
	}
	return d >= w.Threshold()
}

// buildMarketAlerts : les marchés suivis qui ont bougé au-delà de leur
// seuil, pour la barre latérale. Un marché dont le prix n'a pas pu être lu
// n'est jamais signalé.
func buildMarketAlerts(ws []trading.Watch, markets map[string]polymarket.Market) []templates.SideItem {
	var out []templates.SideItem
	for _, w := range ws {
		m, ok := markets[w.Slug]
		if !ok || !moved(w, m) {
			continue
		}
		detail := templates.Change(m.DayChange) + " en 24 h"
		if len(m.Outcomes) > 0 {
			detail = templates.Pct(m.Outcomes[0].Price) + " · " + detail
		}
		out = append(out, templates.SideItem{Href: "/markets/" + w.Slug, Icon: "📈", Title: w.Question, Detail: detail})
		if len(out) == sidebarMax {
			break
		}
	}
	return out
}

// sidebarMarkets : le bloc « Marchés qui bougent » de la barre latérale.
func (s *Server) sidebarMarkets(r *http.Request) []templates.SideItem {
	if s.Market == nil || s.Trading == nil {
		return nil
	}
	ws, err := s.watches(r).ListWatches(r.Context())
	if err != nil || len(ws) == 0 {
		return nil
	}
	if len(ws) > 30 {
		ws = ws[:30]
	}
	markets, _ := s.fetchMarkets(r.Context(), ws, sidebarMarketsTimeout)
	return buildMarketAlerts(ws, markets)
}

func (s *Server) handleMarketDetail(w http.ResponseWriter, r *http.Request) {
	if !s.marketsEnabled(w, r) {
		return
	}
	slug, ok := slugParam(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	m, found, err := s.Market.Market(ctx, slug)
	if err != nil {
		http.Error(w, "Polymarket n'a pas répondu : "+err.Error(), http.StatusBadGateway)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	iv := polymarket.Interval(r.URL.Query().Get("iv"))
	if !polymarket.ValidInterval(iv) {
		iv = polymarket.Interval1W
	}
	v := templates.MarketDetailView{Market: m, Interval: iv}
	if wt, ok, err := s.watches(r).GetWatch(ctx, slug); err == nil && ok {
		v.Watched, v.Note = true, wt.Note
	}

	// Carnets et historique en parallèle : quatre allers-retours vers
	// Polymarket, qu'on n'enchaîne pas.
	fctx, cancel := context.WithTimeout(ctx, marketsFetchTimeout)
	defer cancel()
	var wg sync.WaitGroup
	v.Books = make([]templates.OutcomeBook, len(m.Outcomes))
	for i, o := range m.Outcomes {
		v.Books[i].Outcome = o
		if o.TokenID == "" {
			v.Books[i].Err = "pas de carnet d'ordres pour cette issue"
			continue
		}
		wg.Add(1)
		go func(i int, tok string) {
			defer wg.Done()
			b, err := s.Market.Book(fctx, tok)
			if err != nil {
				v.Books[i].Err = err.Error()
				return
			}
			v.Books[i].Book = b
		}(i, o.TokenID)
	}
	if len(m.Outcomes) > 0 && m.Outcomes[0].TokenID != "" {
		v.HistoryOf = m.Outcomes[0].Name
		wg.Add(1)
		go func(tok string) {
			defer wg.Done()
			pts, err := s.Market.History(fctx, tok, iv)
			if err != nil {
				v.HistoryErr = err.Error()
				return
			}
			v.History = pts
		}(m.Outcomes[0].TokenID)
	} else {
		v.HistoryErr = "pas d'historique pour ce marché"
	}
	wg.Wait()
	v.Chart = templates.PriceChart(v.History, 600, 160)
	renderPage(w, r, http.StatusOK, templates.MarketDetailPage(v), "market")
}

func (s *Server) handleMarketWatch(w http.ResponseWriter, r *http.Request) {
	if !s.marketsEnabled(w, r) {
		return
	}
	slug, ok := slugParam(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	// Le marché est relu chez Polymarket plutôt que d'en croire le
	// formulaire : on ne suit que ce qui existe, sous son vrai intitulé.
	m, found, err := s.Market.Market(ctx, slug)
	if err != nil {
		http.Error(w, "Polymarket n'a pas répondu : "+err.Error(), http.StatusBadGateway)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	scope := scopeOf(r)
	err = s.watches(r).AddWatch(ctx, trading.Watch{
		Slug: slug, Question: m.Question, EventTitle: m.EventTitle,
		AddedAt: time.Now().UTC(), AddedBy: scope.User,
	})
	if err != nil && !errors.Is(err, trading.ErrAlreadyWatched) {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, localBack(r.FormValue("back"), "/markets"), http.StatusSeeOther)
}

func (s *Server) handleMarketUnwatch(w http.ResponseWriter, r *http.Request) {
	if !s.marketsEnabled(w, r) {
		return
	}
	slug, ok := slugParam(w, r)
	if !ok {
		return
	}
	if err := s.watches(r).RemoveWatch(r.Context(), slug); err != nil && !errors.Is(err, trading.ErrNotFound) {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, localBack(r.FormValue("back"), "/markets"), http.StatusSeeOther)
}

func (s *Server) handleMarketNote(w http.ResponseWriter, r *http.Request) {
	if !s.marketsEnabled(w, r) {
		return
	}
	slug, ok := slugParam(w, r)
	if !ok {
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if len([]rune(note)) > maxMarketNote {
		http.Error(w, "note trop longue", http.StatusBadRequest)
		return
	}
	err := s.watches(r).SetWatchNote(r.Context(), slug, note)
	if errors.Is(err, trading.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/markets/"+slug, http.StatusSeeOther)
}
