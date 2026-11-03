package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/news"
	"github.com/JulianAndrieux/Jarvis/internal/polymarket"
	"github.com/JulianAndrieux/Jarvis/internal/trading"
)

// CategoryWatched : la catégorie d'une cible venue d'une liste de suivi.
const CategoryWatched = "suivi"

// DefaultTags : les catégories retenues par l'utilisateur (politique et
// géopolitique, économie et banques centrales, crypto), en tags Gamma.
var DefaultTags = []string{"politics", "geopolitics", "economy", "fed", "crypto"}

const (
	// DefaultBackfill : la profondeur de la première collecte d'une cible
	// (prix à la minute et GDELT). L'historique à la minute du CLOB remonte
	// au moins à 60 jours (constaté) ; GDELT à 3 mois.
	DefaultBackfill = 30 * 24 * time.Hour
	// staleAfter : une cible qui n'a pas été revue dans les catégories ni
	// dans une liste de suivi depuis ce délai n'est plus collectée.
	staleAfter = 48 * time.Hour
	// gdeltOverlap : GDELT indexe par tranches de 15 min ; on reprend un
	// peu avant le dernier passage (les doublons sont absorbés).
	gdeltOverlap = 30 * time.Minute
	maxErrors    = 20
)

// FeedReader : les flux RSS (news.RSS).
type FeedReader interface {
	FetchAll(ctx context.Context) ([]news.Article, []error)
}

// Searcher : la recherche GDELT (news.GDELT).
type Searcher interface {
	Search(ctx context.Context, phrases []string, since time.Time) ([]news.Article, error)
}

// KeywordExtractor : les phrases de recherche d'un marché, et le modèle
// qui les a proposées.
type KeywordExtractor interface {
	Keywords(ctx context.Context, t Target, description string) ([]string, string, error)
}

// StatusError : une erreur de collecte, datée.
type StatusError struct {
	At      time.Time
	Message string
}

// Status : l'état de la collecte, pour l'interface.
type Status struct {
	Targets                                       int
	LastTargets, LastPrices, LastFeeds, LastGDELT time.Time
	NewPoints, NewFeedArticles, NewGDELTArticles  int
	Errors                                        []StatusError // la plus récente d'abord
	Running                                       bool
}

// Collector relève prix et actualités des cibles. Chaque passe peut être
// lancée seule (tests, bouton) ; Run les enchaîne à leur rythme.
type Collector struct {
	Store  Store
	Market polymarket.Client // sans cache : on collecte, on n'affiche pas
	Feeds  FeedReader
	GDELT  Searcher
	// Keywords : nil, les cibles restent sans mots-clés (prix seulement).
	Keywords KeywordExtractor
	// Acquire : la file des modèles, prise pour chaque extraction de
	// mots-clés (le LLM local est partagé avec documents et emails).
	Acquire func(ctx context.Context) (func(), error)
	Tags    []string
	PerTag  int
	// Watched : les marchés suivis, ajoutés aux cibles.
	Watched  func(ctx context.Context) ([]trading.Watch, error)
	Backfill time.Duration
	Now      func() time.Time
	Log      func(format string, args ...any)

	mu     sync.Mutex
	status Status
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *Collector) backfill() time.Duration {
	if c.Backfill <= 0 {
		return DefaultBackfill
	}
	return c.Backfill
}

func (c *Collector) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log("collecte : "+format, args...)
	}
}

func (c *Collector) fail(err error) {
	if err == nil {
		return
	}
	c.logf("%v", err)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Errors = append([]StatusError{{At: c.now(), Message: err.Error()}}, c.status.Errors...)
	if len(c.status.Errors) > maxErrors {
		c.status.Errors = c.status.Errors[:maxErrors]
	}
}

func (c *Collector) update(f func(*Status)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f(&c.status)
}

// Status : une copie de l'état.
func (c *Collector) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.status
	s.Errors = append([]StatusError(nil), s.Errors...)
	return s
}

// active : les cibles revues récemment.
func (c *Collector) active(ctx context.Context) ([]Target, error) {
	ts, err := c.Store.Targets(ctx)
	if err != nil {
		return nil, err
	}
	var out []Target
	for _, t := range ts {
		if c.now().Sub(t.UpdatedAt) <= staleAfter && t.TokenID != "" {
			out = append(out, t)
		}
	}
	return out, nil
}

