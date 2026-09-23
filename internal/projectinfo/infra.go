package projectinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Health est l'état sondé d'un composant.
type Health string

const (
	Up   Health = "up"
	Down Health = "down"
	// Off : composant non configuré (dossier surveillé vide...).
	Off Health = "off"
)

// Status est le résultat d'une sonde.
type Status struct {
	Health Health
	Detail string
}

// Check sonde un composant ; elle respecte le délai de ctx.
type Check func(ctx context.Context) Status

// LlamaCheck sonde un serveur llama.cpp à partir de son URL d'API
// (".../v1") : /health, puis le nom du modèle servi.
func LlamaCheck(client *http.Client, apiURL string) Check {
	root := strings.TrimSuffix(strings.TrimSuffix(apiURL, "/"), "/v1")
	return func(ctx context.Context) Status {
		if err := getJSON(ctx, client, root+"/health", nil); err != nil {
			return Status{Health: Down, Detail: err.Error()}
		}
		var models struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		detail := "en service"
		if err := getJSON(ctx, client, root+"/v1/models", &models); err == nil && len(models.Data) > 0 {
			detail = filepath.Base(models.Data[0].ID)
		}
		return Status{Health: Up, Detail: detail}
	}
}

func getJSON(ctx context.Context, client *http.Client, url string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("délai dépassé")
		}
		return fmt.Errorf("injoignable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if into == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// BinaryCheck : le premier des outils trouvé sur le PATH.
func BinaryCheck(names ...string) Check {
	return func(context.Context) Status {
		for _, n := range names {
			if p, err := exec.LookPath(n); err == nil {
				return Status{Health: Up, Detail: p}
			}
		}
		return Status{Health: Down, Detail: "introuvable : " + strings.Join(names, ", ")}
	}
}

// DirCheck : un dossier configuré existe ("" : non configuré).
func DirCheck(path string) Check {
	return func(context.Context) Status {
		if path == "" {
			return Status{Health: Off, Detail: "non configuré"}
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return Status{Health: Down, Detail: "absent : " + path}
		}
		return Status{Health: Up, Detail: path}
	}
}

// FuncCheck adapte une fonction qui rend un détail ou une erreur.
func FuncCheck(f func(ctx context.Context) (string, error)) Check {
	return func(ctx context.Context) Status {
		detail, err := f(ctx)
		if err != nil {
			return Status{Health: Down, Detail: err.Error()}
		}
		return Status{Health: Up, Detail: detail}
	}
}

// Node est un composant de l'infrastructure, placé sur une grille.
type Node struct {
	ID, Title string
	// Role : une ligne de description ("VLM olmOCR-2, :8080").
	Role     string
	Col, Row int
	Check    Check
	Status   Status
}

// Edge relie deux composants.
type Edge struct {
	From, To, Label string
}

// Diagram est l'infrastructure : composants et flux.
type Diagram struct {
	Nodes []Node
	Edges []Edge
}

// Probe sonde tous les composants en parallèle, chacun borné par
// timeout, et retourne une copie du diagramme avec leur état.
func (d Diagram) Probe(ctx context.Context, timeout time.Duration) Diagram {
	out := Diagram{Nodes: append([]Node(nil), d.Nodes...), Edges: d.Edges}
	var wg sync.WaitGroup
	for i := range out.Nodes {
		if out.Nodes[i].Check == nil {
			continue
		}
		wg.Add(1)
		go func(n *Node) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			done := make(chan Status, 1)
			go func() { done <- n.Check(cctx) }()
			select {
			case st := <-done:
				n.Status = st
			case <-cctx.Done():
				n.Status = Status{Health: Down, Detail: "délai dépassé"}
			}
		}(&out.Nodes[i])
	}
	wg.Wait()
	return out
}

// Géométrie de la grille.
const (
	nodeW, nodeH = 230, 74
	colGap       = 110
	rowGap       = 52
	margin       = 16
)

func (n Node) x() int { return margin + n.Col*(nodeW+colGap) }
func (n Node) y() int { return margin + n.Row*(nodeH+rowGap) }

