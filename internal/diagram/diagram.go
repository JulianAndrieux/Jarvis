// Package diagram calcule le diagramme navigable du modèle de données
// (jalon 24) : le voisinage d'un type du module (ce qu'il contient, ce qui
// le référence), mis en page en colonnes, prêt à être dessiné en SVG.
// Fonction pure sur un codemap.Model — aucune dépendance de rendu,
// aucune bibliothèque de graphes : la disposition est volontairement
// simple (couches par distance au type central, ordre par barycentre),
// ce qui suffit pour un voisinage de quelques dizaines de types.
package diagram

import (
	"fmt"
	"sort"

	"github.com/JulianAndrieux/Jarvis/internal/codemap"
)

// Dimensions du rendu, partagées avec le template SVG.
const (
	HeaderHeight = 30.0
	RowHeight    = 18.0
	nodePadding  = 6.0
	columnGap    = 120.0
	nodeGap      = 26.0
	margin       = 30.0
	minWidth     = 150.0
	maxWidth     = 380.0
	charWidth    = 7.0 // police monospace 12px
	defaultRows  = 12
)

// EdgeKind est la nature d'une relation.
type EdgeKind string

const (
	EdgeField      EdgeKind = "field"      // A a un champ de type B
	EdgeEmbed      EdgeKind = "embed"      // A embed B (l'"héritage" Go)
	EdgeImplements EdgeKind = "implements" // A implémente l'interface B
)

// Options règle l'étendue du voisinage.
type Options struct {
	// Depth : nombre de sauts depuis le type central, de chaque côté.
	// 0 -> 1.
	Depth int
	// Implements ajoute les relations "implémente" (masquées par défaut :
	// elles relient vite tous les types à quelques interfaces centrales).
	Implements bool
	// MaxRows borne les lignes affichées par boîte. 0 -> 12.
	MaxRows int
}

// Row est une ligne d'une boîte : un champ (struct) ou une méthode
// (interface).
type Row struct {
	Name string
	Type string
}

// Node est une boîte du diagramme, positionnée.
type Node struct {
	Ref     codemap.TypeRef
	Package string // nom court du package (ex. "pipeline")
	Kind    codemap.Kind
	Focus   bool
	Rows    []Row
	More    int // lignes masquées
	Layer   int // distance signée au type central (négatif : le référence)

	X, Y, W, H float64
}

// Edge est une relation dessinée, de From vers To.
type Edge struct {
	From, To codemap.TypeRef
	Kind     EdgeKind
	Label    string // nom du champ
	Card     string // "1", "0..1", "*" (champs) ; "" sinon
	Row      int    // ligne de départ dans la boîte From, -1 = en-tête

	SX, SY, TX, TY float64
	Path           string // chemin SVG (courbe de Bézier)

	loop bool // même colonne : boucle par la droite
}

// Diagram est le résultat mis en page.
type Diagram struct {
	Nodes         []Node
	Edges         []Edge
	Width, Height float64
}

type rawEdge struct {
	from, to codemap.TypeRef
	kind     EdgeKind
	field    string
	card     string
}

