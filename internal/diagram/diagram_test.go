package diagram

import (
	"fmt"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/codemap"
)

const app = "example.com/app"

func ref(name string) codemap.TypeRef { return codemap.TypeRef{Package: app, Name: name} }

// testModel : Job -> *Result -> []PageContent / MergedResult ;
// Manager -> Store (interface) <- implémentée par FakeStore ; Unrelated
// isolé ; Big a 20 champs dont le 16e référence Result.
func testModel() *codemap.Model {
	fieldRef := func(name, typ string, target string, many, optional bool) codemap.FieldInfo {
		return codemap.FieldInfo{Name: name, Type: typ, Refs: []codemap.TypeRef{ref(target)}, Many: many, Optional: optional}
	}
	big := &codemap.TypeInfo{Name: "Big", Package: app, Kind: codemap.KindStruct}
	for i := 0; i < 20; i++ {
		f := codemap.FieldInfo{Name: fmt.Sprintf("F%02d", i), Type: "int"}
		if i == 15 {
			f = fieldRef("F15", "Result", "Result", false, false)
		}
		big.Fields = append(big.Fields, f)
	}
	return &codemap.Model{ModulePath: app, Packages: []*codemap.Package{{Path: app, Name: "app", Types: []*codemap.TypeInfo{
		{Name: "Job", Package: app, Kind: codemap.KindStruct, Fields: []codemap.FieldInfo{
			{Name: "ID", Type: "string"},
			fieldRef("Result", "*Result", "Result", false, true),
			{Name: "Tags", Type: "[]string", Many: true},
		}},
		{Name: "Result", Package: app, Kind: codemap.KindStruct, Fields: []codemap.FieldInfo{
			{Name: "Path", Type: "string"},
			fieldRef("Pages", "[]PageContent", "PageContent", true, false),
			fieldRef("Merged", "MergedResult", "MergedResult", false, false),
		}},
		{Name: "PageContent", Package: app, Kind: codemap.KindStruct, Fields: []codemap.FieldInfo{{Name: "Text", Type: "string"}}},
		{Name: "MergedResult", Package: app, Kind: codemap.KindStruct},
		{Name: "Manager", Package: app, Kind: codemap.KindStruct, Fields: []codemap.FieldInfo{fieldRef("Store", "Store", "Store", false, false)}},
		{Name: "Store", Package: app, Kind: codemap.KindInterface, Methods: []codemap.MethodInfo{{Name: "Get", Signature: "func(id string) (Job, error)"}},
			ImplementedBy: []codemap.TypeRef{ref("FakeStore")}},
		{Name: "FakeStore", Package: app, Kind: codemap.KindStruct, Implements: []codemap.TypeRef{ref("Store")}},
		{Name: "Unrelated", Package: app, Kind: codemap.KindStruct},
		big,
	}}}}
}

func nodeByName(t *testing.T, d Diagram, name string) Node {
	t.Helper()
	for _, n := range d.Nodes {
		if n.Ref.Name == name {
			return n
		}
	}
	t.Fatalf("node %s not in diagram (nodes: %v)", name, names(d))
	return Node{}
}

func names(d Diagram) []string {
	var out []string
	for _, n := range d.Nodes {
		out = append(out, n.Ref.Name)
	}
	return out
}

func hasNode(d Diagram, name string) bool {
	for _, n := range d.Nodes {
		if n.Ref.Name == name {
			return true
		}
	}
	return false
}

func edge(t *testing.T, d Diagram, from, to string) Edge {
	t.Helper()
	for _, e := range d.Edges {
		if e.From.Name == from && e.To.Name == to {
			return e
		}
	}
	t.Fatalf("edge %s -> %s not found in %+v", from, to, d.Edges)
	return Edge{}
}

