package news

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Extraits de vrais flux (novembre 2026) : Fed (dates en CDATA, BOM),
// BBC (titres en CDATA, paramètres de suivi dans les liens), Guardian.
func TestParseFeed_RealRSS(t *testing.T) {
	seen := time.Date(2026, 11, 3, 20, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		file, firstTitle string
		firstAt          time.Time
	}{
		{"fed.xml", "Federal Reserve Board announces approval of application by The Bank of Nova Scotia", time.Date(2026, 10, 29, 20, 30, 0, 0, time.UTC)},
		{"bbc.xml", "Seven flights across Europe targeted by suspected bomb threats", time.Date(2026, 11, 3, 17, 56, 3, 0, time.UTC)},
	} {
		arts, err := ParseFeed(fixture(t, c.file), "src", seen)
		if err != nil {
			t.Fatalf("%s : %v", c.file, err)
		}
		if len(arts) != 3 {
			t.Fatalf("%s : %d articles", c.file, len(arts))
		}
		a := arts[0]
		if a.Title != c.firstTitle || !a.Published.Equal(c.firstAt) {
			t.Errorf("%s : %q %v", c.file, a.Title, a.Published)
		}
		if a.Source != "src" || a.Via != ViaRSS || !a.Seen.Equal(seen) || a.ID == "" || !strings.HasPrefix(a.URL, "https://") {
			t.Errorf("%s : %+v", c.file, a)
		}
	}
	arts, _ := ParseFeed(fixture(t, "bbc.xml"), "BBC", seen)
	if strings.Contains(arts[0].URL, "at_medium") || arts[0].Summary == "" {
		t.Errorf("URL = %q (paramètres de suivi retirés), résumé = %q", arts[0].URL, arts[0].Summary)
	}
	if arts, err := ParseFeed(fixture(t, "guardian.xml"), "Guardian", seen); err != nil || len(arts) != 3 {
		t.Errorf("guardian : %d, %v", len(arts), err)
	}
}

func TestParseFeed_Atom(t *testing.T) {
	seen := time.Date(2026, 11, 3, 20, 0, 0, 0, time.UTC)
	arts, err := ParseFeed(fixture(t, "atom.xml"), "BCE", seen)
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 2 {
		t.Fatalf("%d articles", len(arts))
	}
	a := arts[0]
	if a.Title != "ECB keeps rates & signals patience" || a.URL != "https://example.org/ecb-rates" || !a.Published.Equal(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)) || a.Summary == "" {
		t.Errorf("atom = %+v", a)
	}
	// Sans date de publication, on garde l'heure de collecte : un article
	// n'est jamais daté du futur ni de zéro.
	if !arts[1].Published.Equal(seen) || !arts[1].DateGuessed {
		t.Errorf("sans date : %v, devinée=%v", arts[1].Published, arts[1].DateGuessed)
	}
}

func TestParseFeed_Rejects(t *testing.T) {
	if _, err := ParseFeed([]byte("<html><body>pas un flux</body></html>"), "x", time.Now()); err == nil {
		t.Error("une page HTML n'est pas un flux")
	}
	if _, err := ParseFeed([]byte("pas du xml"), "x", time.Now()); err == nil {
		t.Error("veut une erreur")
	}
}

// Une date de publication dans le futur (horloge du site fausse) est
// ramenée à l'heure de collecte : sinon l'article semblerait annoncer ce
// que le marché n'a pas encore vu.
func TestParseFeed_FutureDateClamped(t *testing.T) {
	seen := time.Date(2026, 11, 3, 20, 0, 0, 0, time.UTC)
	feed := `<rss><channel><item><title>T</title><link>https://a.example/x</link><pubDate>Wed, 04 Nov 2026 20:00:00 GMT</pubDate></item></channel></rss>`
	arts, err := ParseFeed([]byte(feed), "x", seen)
	if err != nil || len(arts) != 1 || !arts[0].Published.Equal(seen) || !arts[0].DateGuessed {
		t.Fatalf("%+v %v", arts, err)
	}
}

func TestCanonicalURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.bbc.co.uk/news/a?at_medium=RSS&at_campaign=rss": "https://www.bbc.co.uk/news/a",
		"https://x.example/a?utm_source=t&id=3#frag":                 "https://x.example/a?id=3",
		"HTTPS://X.Example/A":                                        "https://x.example/A",
	} {
		if got := CanonicalURL(in); got != want {
			t.Errorf("CanonicalURL(%q) = %q, veut %q", in, got, want)
		}
	}
	if ArticleID("https://x.example/a?utm_source=t") != ArticleID("https://x.example/a") {
		t.Error("le même article par deux liens doit avoir le même identifiant")
	}
}

func TestMatches(t *testing.T) {
	a := Article{Title: "La Fed maintient ses taux directeurs", Summary: "Jerome Powell évoque une baisse en décembre."}
	for _, c := range []struct {
		phrases []string
		want    bool
	}{
		{[]string{"fed taux"}, true},             // tous les mots, dans n'importe quel ordre
		{[]string{"powell decembre"}, true},      // sans accents
		{[]string{"bce"}, false},                 // absent
		{[]string{"bce", "Jerome Powell"}, true}, // une seule phrase suffit
		{[]string{"fe"}, false},                  // mot entier, pas un morceau (« fe » ≠ « fed »)
		{[]string{""}, false},
		{nil, false},
	} {
		if got := Matches(a, c.phrases); got != c.want {
			t.Errorf("Matches(%q) = %v", c.phrases, got)
		}
	}
}