// Neighborhood construit le diagramme autour de focus : les types qu'il
// contient (à droite) et ceux qui le référencent (à gauche), jusqu'à
// opts.Depth sauts. ok=false si focus n'existe pas dans m.
func Neighborhood(m *codemap.Model, focus codemap.TypeRef, opts Options) (Diagram, bool) {
	if opts.Depth <= 0 {
		opts.Depth = 1
	}
	if opts.MaxRows <= 0 {
		opts.MaxRows = defaultRows
	}

	all := map[codemap.TypeRef]*codemap.TypeInfo{}
	pkgNames := map[string]string{}
	for _, p := range m.Packages {
		pkgNames[p.Path] = p.Name
		for _, t := range p.Types {
			all[codemap.TypeRef{Package: t.Package, Name: t.Name}] = t
		}
	}
	if _, ok := all[focus]; !ok {
		return Diagram{}, false
	}

	edges := relations(all, opts.Implements)
	out, in := map[codemap.TypeRef][]codemap.TypeRef{}, map[codemap.TypeRef][]codemap.TypeRef{}
	for _, e := range edges {
		out[e.from] = append(out[e.from], e.to)
		in[e.to] = append(in[e.to], e.from)
	}

	layer := map[codemap.TypeRef]int{focus: 0}
	expand(layer, focus, out, opts.Depth, 1)
	expand(layer, focus, in, opts.Depth, -1)

	// Relations entre types retenus, dédoublonnées.
	var kept []rawEdge
	seen := map[string]bool{}
	for _, e := range edges {
		_, okFrom := layer[e.from]
		_, okTo := layer[e.to]
		key := fmt.Sprint(e.from, e.to, e.kind, e.field)
		if okFrom && okTo && !seen[key] {
			seen[key] = true
			kept = append(kept, e)
		}
	}

	nodes := map[codemap.TypeRef]*Node{}
	for r, l := range layer {
		nodes[r] = buildNode(all[r], pkgNames[r.Package], l, r == focus, kept, opts.MaxRows)
	}
	columns := orderColumns(nodes, kept)
	place(columns)

	d := Diagram{}
	for _, col := range columns {
		for _, n := range col {
			d.Nodes = append(d.Nodes, *n)
		}
	}
	for _, e := range kept {
		d.Edges = append(d.Edges, route(e, nodes[e.from], nodes[e.to]))
	}
	spreadArrivals(d.Edges, nodes)
	for i := range d.Edges {
		d.Edges[i].Path = curve(d.Edges[i])
	}
	d.Width, d.Height = canvasSize(d.Nodes)
	return d, true
}

// relations énumère toutes les relations du modèle entre types connus.
func relations(all map[codemap.TypeRef]*codemap.TypeInfo, implements bool) []rawEdge {
	refs := make([]codemap.TypeRef, 0, len(all))
	for r := range all {
		refs = append(refs, r)
	}
	sortRefs(refs) // ordre déterministe

	var edges []rawEdge
	for _, r := range refs {
		t := all[r]
		for _, f := range t.Fields {
			for _, target := range f.Refs {
				if _, ok := all[target]; !ok {
					continue
				}
				e := rawEdge{from: r, to: target, kind: EdgeField, field: f.Name, card: cardinality(f)}
				if f.Anonymous {
					e.kind, e.card = EdgeEmbed, ""
				}
				edges = append(edges, e)
			}
		}
		if implements {
			for _, iface := range t.Implements {
				if _, ok := all[iface]; ok {
					edges = append(edges, rawEdge{from: r, to: iface, kind: EdgeImplements})
				}
			}
		}
	}
	return edges
}

func cardinality(f codemap.FieldInfo) string {
	switch {
	case f.Many:
		return "*"
	case f.Optional:
		return "0..1"
	default:
		return "1"
	}
}

// expand parcourt adj en largeur depuis focus sur depth sauts, en notant
// la couche sign*distance des types pas encore placés.
func expand(layer map[codemap.TypeRef]int, focus codemap.TypeRef, adj map[codemap.TypeRef][]codemap.TypeRef, depth, sign int) {
	frontier := []codemap.TypeRef{focus}
	for d := 1; d <= depth; d++ {
		var next []codemap.TypeRef
		for _, n := range frontier {
			for _, m := range adj[n] {
				if _, placed := layer[m]; !placed {
					layer[m] = sign * d
					next = append(next, m)
				}
			}
		}
		frontier = next
	}
}