// pickMarket : la branche ouverte la plus active d'un événement. Une
// branche dont l'échéance est passée (en attente de résolution, mais qui
// accepte encore des ordres — vu sur l'API réelle) est écartée.
func pickMarket(e polymarket.Event, now time.Time) (polymarket.Market, bool) {
	var best polymarket.Market
	found := false
	for _, m := range e.Markets {
		if m.Closed || !m.AcceptingOrders || len(m.Outcomes) == 0 || m.Outcomes[0].TokenID == "" {
			continue
		}
		if !m.EndDate.IsZero() && m.EndDate.Before(now) {
			continue
		}
		if !found || m.Volume24h > best.Volume24h {
			best, found = m, true
		}
	}
	return best, found
}

func targetOf(m polymarket.Market, category string, now time.Time) Target {
	return Target{Slug: m.Slug, Question: m.Question, EventTitle: m.EventTitle, Category: category, TokenID: m.Outcomes[0].TokenID, EndDate: m.EndDate, UpdatedAt: now}
}

// RefreshTargets : les marchés suivis puis, pour chaque catégorie, la
// branche la plus active de ses événements les plus actifs. Une catégorie
// ou un suivi en panne n'empêche pas le reste ; les erreurs sont réunies.
func (c *Collector) RefreshTargets(ctx context.Context) (int, error) {
	now := c.now()
	seen := map[string]bool{}
	var errs []error
	n := 0
	add := func(t Target) {
		if seen[t.Slug] {
			return
		}
		seen[t.Slug] = true
		if err := c.Store.UpsertTarget(ctx, t); err != nil {
			errs = append(errs, err)
			return
		}
		n++
	}
	if c.Watched != nil {
		ws, err := c.Watched(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("marchés suivis : %w", err))
		}
		for _, w := range ws {
			m, ok, err := c.Market.Market(ctx, w.Slug)
			switch {
			case err != nil:
				errs = append(errs, fmt.Errorf("suivi %s : %w", w.Slug, err))
			case ok && len(m.Outcomes) > 0 && m.Outcomes[0].TokenID != "":
				add(targetOf(m, CategoryWatched, now))
			}
		}
	}
	per := c.PerTag
	if per <= 0 {
		per = 10
	}
	for _, tag := range c.Tags {
		events, err := c.Market.EventsByTag(ctx, tag, per)
		if err != nil {
			errs = append(errs, fmt.Errorf("catégorie %s : %w", tag, err))
			continue
		}
		for _, e := range events {
			if m, ok := pickMarket(e, now); ok {
				if m.EventTitle == "" {
					m.EventTitle = e.Title
				}
				add(targetOf(m, tag, now))
			}
		}
	}
	c.update(func(s *Status) { s.Targets, s.LastTargets = n, now })
	err := errors.Join(errs...)
	c.fail(err)
	return n, err
}

// CollectPrices : pour chaque cible, l'historique à la minute depuis le
// dernier point connu (ou depuis Backfill), en tranches acceptées par le
// CLOB.
func (c *Collector) CollectPrices(ctx context.Context) (int, error) {
	ts, err := c.active(ctx)
	if err != nil {
		c.fail(err)
		return 0, err
	}
	now := c.now()
	total := 0
	var errs []error
	for _, t := range ts {
		from := now.Add(-c.backfill())
		if last, ok, err := c.Store.LastPrice(ctx, t.TokenID); err != nil {
			errs = append(errs, err)
			continue
		} else if ok && last.After(from) {
			from = last.Add(time.Second)
		}
		for from.Before(now) {
			if ctx.Err() != nil {
				return total, ctx.Err()
			}
			to := from.Add(polymarket.MaxRangeSpan)
			if to.After(now) {
				to = now
			}
			pts, err := c.Market.HistoryRange(ctx, t.TokenID, from, to)
			if err != nil {
				errs = append(errs, fmt.Errorf("prix de %s : %w", t.Slug, err))
				break
			}
			n, err := c.Store.AddPrices(ctx, t.TokenID, pts)
			total += n
			if err != nil {
				errs = append(errs, err)
				break
			}
			from = to
		}
	}
	c.update(func(s *Status) { s.LastPrices, s.NewPoints = now, total })
	err = errors.Join(errs...)
	c.fail(err)
	return total, err
}

