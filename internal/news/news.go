// Package news collecte des actualités horodatées pour les relier aux
// marchés Polymarket (jalon 54) : flux RSS d'agences et de sources
// officielles, et la base GDELT (recherche par mots-clés, avec historique).
//
// Huitième exception à « aucune donnée ne sort de la machine » (cf.
// CLAUDE.md) : les requêtes partent vers GDELT et vers les sites des flux.
// Elles ne portent que des mots-clés tirés des questions publiques des
// marchés (GDELT) ou rien du tout (un flux RSS se télécharge tel quel).
//
// Aucune analyse ici : ce paquet collecte, date et rattache. Mesurer
// l'impact d'une annonce sur un cours est le jalon 55.
package news

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Via : par où l'article est arrivé.
type Via string

const (
	ViaRSS   Via = "rss"
	ViaGDELT Via = "gdelt"
)

// Article : une actualité, telle que collectée.
type Article struct {
	// ID : dérivé de l'URL canonique — le même article arrivé par un flux
	// et par GDELT n'est enregistré qu'une fois.
	ID      string `bson:"_id"`
	URL     string `bson:"url"`
	Title   string `bson:"title"`
	Summary string `bson:"summary,omitempty"`
	// Source : le nom du flux, ou le domaine pour GDELT.
	Source string `bson:"source"`
	Lang   string `bson:"lang,omitempty"`
	Via    Via    `bson:"via"`
	// Published : l'heure annoncée par la source (UTC). C'est elle que
	// l'étude d'impact compare aux cours. Seen : quand Jarvis l'a vue —
	// l'écart entre les deux dit avec quel retard on l'aurait apprise.
	Published time.Time `bson:"published"`
	Seen      time.Time `bson:"seen"`
	// DateGuessed : la source ne donnait pas de date exploitable (absente,
	// illisible ou dans le futur) ; Published vaut alors Seen. Ces articles
	// sont à écarter d'une mesure fine de l'impact.
	DateGuessed bool `bson:"date_guessed,omitempty"`
	// Markets : slugs des marchés auxquels l'article a été rattaché.
	Markets []string `bson:"markets,omitempty"`
}

// ErrRateLimited : la source refuse pour l'instant (trop de requêtes).
var ErrRateLimited = errors.New("news: trop de requêtes, source momentanément refusée")

// trackingParams : paramètres d'URL qui ne désignent pas l'article mais la
// campagne qui y mène.
var trackingParams = map[string]bool{"at_medium": true, "at_campaign": true, "ns_mchannel": true, "ns_source": true, "ocid": true, "cmpid": true, "smid": true, "fbclid": true, "gclid": true}

// CanonicalURL : schéma et hôte en minuscules, sans fragment ni paramètre
// de suivi. Une URL illisible est rendue telle quelle.
func CanonicalURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(raw)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	q := u.Query()
	for k := range q {
		if trackingParams[strings.ToLower(k)] || strings.HasPrefix(strings.ToLower(k), "utm_") {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// ArticleID : l'identifiant d'un article, d'après son URL canonique.
func ArticleID(rawURL string) string {
	sum := sha256.Sum256([]byte(CanonicalURL(rawURL)))
	return hex.EncodeToString(sum[:12])
}

// fold : minuscules, sans accents — « Décembre » et « decembre » se
// rejoignent.
func fold(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	if err != nil {
		out = s
	}
	return strings.ToLower(out)
}

// words : les mots d'un texte replié (lettres et chiffres).
func words(s string) []string {
	return strings.FieldsFunc(fold(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// Matches dit si l'article concerne l'une des phrases : tous les mots
// d'une phrase présents, en mots entiers, dans le titre ou le résumé, sans
// tenir compte de l'ordre, de la casse ni des accents.
//
// Décision assumée : un rattachement par mots-clés, déterministe et
// explicable (on peut toujours dire pourquoi un article a été rattaché).
// Les mots-clés eux-mêmes viennent du modèle local ; juger la pertinence
// réelle d'une annonce est le travail du jalon 56, pas de ce filtre.
func Matches(a Article, phrases []string) bool {
	have := map[string]bool{}
	for _, w := range words(a.Title + " " + a.Summary) {
		have[w] = true
	}
	for _, p := range phrases {
		ws := words(p)
		if len(ws) == 0 {
			continue
		}
		all := true
		for _, w := range ws {
			if !have[w] {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// Feed : un flux RSS ou Atom à relever.
type Feed struct {
	Name string
	URL  string
}

// DefaultFeeds : flux vérifiés le 03/11/2026 (tous répondaient en 200).
// Politique et géopolitique, banques centrales, crypto.
var DefaultFeeds = []Feed{
	{"Federal Reserve", "https://www.federalreserve.gov/feeds/press_all.xml"},
	{"BCE", "https://www.ecb.europa.eu/rss/press.html"},
	{"BBC World", "https://feeds.bbci.co.uk/news/world/rss.xml"},
	{"Guardian World", "https://www.theguardian.com/world/rss"},
	{"NYT Politics", "https://rss.nytimes.com/services/xml/rss/nyt/Politics.xml"},
	{"Politico", "https://rss.politico.com/politics-news.xml"},
	{"Le Monde International", "https://www.lemonde.fr/international/rss_full.xml"},
	{"CoinDesk", "https://www.coindesk.com/arc/outboundfeeds/rss/"},
	{"Cointelegraph", "https://cointelegraph.com/rss"},
}

// FormatFeedList : l'inverse de ParseFeedList.
func FormatFeedList(feeds []Feed) string {
	parts := make([]string, len(feeds))
	for i, f := range feeds {
		parts[i] = f.Name + "=" + f.URL
	}
	return strings.Join(parts, ",")
}

// ParseFeedList lit « Nom=https://…,Nom=https://… ». https seulement : un
// flux en clair pourrait être modifié en chemin, et ce que dit un flux
// finira par peser sur une décision de trading.
func ParseFeedList(s string) ([]Feed, error) {
	var out []Feed
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, raw, ok := strings.Cut(part, "=")
		name, raw = strings.TrimSpace(name), strings.TrimSpace(raw)
		if !ok || name == "" {
			return nil, fmt.Errorf("news: flux %q : attendu Nom=https://…", part)
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return nil, fmt.Errorf("news: flux %q : adresse https attendue", name)
		}
		out = append(out, Feed{Name: name, URL: raw})
	}
	return out, nil
}