// RenderSVG dessine le diagramme ; tout texte est échappé. Les couleurs
// viennent des classes CSS de la page (node up/down/off, edge).
func RenderSVG(d Diagram) string {
	byID := map[string]Node{}
	maxCol, maxRow := 0, 0
	for _, n := range d.Nodes {
		byID[n.ID] = n
		maxCol, maxRow = max(maxCol, n.Col), max(maxRow, n.Row)
	}
	w := 2*margin + (maxCol+1)*nodeW + maxCol*colGap
	h := 2*margin + (maxRow+1)*nodeH + maxRow*rowGap
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="infra" viewBox="0 0 %d %d" width="%d" height="%d" xmlns="http://www.w3.org/2000/svg">`, w, h, w, h)
	b.WriteString(`<defs><marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" class="arrowhead"/></marker></defs>`)

	for _, e := range d.Edges {
		from, ok1 := byID[e.From]
		to, ok2 := byID[e.To]
		if !ok1 || !ok2 {
			continue
		}
		x1, y1, x2, y2, path := edgePath(from, to)
		fmt.Fprintf(&b, `<path class="edge" d="%s" marker-end="url(#arrow)"/>`, path)
		if e.Label != "" {
			fmt.Fprintf(&b, `<text class="edge-label" x="%d" y="%d" text-anchor="middle">%s</text>`, (x1+x2)/2, (y1+y2)/2-5, esc(e.Label))
		}
	}
	for _, n := range d.Nodes {
		class := "node"
		if n.Status.Health != "" {
			class += " " + string(n.Status.Health)
		}
		x, y := n.x(), n.y()
		fmt.Fprintf(&b, `<g class="%s"><title>%s</title>`, class, esc(n.Title+" — "+n.Status.Detail))
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="%d" rx="10"/>`, x, y, nodeW, nodeH)
		if n.Status.Health != "" {
			fmt.Fprintf(&b, `<circle class="dot" cx="%d" cy="%d" r="5"/>`, x+nodeW-16, y+18)
		}
		fmt.Fprintf(&b, `<text class="title" x="%d" y="%d">%s</text>`, x+12, y+22, esc(clip(n.Title, 26)))
		fmt.Fprintf(&b, `<text class="role" x="%d" y="%d">%s</text>`, x+12, y+42, esc(clip(n.Role, 34)))
		fmt.Fprintf(&b, `<text class="detail" x="%d" y="%d">%s</text>`, x+12, y+61, esc(clip(n.Status.Detail, 34)))
		b.WriteString(`</g>`)
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// edgePath : entre deux rangées, du bas de la source au haut de la cible,
// départs et arrivées décalés selon la colonne de l'autre bout (sinon
// tous les liens d'un nœud convergent en un point) ; dans une même
// rangée, d'un côté à l'autre. En courbe.
func edgePath(from, to Node) (x1, y1, x2, y2 int, d string) {
	const spread = 36
	switch {
	case to.Row != from.Row:
		dc := to.Col - from.Col
		x1, x2 = from.x()+nodeW/2+dc*spread, to.x()+nodeW/2-dc*spread
		y1, y2 = from.y()+nodeH, to.y()
		if to.Row < from.Row {
			y1, y2 = from.y(), to.y()+nodeH
		}
		my := (y1 + y2) / 2
		d = fmt.Sprintf("M%d,%d C%d,%d %d,%d %d,%d", x1, y1, x1, my, x2, my, x2, y2)
	case to.Col > from.Col:
		x1, y1, x2, y2 = from.x()+nodeW, from.y()+nodeH/2, to.x(), to.y()+nodeH/2
		d = fmt.Sprintf("M%d,%d L%d,%d", x1, y1, x2, y2)
	default:
		x1, y1, x2, y2 = from.x(), from.y()+nodeH/2, to.x()+nodeW, to.y()+nodeH/2
		d = fmt.Sprintf("M%d,%d L%d,%d", x1, y1, x2, y2)
	}
	return
}

func esc(s string) string { return html.EscapeString(s) }

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
