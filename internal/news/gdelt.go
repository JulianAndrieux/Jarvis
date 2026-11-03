package news

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	DefaultGDELTURL = "https://api.gdeltproject.org/api/v2/doc/doc"
	// DefaultGDELTInterval : GDELT demande au plus une requête toutes les
	// 5 s et répond 429 au-delà ; une seconde de marge.
	DefaultGDELTInterval = 6 * time.Second
	// gdeltMaxRecords : le maximum accepté par l'API DOC.
	gdeltMaxRecords = 250
)

// GDELT interroge l'API DOC 2.0 de GDELT (articles du monde entier,
// indexés toutes les 15 min, recherchables sur les trois derniers mois).
// Sûr en concurrence : les requêtes sont sérialisées et espacées.
type GDELT struct {
	BaseURL     string
	HTTP        *http.Client
	MinInterval time.Duration
	Now         func() time.Time
	Sleep       func(ctx context.Context, d time.Duration) error

	mu   sync.Mutex
	last time.Time
}

func (g *GDELT) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *GDELT) sleep(ctx context.Context, d time.Duration) error {
	if g.Sleep != nil {
		return g.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Query : la requête GDELT pour des phrases — chacune entre guillemets si
// elle a plusieurs mots, réunies par OR. Les guillemets d'une phrase sont
// retirés : ils casseraient la requête.
func Query(phrases []string) string {
	var parts []string
	for _, p := range phrases {
		p = strings.Join(strings.Fields(strings.ReplaceAll(p, `"`, " ")), " ")
		if p == "" {
			continue
		}
		if strings.Contains(p, " ") {
			p = `"` + p + `"`
		}
		parts = append(parts, p)
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// Search : les articles qui contiennent l'une des phrases, publiés depuis
// since (zéro : les dernières 24 h), du plus récent au plus ancien.
func (g *GDELT) Search(ctx context.Context, phrases []string, since time.Time) ([]Article, error) {
	q := Query(phrases)
	if q == "" {
		return nil, errors.New("news: gdelt : aucune phrase à chercher")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	min := g.MinInterval
	if min <= 0 {
		min = DefaultGDELTInterval
	}
	if !g.last.IsZero() {
		if wait := min - g.now().Sub(g.last); wait > 0 {
			if err := g.sleep(ctx, wait); err != nil {
				return nil, err
			}
		}
	}
	defer func() { g.last = g.now() }()

	params := url.Values{"query": {q}, "mode": {"artlist"}, "format": {"json"}, "maxrecords": {fmt.Sprint(gdeltMaxRecords)}, "sort": {"datedesc"}}
	if since.IsZero() {
		params.Set("timespan", "1d")
	} else {
		params.Set("startdatetime", since.UTC().Format("20060102150405"))
	}
	base := g.BaseURL
	if base == "" {
		base = DefaultGDELTURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	hc := g.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("news: gdelt : %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes))
	if err != nil {
		return nil, fmt.Errorf("news: gdelt : %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("news: gdelt : HTTP %d : %s", resp.StatusCode, truncate(strings.TrimSpace(string(body)), 200))
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, nil
	}
	var doc struct {
		Articles []struct {
			URL      string `json:"url"`
			Title    string `json:"title"`
			SeenDate string `json:"seendate"`
			Domain   string `json:"domain"`
			Language string `json:"language"`
		} `json:"articles"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		// GDELT répond en texte brut à une requête qu'il refuse (mot trop
		// court, syntaxe) : on rend ce texte, pas « JSON illisible ».
		return nil, fmt.Errorf("news: gdelt : %s", truncate(strings.TrimSpace(string(body)), 200))
	}
	seen := g.now().UTC()
	out := make([]Article, 0, len(doc.Articles))
	for _, d := range doc.Articles {
		if d.URL == "" || strings.TrimSpace(d.Title) == "" {
			continue
		}
		a := Article{ID: ArticleID(d.URL), URL: CanonicalURL(d.URL), Title: clean(d.Title), Source: d.Domain, Lang: d.Language, Via: ViaGDELT, Seen: seen}
		// seendate : quand GDELT a vu l'article — la meilleure date
		// disponible, à 15 min près.
		if t, err := time.Parse("20060102T150405Z", d.SeenDate); err == nil && !t.After(seen.Add(5*time.Minute)) {
			a.Published = t.UTC()
		} else {
			a.Published, a.DateGuessed = seen, true
		}
		out = append(out, a)
	}
	return out, nil
}