// FillKeywords : demande au modèle local les mots-clés des cibles qui
// n'en ont pas, puis rattache à chacune les articles déjà enregistrés
// (période de Backfill) qui les contiennent.
func (c *Collector) FillKeywords(ctx context.Context) (int, error) {
	if c.Keywords == nil {
		return 0, nil
	}
	ts, err := c.active(ctx)
	if err != nil {
		c.fail(err)
		return 0, err
	}
	var errs []error
	n := 0
	for _, t := range ts {
		if len(t.Keywords) > 0 {
			continue
		}
		kw, model, err := c.withModel(ctx, func() ([]string, string, error) {
			desc := ""
			if m, ok, err := c.Market.Market(ctx, t.Slug); err == nil && ok {
				desc = m.Description
			}
			return c.Keywords.Keywords(ctx, t, desc)
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("mots-clés de %s : %w", t.Slug, err))
			if ctx.Err() != nil {
				break
			}
			continue
		}
		if err := c.Store.SetKeywords(ctx, t.Slug, kw, model, c.now()); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
		if err := c.relink(ctx, t.Slug, kw); err != nil {
			errs = append(errs, err)
		}
	}
	err = errors.Join(errs...)
	c.fail(err)
	return n, err
}

func (c *Collector) withModel(ctx context.Context, f func() ([]string, string, error)) ([]string, string, error) {
	if c.Acquire != nil {
		release, err := c.Acquire(ctx)
		if err != nil {
			return nil, "", err
		}
		defer release()
	}
	return f()
}

// relink : les articles déjà enregistrés qui correspondent aux mots-clés.
func (c *Collector) relink(ctx context.Context, slug string, kw []string) error {
	arts, err := c.Store.Articles(ctx, ArticleQuery{From: c.now().Add(-c.backfill()), Limit: 5000})
	if err != nil {
		return err
	}
	var ids []string
	for _, a := range arts {
		if news.Matches(a, kw) {
			ids = append(ids, a.ID)
		}
	}
	return c.Store.LinkArticles(ctx, ids, slug)
}

// CollectFeeds : relève les flux, rattache chaque article aux cibles dont
// il contient les mots-clés, et les enregistre tous (un article qui ne
// concerne aucune cible aujourd'hui pourra concerner celle de demain).
func (c *Collector) CollectFeeds(ctx context.Context) (int, error) {
	if c.Feeds == nil {
		return 0, nil
	}
	ts, err := c.active(ctx)
	if err != nil {
		c.fail(err)
		return 0, err
	}
	arts, fetchErrs := c.Feeds.FetchAll(ctx)
	for i := range arts {
		for _, t := range ts {
			if len(t.Keywords) > 0 && news.Matches(arts[i], t.Keywords) {
				arts[i].Markets = append(arts[i].Markets, t.Slug)
			}
		}
	}
	n, err := c.Store.UpsertArticles(ctx, arts)
	errs := append(fetchErrs, err)
	c.update(func(s *Status) { s.LastFeeds, s.NewFeedArticles = c.now(), n })
	err = errors.Join(errs...)
	c.fail(err)
	return n, err
}

// CollectGDELT : pour chaque cible qui a des mots-clés, les articles
// publiés depuis le dernier passage. GDELT qui refuse (trop de requêtes)
// arrête le passage : le reste attendra le suivant.
func (c *Collector) CollectGDELT(ctx context.Context) (int, error) {
	if c.GDELT == nil {
		return 0, nil
	}
	ts, err := c.active(ctx)
	if err != nil {
		c.fail(err)
		return 0, err
	}
	// Les moins récemment interrogées d'abord : un passage interrompu ne
	// laisse pas toujours les mêmes de côté.
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].GDELTAt.Before(ts[j].GDELTAt) })
	total := 0
	var errs []error
	for _, t := range ts {
		if len(t.Keywords) == 0 {
			continue
		}
		since := c.now().Add(-c.backfill())
		if !t.GDELTAt.IsZero() && t.GDELTAt.Add(-gdeltOverlap).After(since) {
			since = t.GDELTAt.Add(-gdeltOverlap)
		}
		started := c.now()
		arts, err := c.GDELT.Search(ctx, t.Keywords, since)
		if err != nil {
			errs = append(errs, fmt.Errorf("gdelt %s : %w", t.Slug, err))
			if errors.Is(err, news.ErrRateLimited) || ctx.Err() != nil {
				break
			}
			continue
		}
		for i := range arts {
			arts[i].Markets = []string{t.Slug}
		}
		n, err := c.Store.UpsertArticles(ctx, arts)
		total += n
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := c.Store.SetGDELTChecked(ctx, t.Slug, started); err != nil {
			errs = append(errs, err)
		}
	}
	c.update(func(s *Status) { s.LastGDELT, s.NewGDELTArticles = c.now(), total })
	err = errors.Join(errs...)
	c.fail(err)
	return total, err
}

// Rythmes de Run.
const (
	TargetsEvery = time.Hour
	PricesEvery  = 10 * time.Minute
	FeedsEvery   = 2 * time.Minute
	GDELTEvery   = 15 * time.Minute
)