func TestNeighborhood_Depth1_ContainedRightReferencingLeft(t *testing.T) {
	d, ok := Neighborhood(testModel(), ref("Result"), Options{Depth: 1})
	if !ok {
		t.Fatal("Neighborhood() ok = false")
	}
	for _, want := range []string{"Result", "PageContent", "MergedResult", "Job", "Big"} {
		if !hasNode(d, want) {
			t.Errorf("node %s missing (nodes: %v)", want, names(d))
		}
	}
	for _, absent := range []string{"Unrelated", "Manager", "Store"} {
		if hasNode(d, absent) {
			t.Errorf("node %s should not be in a depth-1 neighbourhood of Result", absent)
		}
	}
	if !nodeByName(t, d, "Result").Focus {
		t.Error("Result should be marked as focus")
	}

	job, res, pages := nodeByName(t, d, "Job"), nodeByName(t, d, "Result"), nodeByName(t, d, "PageContent")
	if !(job.X < res.X && res.X < pages.X) {
		t.Errorf("x: Job=%v Result=%v PageContent=%v, want referencing types left, contained types right", job.X, res.X, pages.X)
	}
}

func TestNeighborhood_EdgesCarryFieldNameAndCardinality(t *testing.T) {
	d, _ := Neighborhood(testModel(), ref("Result"), Options{Depth: 1})

	if e := edge(t, d, "Job", "Result"); e.Label != "Result" || e.Card != "0..1" || e.Kind != EdgeField {
		t.Errorf("Job->Result = %+v, want field Result, 0..1", e)
	}
	if e := edge(t, d, "Result", "PageContent"); e.Label != "Pages" || e.Card != "*" {
		t.Errorf("Result->PageContent = %+v, want Pages, *", e)
	}
	if e := edge(t, d, "Result", "MergedResult"); e.Card != "1" {
		t.Errorf("Result->MergedResult card = %q, want 1", e.Card)
	}
}

// La flèche part de la ligne du champ qui porte la référence — un
// diagramme de données où l'on voit quel champ pointe vers quoi.
func TestNeighborhood_EdgeStartsAtItsFieldRow(t *testing.T) {
	d, _ := Neighborhood(testModel(), ref("Result"), Options{Depth: 1})
	job := nodeByName(t, d, "Job")
	e := edge(t, d, "Job", "Result")

	if e.Row != 1 {
		t.Fatalf("Job->Result row = %d, want 1 (field Result is Job's second row)", e.Row)
	}
	if want := job.Y + HeaderHeight + float64(e.Row)*RowHeight + RowHeight/2; e.SY != want {
		t.Errorf("edge start y = %v, want %v (middle of the Result row)", e.SY, want)
	}
	if e.SX != job.X+job.W {
		t.Errorf("edge start x = %v, want Job's right side %v (target is on the right)", e.SX, job.X+job.W)
	}
}

func TestNeighborhood_Depth2ReachesFurther(t *testing.T) {
	d1, _ := Neighborhood(testModel(), ref("Job"), Options{Depth: 1})
	d2, _ := Neighborhood(testModel(), ref("Job"), Options{Depth: 2})
	if hasNode(d1, "PageContent") {
		t.Error("PageContent is two hops from Job, should not appear at depth 1")
	}
	if !hasNode(d2, "PageContent") {
		t.Error("PageContent should appear at depth 2")
	}
}

func TestNeighborhood_ImplementsOnlyWhenAsked(t *testing.T) {
	without, _ := Neighborhood(testModel(), ref("Store"), Options{Depth: 1})
	if hasNode(without, "FakeStore") {
		t.Error("FakeStore should only appear with Implements: true")
	}
	if !hasNode(without, "Manager") {
		t.Error("Manager references Store through a field, should appear")
	}

	with, _ := Neighborhood(testModel(), ref("Store"), Options{Depth: 1, Implements: true})
	if e := edge(t, with, "FakeStore", "Store"); e.Kind != EdgeImplements {
		t.Errorf("FakeStore->Store kind = %s, want implements", e.Kind)
	}
	if rows := nodeByName(t, with, "Store").Rows; len(rows) != 1 || rows[0].Name != "Get" {
		t.Errorf("interface rows = %+v, want its methods", rows)
	}
}

