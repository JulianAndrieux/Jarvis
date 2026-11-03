package news

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// maxFeedBytes : un flux plus gros est refusé plutôt que lu sans limite.
const maxFeedBytes = 8 << 20

// ParseFeed lit un flux RSS 2.0 ou Atom. seen : l'heure de collecte.
func ParseFeed(data []byte, source string, seen time.Time) ([]Article, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // BOM (vu sur le flux de la Fed)
	var doc struct {
		XMLName xml.Name
		Channel struct {
			Items []struct {
				Title       string `xml:"title"`
				Link        string `xml:"link"`
				GUID        string `xml:"guid"`
				Description string `xml:"description"`
				PubDate     string `xml:"pubDate"`
				Date        string `xml:"date"` // dc:date
			} `xml:"item"`
		} `xml:"channel"`
		Entries []struct {
			Title string `xml:"title"`
			Links []struct {
				Href string `xml:"href,attr"`
				Rel  string `xml:"rel,attr"`
			} `xml:"link"`
			Summary   string `xml:"summary"`
			Content   string `xml:"content"`
			Published string `xml:"published"`
			Updated   string `xml:"updated"`
		} `xml:"entry"`
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("news: flux %s illisible : %w", source, err)
	}
	var out []Article
	add := func(title, link, summary, date string) {
		link = strings.TrimSpace(link)
		title = clean(title)
		if link == "" || title == "" {
			return
		}
		a := Article{
			ID: ArticleID(link), URL: CanonicalURL(link), Title: title, Summary: truncate(clean(summary), 600),
			Source: source, Via: ViaRSS, Seen: seen.UTC(),
		}
		a.Published, a.DateGuessed = dated(date, seen)
		out = append(out, a)
	}
	switch strings.ToLower(doc.XMLName.Local) {
	case "rss", "rdf":
		for _, it := range doc.Channel.Items {
			link := it.Link
			if link == "" && strings.HasPrefix(strings.TrimSpace(it.GUID), "http") {
				link = it.GUID
			}
			date := it.PubDate
			if strings.TrimSpace(date) == "" {
				date = it.Date
			}
			add(it.Title, link, it.Description, date)
		}
	case "feed":
		for _, e := range doc.Entries {
			link := ""
			for _, l := range e.Links {
				if l.Rel == "" || l.Rel == "alternate" {
					link = l.Href
					break
				}
			}
			summary := e.Summary
			if summary == "" {
				summary = e.Content
			}
			date := e.Published
			if date == "" {
				date = e.Updated
			}
			add(e.Title, link, summary, date)
		}
	default:
		return nil, fmt.Errorf("news: %s n'est pas un flux RSS ou Atom (<%s>)", source, doc.XMLName.Local)
	}
	return out, nil
}

var (
	tagRe   = regexp.MustCompile(`<[^>]*>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// clean : texte brut d'un titre ou d'un résumé (balises retirées, entités
// décodées, espaces resserrés). Ce texte vient d'un tiers : l'interface
// l'affiche toujours échappé.
func clean(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

var dateLayouts = []string{
	time.RFC1123Z, time.RFC1123, time.RFC3339, time.RFC3339Nano,
	"Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST",
	"Mon, 02 Jan 2006 15:04 -0700", "2 Jan 2006 15:04:05 -0700",
	"2006-01-02T15:04:05Z0700", "2006-01-02 15:04:05",
}

// dated : la date annoncée, ou l'heure de collecte si elle manque, est
// illisible ou postérieure à la collecte (une source à l'horloge fausse ne
// doit pas faire croire qu'elle annonçait l'avenir).
func dated(s string, seen time.Time) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			t = t.UTC()
			if t.After(seen.Add(5 * time.Minute)) {
				return seen.UTC(), true
			}
			return t, false
		}
	}
	return seen.UTC(), true
}

// RSS relève une liste de flux.
type RSS struct {
	Feeds []Feed
	HTTP  *http.Client
	Now   func() time.Time
}

// FetchAll relève tous les flux en parallèle. Un flux en panne n'empêche
// pas les autres : son erreur est rendue à part, avec son nom.
func (r *RSS) FetchAll(ctx context.Context) ([]Article, []error) {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		arts []Article
		errs []error
	)
	for _, f := range r.Feeds {
		wg.Add(1)
		go func(f Feed) {
			defer wg.Done()
			got, err := r.fetch(ctx, f, now())
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("flux %s : %w", f.Name, err))
				return
			}
			arts = append(arts, got...)
		}(f)
	}
	wg.Wait()
	return arts, errs
}

func (r *RSS) fetch(ctx context.Context, f Feed, seen time.Time) ([]Article, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml")
	req.Header.Set("User-Agent", "Jarvis-news/1.0")
	hc := r.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFeedBytes {
		return nil, fmt.Errorf("flux de plus de %d octets", maxFeedBytes)
	}
	return ParseFeed(data, f.Name, seen)
}