// Run enchaîne les passes jusqu'à l'annulation du contexte : cibles et
// mots-clés toutes les heures, prix toutes les 10 min, flux toutes les
// 2 min, GDELT tous les quarts d'heure (son rythme d'indexation). Chaque
// boucle est indépendante : une passe GDELT lente (6 s par cible) ne
// retarde pas les flux.
func (c *Collector) Run(ctx context.Context) {
	c.update(func(s *Status) { s.Running = true })
	defer c.update(func(s *Status) { s.Running = false })
	c.RefreshTargets(ctx)
	var wg sync.WaitGroup
	loop := func(every time.Duration, f func(context.Context) (int, error)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				f(ctx)
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
	loop(TargetsEvery, func(ctx context.Context) (int, error) {
		if _, err := c.RefreshTargets(ctx); err != nil && ctx.Err() != nil {
			return 0, err
		}
		return c.FillKeywords(ctx)
	})
	loop(PricesEvery, c.CollectPrices)
	loop(FeedsEvery, c.CollectFeeds)
	loop(GDELTEvery, c.CollectGDELT)
	wg.Wait()
}

// --- Mots-clés par le modèle local ----------------------------------------

// DefaultKeywordsPrompt : consigne de l'agent « Mots-clés des marchés »
// (modifiable dans l'onglet Agents).
const DefaultKeywordsPrompt = `Tu prépares la veille d'actualités d'un marché de prédiction.
À partir de la question du marché (et de ses règles de résolution), donne 2 à 5 phrases de recherche courtes (1 à 4 mots chacune) qui trouveront les articles de presse capables de faire bouger ce marché.
- En anglais, sauf un nom propre qui s'écrit autrement.
- Des noms précis : personnes, institutions, pays, indicateurs (« Federal Reserve », « FOMC », « Jerome Powell », « Bitcoin ETF »), pas des mots vagues (« decision », « price », « market »).
- Pas de date, pas de chiffre seul, pas de guillemets.
Réponds uniquement avec le JSON demandé.`

// LLMKeywords : les mots-clés proposés par le LLM local, réponse contrainte
// par un schéma.
type LLMKeywords struct {
	LLM   llm.Client
	Model string
	// Prompt : la consigne en vigueur (agents.Registry) ; nil :
	// DefaultKeywordsPrompt.
	Prompt func() string
}

var keywordsSchema = json.RawMessage(`{"type":"object","properties":{"keywords":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":5}},"required":["keywords"]}`)

// maxKeywordsText : question, événement et début des règles — assez pour
// savoir de quoi il s'agit.
const maxKeywordsText = 2000

func (k LLMKeywords) Keywords(ctx context.Context, t Target, description string) ([]string, string, error) {
	prompt := DefaultKeywordsPrompt
	if k.Prompt != nil {
		if p := k.Prompt(); strings.TrimSpace(p) != "" {
			prompt = p
		}
	}
	text := "Question : " + t.Question
	if t.EventTitle != "" && t.EventTitle != t.Question {
		text += "\nÉvénement : " + t.EventTitle
	}
	if description != "" {
		text += "\nRègles de résolution : " + description
	}
	if r := []rune(text); len(r) > maxKeywordsText {
		text = string(r[:maxKeywordsText])
	}
	res, err := k.LLM.Extract(ctx, llm.ExtractRequest{Text: text, Schema: keywordsSchema, Prompt: prompt})
	if err != nil {
		return nil, "", err
	}
	var out struct {
		Keywords []string `json:"keywords"`
	}
	if err := json.Unmarshal(res.JSON, &out); err != nil {
		return nil, "", fmt.Errorf("réponse illisible : %w", err)
	}
	kw := CleanKeywords(out.Keywords)
	if len(kw) == 0 {
		return nil, "", errors.New("aucun mot-clé utilisable dans la réponse du modèle")
	}
	model := k.Model
	if res.Model.Name != "" {
		model = strings.TrimSpace(res.Model.Name + " " + res.Model.Version)
	}
	return kw, model, nil
}

// CleanKeywords : phrases nettoyées (guillemets retirés, espaces
// resserrés), de 3 à 60 caractères et 6 mots au plus, sans doublon (casse
// ignorée), 5 au plus. Ce qui sort d'un modèle est vérifié avant de partir
// en requête.
func CleanKeywords(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		p = strings.Join(strings.Fields(strings.ReplaceAll(p, `"`, " ")), " ")
		if len([]rune(p)) < 3 || len([]rune(p)) > 60 || len(strings.Fields(p)) > 6 {
			continue
		}
		key := strings.ToLower(p)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
		if len(out) == 5 {
			break
		}
	}
	return out
}
