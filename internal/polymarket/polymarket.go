// Package polymarket lit les marchés publics de Polymarket : marchés et
// événements (API Gamma), carnets d'ordres et historique des prix (CLOB).
//
// Lecture seule, sans compte ni clé (jalon 53) : rien de ce paquet ne
// peut passer un ordre. C'est la septième exception à « aucune donnée ne
// sort de la machine » (cf. CLAUDE.md) : les requêtes partent vers
// Polymarket, elles ne portent que l'identifiant du marché demandé ou le
// texte d'une recherche.
//
// Même forme que les autres ports du projet : Client (interface),
// HTTPClient (l'API réelle), FakeClient (tests), CachedClient (évite
// d'interroger Polymarket à chaque affichage de la barre latérale).
package polymarket

import (
	"context"
	"errors"
	"regexp"
	"time"
)

// Outcome : une issue d'un marché (« Yes », « No », un candidat…) et le
// jeton qui la représente dans le carnet d'ordres.
type Outcome struct {
	Name    string
	TokenID string
	// Price : dernier prix affiché par Polymarket, entre 0 et 1 — la
	// probabilité implicite donnée par le marché.
	Price float64
}

// FeeSchedule : les frais du marché tels que Polymarket les publie. Leur
// application à un ordre est l'affaire du modèle de risque (jalon 54).
type FeeSchedule struct {
	Enabled   bool
	Rate      float64
	Exponent  float64
	TakerOnly bool
}

// Market : un marché binaire ou une branche d'un événement à plusieurs
// issues.
type Market struct {
	ID          string
	Slug        string
	Question    string
	ConditionID string
	// Description : les règles de résolution, telles qu'écrites par
	// Polymarket. Texte d'un tiers : jamais interprété comme du HTML.
	Description      string
	ResolutionSource string
	EventSlug        string
	EventTitle       string
	// GroupItemTitle : le nom de la branche dans un événement à plusieurs
	// marchés (« 94 000 $ », un candidat…).
	GroupItemTitle string
	EndDate        time.Time
	Outcomes       []Outcome

	Volume    float64
	Volume24h float64
	Liquidity float64
	BestBid   float64
	BestAsk   float64
	LastTrade float64
	Spread    float64
	// DayChange : variation du prix de la première issue sur 24 h, en
	// fraction (−0.0045 = −0,45 point).
	DayChange float64

	Active          bool
	Closed          bool
	AcceptingOrders bool
	NegRisk         bool
	// Restricted : Polymarket signale le marché comme soumis à des
	// restrictions géographiques.
	Restricted bool

	TickSize     float64
	MinOrderSize float64
	Fees         FeeSchedule
}

// URL : la page du marché sur polymarket.com.
func (m Market) URL() string {
	if m.EventSlug != "" {
		return "https://polymarket.com/event/" + m.EventSlug
	}
	return "https://polymarket.com/market/" + m.Slug
}

// Event : un événement et ses marchés (un seul pour une question oui/non,
// plusieurs pour « quel prix atteindra le bitcoin ? »).
type Event struct {
	ID      string
	Slug    string
	Title   string
	EndDate time.Time
	Volume  float64
	Markets []Market
}

// Level : un niveau du carnet d'ordres — un prix et la quantité de parts
// disponibles à ce prix.
type Level struct {
	Price float64
	Size  float64
}

// Book : le carnet d'ordres d'un jeton, le meilleur prix en tête des deux
// côtés (achat le plus haut, vente la plus basse).
type Book struct {
	TokenID string
	At      time.Time
	Bids    []Level
	Asks    []Level
}

// Mid : le milieu entre meilleur achat et meilleure vente ; ok=false si un
// côté est vide.
func (b Book) Mid() (float64, bool) {
	if len(b.Bids) == 0 || len(b.Asks) == 0 {
		return 0, false
	}
	return (b.Bids[0].Price + b.Asks[0].Price) / 2, true
}

// Spread : l'écart entre meilleure vente et meilleur achat.
func (b Book) Spread() (float64, bool) {
	if len(b.Bids) == 0 || len(b.Asks) == 0 {
		return 0, false
	}
	return b.Asks[0].Price - b.Bids[0].Price, true
}

// PricePoint : un point de l'historique des prix d'un jeton.
type PricePoint struct {
	At    time.Time
	Price float64
}

// Interval : la période d'un historique de prix.
type Interval string

const (
	Interval1D  Interval = "1d"
	Interval1W  Interval = "1w"
	Interval1M  Interval = "1m"
	IntervalMax Interval = "max"
)

// Intervals : ceux que l'interface propose, du plus court au plus long.
var Intervals = []Interval{Interval1D, Interval1W, Interval1M, IntervalMax}

// fidelity : résolution demandée au CLOB, en minutes — de quoi tracer une
// courbe lisible sans rapatrier des milliers de points.
var fidelity = map[Interval]string{Interval1D: "10", Interval1W: "60", Interval1M: "240", IntervalMax: "1440"}

// Client : ce que Jarvis lit de Polymarket.
type Client interface {
	// Search : événements actifs correspondant au texte. Une recherche vide
	// ne rend rien, sans requête.
	Search(ctx context.Context, query string) ([]Event, error)
	// Market : le marché d'un slug ; ok=false s'il n'existe pas.
	Market(ctx context.Context, slug string) (Market, bool, error)
	Book(ctx context.Context, tokenID string) (Book, error)
	History(ctx context.Context, tokenID string, iv Interval) ([]PricePoint, error)
	// EventsByTag : les événements ouverts d'une catégorie (« politics »,
	// « fed », « crypto »…), les plus actifs sur 24 h d'abord.
	EventsByTag(ctx context.Context, tag string, limit int) ([]Event, error)
	// HistoryRange : l'historique à la minute de [from, to], plage de
	// MaxRangeSpan au plus (limite du CLOB).
	HistoryRange(ctx context.Context, tokenID string, from, to time.Time) ([]PricePoint, error)
}

// MaxRangeSpan : la plus longue plage d'historique à la minute que le CLOB
// accepte en une requête (constaté : 7 jours passent, 20 jours sont
// refusés — « interval is too long »).
const MaxRangeSpan = 7 * 24 * time.Hour

// ErrInvalidID : identifiant (slug, jeton, intervalle) refusé avant tout
// appel réseau.
var ErrInvalidID = errors.New("polymarket: identifiant invalide")

var (
	slugRe  = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	tokenRe = regexp.MustCompile(`^[0-9]{1,100}$`)
)

// ValidSlug : un slug Polymarket (minuscules, chiffres, tirets). Ce qui
// arrive d'une URL de Jarvis passe par ici avant de partir sur le réseau.
func ValidSlug(s string) bool { return len(s) <= 300 && slugRe.MatchString(s) }

// ValidTokenID : un identifiant de jeton (un entier décimal).
func ValidTokenID(s string) bool { return tokenRe.MatchString(s) }

// ValidTag : un tag de catégorie Gamma (même forme qu'un slug).
func ValidTag(s string) bool { return len(s) <= 100 && slugRe.MatchString(s) }

// ValidInterval : un intervalle connu.
func ValidInterval(iv Interval) bool { _, ok := fidelity[iv]; return ok }