// buildNode choisit les lignes affichées : dans l'ordre de déclaration,
// en gardant toujours celles qui portent une flèche du diagramme.
func buildNode(t *codemap.TypeInfo, pkg string, layer int, focus bool, edges []rawEdge, maxRows int) *Node {
	n := &Node{Ref: codemap.TypeRef{Package: t.Package, Name: t.Name}, Package: pkg, Kind: t.Kind, Focus: focus, Layer: layer}

	var all []Row
	if t.Kind == codemap.KindInterface {
		for _, meth := range t.Methods {
			all = append(all, Row{Name: meth.Name, Type: trimFunc(meth.Signature)})
		}
	} else {
		for _, f := range t.Fields {
			all = append(all, Row{Name: f.Name, Type: f.Type})
		}
	}

	if len(all) > maxRows {
		linked := map[string]bool{}
		for _, e := range edges {
			if e.from == n.Ref && e.field != "" {
				linked[e.field] = true
			}
		}
		keep := map[int]bool{}
		for i, r := range all {
			if linked[r.Name] && len(keep) < maxRows {
				keep[i] = true
			}
		}
		for i := range all {
			if len(keep) >= maxRows {
				break
			}
			keep[i] = true
		}
		var rows []Row
		for i, r := range all {
			if keep[i] {
				rows = append(rows, r)
			}
		}
		n.More = len(all) - len(rows)
		all = rows
	}
	n.Rows = all

	// En-tête : nom en gras (~8.2 px/car.) à gauche, package (et
	// « interface ») en petit monospace (~6.8 px/car.) à droite.
	label := len(pkg)
	if t.Kind == codemap.KindInterface {
		label += len("«interface» ")
	}
	w := float64(len(t.Name))*8.2 + float64(label)*6.8 + 30 - 24
	for _, r := range n.Rows {
		if rw := float64(len(r.Name)+len(r.Type)+2) * charWidth; rw > w {
			w = rw
		}
	}
	n.W = clamp(w+24, minWidth, maxWidth)
	rows := len(n.Rows)
	if n.More > 0 {
		rows++
	}
	n.H = HeaderHeight + float64(rows)*RowHeight + nodePadding
	return n
}

func trimFunc(sig string) string {
	if len(sig) > 4 && sig[:4] == "func" {
		return sig[4:]
	}
	return sig
}

// orderColumns range les boîtes par couche, puis ordonne chaque colonne
// par barycentre de ses voisins dans la colonne plus proche du centre —
// pour limiter les croisements de flèches.
func orderColumns(nodes map[codemap.TypeRef]*Node, edges []rawEdge) [][]*Node {
	byLayer := map[int][]*Node{}
	var layers []int
	for _, n := range nodes {
		if _, ok := byLayer[n.Layer]; !ok {
			layers = append(layers, n.Layer)
		}
		byLayer[n.Layer] = append(byLayer[n.Layer], n)
	}
	sort.Ints(layers)
	for _, l := range layers {
		col := byLayer[l]
		sort.Slice(col, func(i, j int) bool { return lessRef(col[i].Ref, col[j].Ref) })
	}

	neighbours := map[codemap.TypeRef][]codemap.TypeRef{}
	for _, e := range edges {
		neighbours[e.from] = append(neighbours[e.from], e.to)
		neighbours[e.to] = append(neighbours[e.to], e.from)
	}
	reorder := func(l, towards int) {
		index := map[codemap.TypeRef]int{}
		for i, n := range byLayer[towards] {
			index[n.Ref] = i
		}
		col := byLayer[l]
		key := make(map[codemap.TypeRef]float64, len(col))
		for i, n := range col {
			sum, count := 0.0, 0
			for _, nb := range neighbours[n.Ref] {
				if idx, ok := index[nb]; ok {
					sum += float64(idx)
					count++
				}
			}
			if count == 0 {
				key[n.Ref] = float64(i) // sans voisin : garde sa place
			} else {
				key[n.Ref] = sum / float64(count)
			}
		}
		sort.SliceStable(col, func(i, j int) bool { return key[col[i].Ref] < key[col[j].Ref] })
	}
	for l := 1; ; l++ {
		_, pos := byLayer[l]
		_, neg := byLayer[-l]
		if !pos && !neg {
			break
		}
		if pos {
			reorder(l, l-1)
		}
		if neg {
			reorder(-l, -l+1)
		}
	}

	columns := make([][]*Node, len(layers))
	for i, l := range layers {
		columns[i] = byLayer[l]
	}
	return columns
}