func TestGDELT_Search(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Write(fixture(t, "gdelt.json"))
	}))
	defer srv.Close()
	seen := time.Date(2026, 11, 3, 18, 10, 0, 0, time.UTC)
	g := &GDELT{BaseURL: srv.URL, HTTP: srv.Client(), Now: func() time.Time { return seen }}
	since := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	arts, err := g.Search(context.Background(), []string{"Federal Reserve", "Powell"}, since)
	if err != nil {
		t.Fatal(err)
	}
	q := got.URL.Query()
	if q.Get("query") != `("Federal Reserve" OR Powell)` || q.Get("mode") != "artlist" || q.Get("format") != "json" || q.Get("startdatetime") != "20261103120000" || q.Get("sort") != "datedesc" {
		t.Errorf("requête = %v", q)
	}
	if len(arts) != 2 {
		t.Fatalf("%d articles", len(arts))
	}
	a := arts[0]
	if a.Title != "Fed holds rates steady, signals December cut" || !a.Published.Equal(time.Date(2026, 11, 3, 17, 45, 0, 0, time.UTC)) || a.Via != ViaGDELT || a.Source != "example.com" || a.Lang != "English" || !a.Seen.Equal(seen) {
		t.Errorf("article = %+v", a)
	}
}

// GDELT répond en texte brut, code 429, quand on l'interroge trop vite
// (constaté sur l'API réelle) : une erreur reconnaissable, pas « JSON
// illisible ».
func TestGDELT_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("Please limit requests to one every 5 seconds"))
	}))
	defer srv.Close()
	g := &GDELT{BaseURL: srv.URL, HTTP: srv.Client()}
	if _, err := g.Search(context.Background(), []string{"x"}, time.Time{}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, veut ErrRateLimited", err)
	}
}

// Une recherche sans résultat : GDELT renvoie un objet vide (ou rien).
func TestGDELT_EmptyAndQuoting(t *testing.T) {
	var q string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query().Get("query")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	g := &GDELT{BaseURL: srv.URL, HTTP: srv.Client()}
	arts, err := g.Search(context.Background(), []string{`Bitcoin "ETF"`, "  ", "BTC"}, time.Time{})
	if err != nil || len(arts) != 0 {
		t.Fatalf("%v %v", arts, err)
	}
	// Les guillemets d'une phrase ne peuvent pas casser la requête ; une
	// phrase vide est ignorée.
	if q != `("Bitcoin ETF" OR BTC)` {
		t.Errorf("query = %q", q)
	}
	if _, err := g.Search(context.Background(), []string{" "}, time.Time{}); err == nil {
		t.Error("aucune phrase : veut une erreur")
	}
}

// Entre deux requêtes, au moins MinInterval : GDELT bloque sinon.
func TestGDELT_Throttles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }))
	defer srv.Close()
	var slept []time.Duration
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	g := &GDELT{BaseURL: srv.URL, HTTP: srv.Client(), MinInterval: 6 * time.Second,
		Now: func() time.Time { return now },
		Sleep: func(ctx context.Context, d time.Duration) error {
			slept = append(slept, d)
			now = now.Add(d)
			return nil
		}}
	ctx := context.Background()
	g.Search(ctx, []string{"a"}, time.Time{})
	now = now.Add(2 * time.Second)
	g.Search(ctx, []string{"b"}, time.Time{})
	if len(slept) != 1 || slept[0] != 4*time.Second {
		t.Errorf("attentes = %v, veut [4s]", slept)
	}
}

func TestRSS_FetchAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fed":
			w.Write(fixture(t, "fed.xml"))
		case "/bbc":
			w.Write(fixture(t, "bbc.xml"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	r := &RSS{HTTP: srv.Client(), Feeds: []Feed{{Name: "Fed", URL: srv.URL + "/fed"}, {Name: "BBC", URL: srv.URL + "/bbc"}, {Name: "Cassé", URL: srv.URL + "/absent"}}}
	arts, errs := r.FetchAll(context.Background())
	if len(arts) != 6 {
		t.Errorf("articles = %d, veut 6", len(arts))
	}
	// Un flux en panne n'empêche pas les autres, et son erreur le nomme.
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "Cassé") {
		t.Errorf("erreurs = %v", errs)
	}
}

func TestParseFeedList(t *testing.T) {
	feeds, err := ParseFeedList("Fed=https://a.example/f.xml, BBC = https://b.example/r.xml")
	if err != nil || len(feeds) != 2 || feeds[1].Name != "BBC" || feeds[1].URL != "https://b.example/r.xml" {
		t.Fatalf("%+v %v", feeds, err)
	}
	for _, bad := range []string{"sans-egal", "X=ftp://a", "X=http://a.example/f"} {
		if _, err := ParseFeedList(bad); err == nil {
			t.Errorf("ParseFeedList(%q) doit échouer (https seulement)", bad)
		}
	}
	if feeds, err := ParseFeedList(""); err != nil || len(feeds) != 0 {
		t.Errorf("vide = %v %v", feeds, err)
	}
}