func TestNeighborhood_NodesInAColumnNeverOverlap(t *testing.T) {
	d, _ := Neighborhood(testModel(), ref("Result"), Options{Depth: 2, Implements: true})
	for i, a := range d.Nodes {
		for _, b := range d.Nodes[i+1:] {
			if a.X == b.X && a.Y < b.Y+b.H && b.Y < a.Y+a.H {
				t.Errorf("%s and %s overlap in the same column", a.Ref.Name, b.Ref.Name)
			}
		}
	}
	for _, n := range d.Nodes {
		if n.X+n.W > d.Width || n.Y+n.H > d.Height || n.X < 0 || n.Y < 0 {
			t.Errorf("node %s (%v,%v %vx%v) outside the canvas %vx%v", n.Ref.Name, n.X, n.Y, n.W, n.H, d.Width, d.Height)
		}
	}
}

// Au-delà de MaxRows, les champs sont masqués — sauf ceux qui portent une
// flèche du diagramme, toujours visibles.
func TestNeighborhood_RowLimitKeepsReferencingFields(t *testing.T) {
	d, _ := Neighborhood(testModel(), ref("Result"), Options{Depth: 1, MaxRows: 5})
	big := nodeByName(t, d, "Big")
	if len(big.Rows) != 5 || big.More != 15 {
		t.Fatalf("Big rows = %d more = %d, want 5 shown, 15 hidden", len(big.Rows), big.More)
	}
	found := false
	for _, r := range big.Rows {
		if r.Name == "F15" {
			found = true
		}
	}
	if !found {
		t.Errorf("Big rows = %+v, want F15 (it references Result) kept visible", big.Rows)
	}
	if e := edge(t, d, "Big", "Result"); big.Rows[e.Row].Name != "F15" {
		t.Errorf("Big->Result anchored on row %d (%s), want F15", e.Row, big.Rows[e.Row].Name)
	}
}

func TestNeighborhood_UnknownFocus(t *testing.T) {
	if _, ok := Neighborhood(testModel(), ref("Nope"), Options{Depth: 1}); ok {
		t.Error("Neighborhood() ok = true for an unknown type")
	}
}

// Plusieurs flèches vers une même boîte, du même côté, arrivent à des
// hauteurs distinctes (réparties sur le bord, dans l'ordre vertical des
// sources) — sinon elles convergent en un point et leurs cardinalités se
// superposent.
func TestNeighborhood_IncomingEdgesAreSpreadAlongTheTargetSide(t *testing.T) {
	d, _ := Neighborhood(testModel(), ref("Result"), Options{Depth: 1})
	fromJob, fromBig := edge(t, d, "Job", "Result"), edge(t, d, "Big", "Result")
	if fromJob.TY == fromBig.TY {
		t.Fatalf("Job->Result and Big->Result both arrive at y=%v, want distinct anchors", fromJob.TY)
	}
	res := nodeByName(t, d, "Result")
	for _, e := range []Edge{fromJob, fromBig} {
		if e.TY <= res.Y || e.TY >= res.Y+res.H {
			t.Errorf("anchor y=%v outside Result's box [%v, %v]", e.TY, res.Y, res.Y+res.H)
		}
	}
	// Ordre préservé : la source la plus haute arrive le plus haut.
	job, big := nodeByName(t, d, "Job"), nodeByName(t, d, "Big")
	if (job.Y < big.Y) != (fromJob.TY < fromBig.TY) {
		t.Errorf("anchors crossed: Job.Y=%v Big.Y=%v but TY %v / %v", job.Y, big.Y, fromJob.TY, fromBig.TY)
	}
}

// L'en-tête d'une boîte affiche le nom (gras) à gauche et le package à
// droite : la largeur doit tenir compte des deux, sinon ils se
// chevauchent (vu sur PageContent / pipeline).
func TestNeighborhood_HeaderWidthFitsNameAndPackage(t *testing.T) {
	m := testModel()
	m.Packages[0].Name = "averylongpackagename"
	d, _ := Neighborhood(m, ref("PageContent"), Options{Depth: 1})
	n := nodeByName(t, d, "PageContent")
	need := float64(len("PageContent"))*8.2 + float64(len("averylongpackagename"))*6.8 + 30
	if n.W < need {
		t.Errorf("PageContent width = %v, want >= %v to fit name and package side by side", n.W, need)
	}
}
