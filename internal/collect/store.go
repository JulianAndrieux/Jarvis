// Package collect rassemble, pour les marchés Polymarket étudiés, ce dont
// l'étude d'impact aura besoin (jalon 54) : l'historique des prix à la
// minute et les actualités horodatées qui s'y rapportent.
//
// Données publiques, donc globales à l'instance — comme les tickets et les
// agents, et à la différence des documents ou de la liste de suivi : un
// cours et une dépêche ne sont à aucun environnement. Un marché suivi par
// un environnement devient une cible de collecte ; seul son slug (public)
// en sort.
package collect

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/news"
	"github.com/JulianAndrieux/Jarvis/internal/polymarket"
)

// Target : un marché dont on collecte les prix et les actualités.
type Target struct {
	Slug       string    `bson:"_id"`
	Question   string    `bson:"question"`
	EventTitle string    `bson:"event_title,omitempty"`
	Category   string    `bson:"category,omitempty"` // tag Polymarket, ou « suivi »
	TokenID    string    `bson:"token_id"`           // jeton de la première issue (« Yes »)
	EndDate    time.Time `bson:"end_date,omitempty"`
	UpdatedAt  time.Time `bson:"updated_at"`
	// Keywords : phrases de recherche, proposées par le modèle local
	// (reproductibilité : modèle et date gardés avec elles).
	Keywords      []string  `bson:"keywords,omitempty"`
	KeywordsModel string    `bson:"keywords_model,omitempty"`
	KeywordsAt    time.Time `bson:"keywords_at,omitempty"`
	// GDELTAt : jusqu'où GDELT a déjà été interrogé pour cette cible.
	GDELTAt time.Time `bson:"gdelt_at,omitempty"`
}

// ArticleQuery : Market vide = tous les marchés (et les articles
// rattachés à aucun) ; From/To vides = sans borne ; Limit 0 = 200.
type ArticleQuery struct {
	Market   string
	From, To time.Time
	Limit    int
}

const defaultArticleLimit = 200

var ErrUnknownTarget = errors.New("collect: cible inconnue")

// Store persiste cibles, prix et articles.
type Store interface {
	// UpsertTarget enregistre une cible ; ses mots-clés et sa progression
	// GDELT, s'ils existent déjà, sont conservés.
	UpsertTarget(ctx context.Context, t Target) error
	Targets(ctx context.Context) ([]Target, error)
	SetKeywords(ctx context.Context, slug string, keywords []string, model string, at time.Time) error
	SetGDELTChecked(ctx context.Context, slug string, at time.Time) error

	// AddPrices ajoute des points (un même instant n'est gardé qu'une
	// fois) et rend le nombre de points nouveaux.
	AddPrices(ctx context.Context, tokenID string, pts []polymarket.PricePoint) (int, error)
	// Prices : les points de [from, to], dans l'ordre chronologique.
	Prices(ctx context.Context, tokenID string, from, to time.Time) ([]polymarket.PricePoint, error)
	LastPrice(ctx context.Context, tokenID string) (time.Time, bool, error)

	// UpsertArticles enregistre des articles et rend le nombre de
	// nouveaux. Un article déjà connu garde sa première vue (Seen,
	// Published, Via) et gagne les nouveaux rattachements.
	UpsertArticles(ctx context.Context, arts []news.Article) (int, error)
	// LinkArticles rattache des articles déjà enregistrés à un marché.
	LinkArticles(ctx context.Context, ids []string, slug string) error
	// Articles : du plus récent au plus ancien (date de publication).
	Articles(ctx context.Context, q ArticleQuery) ([]news.Article, error)
}

// dayKey : le seau d'un point de prix — un document par jeton et par jour
// UTC (une minute de résolution : 1 440 points par jour et par marché, un
// document par point serait démesuré).
func dayKey(tokenID string, t time.Time) string {
	return tokenID + ":" + t.UTC().Format("2006-01-02")
}

// mergePoints : réunit deux séries, un instant (à la seconde) n'apparaît
// qu'une fois — le point existant fait foi —, dans l'ordre chronologique.
func mergePoints(old, add []polymarket.PricePoint) ([]polymarket.PricePoint, int) {
	seen := make(map[int64]bool, len(old))
	for _, p := range old {
		seen[p.At.Unix()] = true
	}
	out := append([]polymarket.PricePoint(nil), old...)
	n := 0
	for _, p := range add {
		if seen[p.At.Unix()] {
			continue
		}
		seen[p.At.Unix()] = true
		out = append(out, polymarket.PricePoint{At: p.At.UTC(), Price: p.Price})
		n++
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, n
}

// groupByDay : les points répartis par seau.
func groupByDay(tokenID string, pts []polymarket.PricePoint) map[string][]polymarket.PricePoint {
	out := map[string][]polymarket.PricePoint{}
	for _, p := range pts {
		k := dayKey(tokenID, p.At)
		out[k] = append(out[k], p)
	}
	return out
}

// dayKeys : les seaux couvrant [from, to].
func dayKeys(tokenID string, from, to time.Time) []string {
	var out []string
	for d := time.Date(from.UTC().Year(), from.UTC().Month(), from.UTC().Day(), 0, 0, 0, 0, time.UTC); !d.After(to.UTC()); d = d.AddDate(0, 0, 1) {
		out = append(out, dayKey(tokenID, d))
	}
	return out
}

func inRange(pts []polymarket.PricePoint, from, to time.Time) []polymarket.PricePoint {
	var out []polymarket.PricePoint
	for _, p := range pts {
		if !p.At.Before(from) && !p.At.After(to) {
			out = append(out, p)
		}
	}
	return out
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string(nil), a...), b...) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