// place attribue les coordonnées : colonnes de gauche à droite, boîtes
// empilées et centrées verticalement.
func place(columns [][]*Node) {
	heights := make([]float64, len(columns))
	tallest := 0.0
	for i, col := range columns {
		for j, n := range col {
			heights[i] += n.H
			if j > 0 {
				heights[i] += nodeGap
			}
		}
		if heights[i] > tallest {
			tallest = heights[i]
		}
	}

	x := margin
	for i, col := range columns {
		width := 0.0
		y := margin + (tallest-heights[i])/2
		for _, n := range col {
			n.X, n.Y = x, y
			y += n.H + nodeGap
			if n.W > width {
				width = n.W
			}
		}
		x += width + columnGap
	}
}

// route calcule le tracé d'une relation : départ sur la ligne du champ
// (côté de la cible), arrivée sur l'en-tête de la cible.
func route(e rawEdge, from, to *Node) Edge {
	out := Edge{From: e.from, To: e.to, Kind: e.kind, Label: e.field, Card: e.card, Row: -1}
	if e.field != "" {
		for i, r := range from.Rows {
			if r.Name == e.field {
				out.Row = i
			}
		}
	}

	out.SY = from.Y + HeaderHeight/2
	if out.Row >= 0 {
		out.SY = from.Y + HeaderHeight + float64(out.Row)*RowHeight + RowHeight/2
	}
	out.TY = to.Y + HeaderHeight/2

	switch {
	case to.X > from.X:
		out.SX, out.TX = from.X+from.W, to.X
	case to.X < from.X:
		out.SX, out.TX = from.X, to.X+to.W
	default: // même colonne (ou référence à soi-même) : boucle par la droite
		out.SX, out.TX = from.X+from.W, to.X+to.W
		out.loop = true
	}
	return out
}

// spreadArrivals répartit sur le bord de la boîte cible les flèches qui
// y arrivent du même côté, dans l'ordre vertical de leurs départs — sans
// quoi elles convergent en un point et leurs cardinalités se superposent.
func spreadArrivals(edges []Edge, nodes map[codemap.TypeRef]*Node) {
	type side struct {
		to    codemap.TypeRef
		right bool
	}
	groups := map[side][]int{}
	for i, e := range edges {
		to := nodes[e.To]
		key := side{to: e.To, right: e.TX == to.X+to.W}
		groups[key] = append(groups[key], i)
	}
	for key, idx := range groups {
		if len(idx) < 2 {
			continue
		}
		sort.SliceStable(idx, func(a, b int) bool { return edges[idx[a]].SY < edges[idx[b]].SY })
		to := nodes[key.to]
		lo, hi := to.Y+8, to.Y+to.H-8
		for k, i := range idx {
			edges[i].TY = lo + (float64(k)+0.5)*(hi-lo)/float64(len(idx))
		}
	}
}

// curve trace la courbe de Bézier d'une relation, tangentes horizontales
// aux deux extrémités.
func curve(e Edge) string {
	if e.loop {
		const bulge = 50.0
		return fmt.Sprintf("M%.1f %.1f C%.1f %.1f %.1f %.1f %.1f %.1f", e.SX, e.SY, e.SX+bulge, e.SY, e.TX+bulge, e.TY, e.TX, e.TY)
	}
	dx := (e.TX - e.SX) / 2
	return fmt.Sprintf("M%.1f %.1f C%.1f %.1f %.1f %.1f %.1f %.1f", e.SX, e.SY, e.SX+dx, e.SY, e.TX-dx, e.TY, e.TX, e.TY)
}

func canvasSize(nodes []Node) (float64, float64) {
	w, h := 0.0, 0.0
	for _, n := range nodes {
		if r := n.X + n.W; r > w {
			w = r
		}
		if b := n.Y + n.H; b > h {
			h = b
		}
	}
	// Marge droite élargie : les boucles d'une même colonne dépassent.
	return w + margin + 60, h + margin
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func lessRef(a, b codemap.TypeRef) bool {
	if a.Package != b.Package {
		return a.Package < b.Package
	}
	return a.Name < b.Name
}

func sortRefs(refs []codemap.TypeRef) {
	sort.Slice(refs, func(i, j int) bool { return lessRef(refs[i], refs[j]) })
}
