package projectinfo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLlamaCheck_UpWithModelName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.Write([]byte(`{"status":"ok"}`))
		case "/v1/models":
			w.Write([]byte(`{"data":[{"id":"/Users/x/models/Qwen3-8B-Q5_K_M.gguf"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	st := LlamaCheck(srv.Client(), srv.URL+"/v1")(context.Background())
	if st.Health != Up || st.Detail != "Qwen3-8B-Q5_K_M.gguf" {
		t.Errorf("status = %+v", st)
	}
}

func TestLlamaCheck_LoadingAndDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable) // modèle en cours de chargement
	}))
	st := LlamaCheck(srv.Client(), srv.URL+"/v1")(context.Background())
	if st.Health != Down || !strings.Contains(st.Detail, "503") {
		t.Errorf("loading status = %+v", st)
	}
	srv.Close()
	if st := LlamaCheck(http.DefaultClient, srv.URL+"/v1")(context.Background()); st.Health != Down {
		t.Errorf("closed server status = %+v", st)
	}
}

func TestBinaryCheck(t *testing.T) {
	if st := BinaryCheck("jarvis-introuvable-xyz", "go")(context.Background()); st.Health != Up || !strings.HasSuffix(st.Detail, "go") {
		t.Errorf("go = %+v", st)
	}
	if st := BinaryCheck("jarvis-introuvable-xyz")(context.Background()); st.Health != Down {
		t.Errorf("missing = %+v", st)
	}
}

func TestDirCheck(t *testing.T) {
	if st := DirCheck("")(context.Background()); st.Health != Off {
		t.Errorf("empty path = %+v", st)
	}
	if st := DirCheck(t.TempDir())(context.Background()); st.Health != Up {
		t.Errorf("existing dir = %+v", st)
	}
	if st := DirCheck("/jarvis/nope")(context.Background()); st.Health != Down {
		t.Errorf("missing dir = %+v", st)
	}
}

func TestFuncCheck(t *testing.T) {
	if st := FuncCheck(func(context.Context) (string, error) { return "3 jobs", nil })(context.Background()); st.Health != Up || st.Detail != "3 jobs" {
		t.Errorf("ok = %+v", st)
	}
	if st := FuncCheck(func(context.Context) (string, error) { return "", errors.New("boom") })(context.Background()); st.Health != Down || st.Detail != "boom" {
		t.Errorf("err = %+v", st)
	}
}

// Une sonde lente ne bloque pas la page : délai commun, sondes en parallèle.
func TestProbe_ParallelWithTimeout(t *testing.T) {
	slow := func(ctx context.Context) Status { <-ctx.Done(); return Status{Health: Up} }
	d := Diagram{Nodes: []Node{
		{ID: "a", Check: slow}, {ID: "b", Check: slow},
		{ID: "c", Check: func(context.Context) Status { return Status{Health: Up, Detail: "ok"} }},
		{ID: "d"}, // sans sonde
	}}
	start := time.Now()
	got := d.Probe(context.Background(), 200*time.Millisecond)
	if el := time.Since(start); el > time.Second {
		t.Errorf("Probe took %v, want parallel probes bounded by the timeout", el)
	}
	if got.Nodes[0].Status.Health != Down || !strings.Contains(got.Nodes[0].Status.Detail, "délai") {
		t.Errorf("slow node = %+v", got.Nodes[0].Status)
	}
	if got.Nodes[2].Status.Detail != "ok" || got.Nodes[3].Status.Health != "" {
		t.Errorf("nodes = %+v", got.Nodes)
	}
	if d.Nodes[2].Status.Detail != "" {
		t.Error("Probe must not modify the original diagram")
	}
}

func TestRenderSVG_NodesEdgesAndEscaping(t *testing.T) {
	d := Diagram{
		Nodes: []Node{
			{ID: "app", Title: "jarvisapp <:8090>", Role: "web", Col: 1, Row: 0, Status: Status{Health: Up, Detail: "a&b"}},
			{ID: "vlm", Title: "VLM", Col: 2, Row: 0, Status: Status{Health: Down}},
			{ID: "x", Title: "Sans sonde", Col: 0, Row: 1},
		},
		Edges: []Edge{{From: "app", To: "vlm", Label: "pages <PNG>"}, {From: "app", To: "inconnu"}},
	}
	svg := RenderSVG(d)
	for _, want := range []string{"<svg", "jarvisapp &lt;:8090&gt;", "a&amp;b", "pages &lt;PNG&gt;", `class="node up"`, `class="node down"`, `class="node"`, `<path class="edge"`} {
		if !strings.Contains(svg, want) {
			t.Errorf("SVG lacks %q", want)
		}
	}
	if strings.Count(svg, `<path class="edge"`) != 1 {
		t.Error("an edge to an unknown node must be skipped")
	}
	if strings.Contains(svg, "<:8090>") {
		t.Error("unescaped title")
	}
}

// Entre deux rangées : du bas de la source au haut de la cible (les côtés
// restent aux liens d'une même rangée), arrivées décalées selon la colonne
// d'origine pour ne pas converger en un point.
func TestEdgePath_VerticalBetweenRowsWithSpreadArrivals(t *testing.T) {
	app := Node{ID: "app", Col: 1, Row: 1}
	fromLeft := Node{Col: 0, Row: 0}
	fromAbove := Node{Col: 1, Row: 0}
	x1, y1, x2, y2, _ := edgePath(fromLeft, app)
	if y1 != fromLeft.y()+nodeH || y2 != app.y() {
		t.Errorf("edge between rows goes from y=%d to y=%d, want bottom %d to top %d", y1, y2, fromLeft.y()+nodeH, app.y())
	}
	_, _, ax2, _, _ := edgePath(fromAbove, app)
	if ax2 == x2 {
		t.Errorf("arrivals from different columns converge at x=%d", x2)
	}
	if x1 <= fromLeft.x() || x1 >= fromLeft.x()+nodeW {
		t.Errorf("departure x=%d outside the source box", x1)
	}
	lx1, ly1, _, _, _ := edgePath(app, Node{Col: 0, Row: 1})
	if lx1 != app.x() || ly1 != app.y()+nodeH/2 {
		t.Errorf("same-row edge should leave from the side: (%d,%d)", lx1, ly1)
	}
}
