package templates

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/JulianAndrieux/Jarvis/internal/polymarket"
)

// Jalon 53 : marchés Polymarket en lecture seule.

// WatchRow : un marché suivi, avec ses prix relus à l'affichage. Err non
// vide : Polymarket n'a pas répondu pour ce marché (la ligne reste, avec ce
// qui avait été recopié au moment du suivi).
type WatchRow struct {
	Slug       string
	Question   string
	EventTitle string
	Note       string
	Market     polymarket.Market
	Err        string
	// Alert : la variation sur 24 h dépasse le seuil du suivi.
	Alert bool
}

// MarketsView : la page Marchés.
type MarketsView struct {
	Query   string
	Watches []WatchRow
	// Results : résultats de la recherche ; Searched : une recherche a eu
	// lieu (« aucun résultat » n'a de sens qu'après).
	Results     []ResultEvent
	Searched    bool
	SearchError string
	// Watched : slugs suivis, pour afficher « Suivi » plutôt que « Suivre »
	// dans les résultats.
	Watched map[string]bool
	Error   string
}

// ResultEvent : un événement trouvé, réduit à ses branches les plus
// actives ; More : branches ouvertes non affichées.
type ResultEvent struct {
	Event polymarket.Event
	More  int
}

// OutcomeBook : le carnet d'une issue, prêt à afficher.
type OutcomeBook struct {
	Outcome polymarket.Outcome
	Book    polymarket.Book
	Err     string
}

// MarketDetailView : la fiche d'un marché.
type MarketDetailView struct {
	Market   polymarket.Market
	Watched  bool
	Note     string
	Books    []OutcomeBook
	Interval polymarket.Interval
	History  []polymarket.PricePoint
	// HistoryOf : l'issue tracée.
	HistoryOf  string
	HistoryErr string
	Chart      Chart
}

// Chart : une courbe de prix déjà mise à l'échelle (voir PriceChart).
type Chart struct {
	Width, Height float64
	Path          string
	// Ticks : graduations horizontales (prix et ordonnée).
	Ticks []ChartTick
	First string
	Last  string
}

type ChartTick struct {
	Y     float64
	Label string
}

// PriceChart met une série de prix à l'échelle d'un cadre w×h. L'échelle
// verticale suit les données (avec une marge) plutôt que 0-100 % : un
// marché qui oscille entre 2 et 4 % serait une ligne plate sinon. Moins de
// deux points : pas de courbe.
func PriceChart(points []polymarket.PricePoint, w, h float64) Chart {
	c := Chart{Width: w, Height: h}
	if len(points) < 2 {
		return c
	}
	lo, hi := points[0].Price, points[0].Price
	for _, p := range points {
		lo, hi = math.Min(lo, p.Price), math.Max(hi, p.Price)
	}
	pad := math.Max((hi-lo)*0.1, 0.005)
	lo, hi = math.Max(0, lo-pad), math.Min(1, hi+pad)
	t0, t1 := points[0].At.Unix(), points[len(points)-1].At.Unix()
	span := float64(t1 - t0)
	if span <= 0 {
		span = 1
	}
	y := func(p float64) float64 { return h - (p-lo)/(hi-lo)*h }
	var b strings.Builder
	for i, p := range points {
		x := float64(p.At.Unix()-t0) / span * w
		if i == 0 {
			b.WriteString("M")
		} else {
			b.WriteString(" L")
		}
		b.WriteString(strconv.FormatFloat(x, 'f', 1, 64))
		b.WriteString(",")
		b.WriteString(strconv.FormatFloat(y(p.Price), 'f', 1, 64))
	}
	c.Path = b.String()
	for _, v := range []float64{hi, (lo + hi) / 2, lo} {
		c.Ticks = append(c.Ticks, ChartTick{Y: y(v), Label: Pct(v)})
	}
	c.First = points[0].At.Local().Format("02/01 15:04")
	c.Last = points[len(points)-1].At.Local().Format("02/01 15:04")
	return c
}

// frenchFloat : virgule décimale, arrondi au demi supérieur. Le petit
// décalage corrige la représentation binaire : 0,45 vaut 0,4499999… en
// float64 et s'arrondirait sinon à 0,4.
func frenchFloat(v float64, digits int) string {
	p := math.Pow(10, float64(digits))
	r := math.Round(math.Abs(v)*p+1e-6) / p
	if v < 0 {
		r = -r
	}
	return strings.Replace(strconv.FormatFloat(r, 'f', digits, 64), ".", ",", 1)
}

// Pct : une probabilité (0-1) en pourcentage. Sous 1 % et au-dessus de
// 99 %, une décimale de plus : 0,3 % et 0,03 % ne disent pas la même chose.
func Pct(p float64) string {
	v := p * 100
	digits := 1
	if v < 1 || v > 99 {
		digits = 2
	}
	return frenchFloat(v, digits) + " %"
}

// Change : une variation de probabilité, en points, toujours signée.
func Change(d float64) string {
	v := d * 100
	if math.Abs(v) < 0.05 {
		return "= 0,0 pt"
	}
	sign := "+"
	if v < 0 {
		sign = "−"
	}
	return sign + frenchFloat(math.Abs(v), 1) + " pt"
}

// USD : un montant en dollars, abrégé.
func USD(v float64) string {
	switch a := math.Abs(v); {
	case a >= 1e9:
		return frenchFloat(v/1e9, 1) + " Md $"
	case a >= 1e6:
		return frenchFloat(v/1e6, 1) + " M $"
	case a >= 1e3:
		return frenchFloat(v/1e3, 1) + " k $"
	default:
		return frenchFloat(v, 0) + " $"
	}
}

// Price : un prix du carnet, en cents (le prix d'une part qui paie 1 $).
func Price(p float64) string { return frenchFloat(p*100, 1) + " ¢" }

// Shares : une quantité de parts.
func Shares(v float64) string {
	if v >= 1e3 {
		return frenchFloat(v/1e3, 1) + " k"
	}
	return frenchFloat(v, 0)
}

// changeClass : couleur d'une variation.
func changeClass(d float64) string {
	switch {
	case d >= 0.0005:
		return "up"
	case d <= -0.0005:
		return "down"
	}
	return "flat"
}

// topLevels : les n meilleurs niveaux.
func topLevels(l []polymarket.Level, n int) []polymarket.Level {
	if len(l) > n {
		return l[:n]
	}
	return l
}

func intervalLabel(iv polymarket.Interval) string {
	switch iv {
	case polymarket.Interval1D:
		return "24 h"
	case polymarket.Interval1W:
		return "7 jours"
	case polymarket.Interval1M:
		return "30 jours"
	case polymarket.IntervalMax:
		return "Tout"
	}
	return string(iv)
}

func marketHref(slug string) string { return "/markets/" + slug }

func tickY(t ChartTick) string { return fmt.Sprintf("%.1f", t.Y) }

// searchBack : la page de recherche où revenir après « Suivre ».
func searchBack(q string) string {
	if q == "" {
		return "/markets"
	}
	return "/markets?" + url.Values{"q": {q}}.Encode()
}
