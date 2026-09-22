package main

import (
	"sort"
	"strings"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/codemap"
	"github.com/JulianAndrieux/Jarvis/internal/diagram"
	"github.com/JulianAndrieux/Jarvis/internal/highlight"
	"github.com/JulianAndrieux/Jarvis/internal/testmap"
)

// Construction des vues des pages Classes, Modèle et Tests (jalon 24) à
// partir du modèle de code. Appelé sous s.mu (lecture).

// shortPath retire le préfixe du module d'un chemin d'import, pour
// l'affichage ("internal/pipeline" plutôt que "github.com/.../internal/
// pipeline").
func (s *Server) shortPath(path string) string {
	if s.model != nil && s.model.ModulePath != "" {
		return s.model.ShortPath(path)
	}
	if s.modulePath != "" {
		if rest, ok := strings.CutPrefix(path, s.modulePath+"/"); ok {
			return rest
		}
	}
	return path
}

// knownTypeNames : tous les noms de types du module, pour colorer un
// extrait de code comme VS Code (un type utilisé hors de sa déclaration
// reste turquoise).
func (s *Server) knownTypeNames() map[string]bool {
	names := map[string]bool{}
	for _, p := range s.model.Packages {
		for _, t := range p.Types {
			names[t.Name] = true
		}
	}
	return names
}

func toCodeView(src, file string, line int, opts highlight.Options) templates.CodeView {
	if src == "" {
		return templates.CodeView{}
	}
	return templates.CodeView{Lines: highlight.Lines(highlight.Go(src, opts)), FirstLine: line, File: file}
}

func setOf(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// typeDetailView construit la fiche d'un type : UML, relations, code.
func (s *Server) typeDetailView(t *codemap.TypeInfo) templates.TypeDetailView {
	pkg, _ := s.model.Package(t.Package)
	opts := highlight.Options{Types: s.knownTypeNames()}
	if pkg != nil {
		opts.Packages = setOf(pkg.ImportNames)
	}

	d := templates.TypeDetailView{
		Name:         t.Name,
		Package:      t.Package,
		PackageLabel: s.shortPath(t.Package),
		Kind:         string(t.Kind),
		Code:         toCodeView(t.Source, t.File, t.Line, opts),
	}
	for _, f := range t.Fields {
		fv := templates.FieldInfoView{Name: f.Name, Type: f.Type, Tag: f.Tag, Anonymous: f.Anonymous, Exported: isExported(f.Name)}
		fv.Links = s.refViews(f.Refs)
		d.Fields = append(d.Fields, fv)
	}
	for _, m := range t.Methods {
		d.Methods = append(d.Methods, templates.MethodView{
			Name: m.Name, Signature: strings.TrimPrefix(m.Signature, "func"), PointerReceiver: m.PointerReceiver,
			Exported: isExported(m.Name),
			Code:     toCodeView(m.Source, m.File, m.Line, opts),
		})
	}
	d.Embeds = s.refViews(t.Embeds)
	d.EmbeddedBy = s.refViews(t.EmbeddedBy)
	d.Implements = s.refViews(t.Implements)
	d.ImplementedBy = s.refViews(t.ImplementedBy)
	d.ReferencedBy = s.refViews(s.referencedBy(codemap.TypeRef{Package: t.Package, Name: t.Name}))
	return d
}

// referencedBy : les types dont un champ (non embarqué) référence target.
func (s *Server) referencedBy(target codemap.TypeRef) []codemap.TypeRef {
	var out []codemap.TypeRef
	for _, p := range s.model.Packages {
		for _, t := range p.Types {
			for _, f := range t.Fields {
				if f.Anonymous {
					continue
				}
				for _, r := range f.Refs {
					if r == target {
						out = append(out, codemap.TypeRef{Package: t.Package, Name: t.Name})
					}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Package+out[i].Name < out[j].Package+out[j].Name })
	// Dédoublonne (plusieurs champs vers le même type).
	uniq := out[:0]
	for i, r := range out {
		if i == 0 || r != out[i-1] {
			uniq = append(uniq, r)
		}
	}
	return uniq
}

func (s *Server) refViews(refs []codemap.TypeRef) []templates.RefView {
	out := make([]templates.RefView, 0, len(refs))
	for _, r := range refs {
		out = append(out, templates.RefView{Package: r.Package, Name: r.Name, PackageLabel: s.shortPath(r.Package)})
	}
	return out
}

func isExported(name string) bool {
	return name != "" && strings.ToUpper(name[:1]) == name[:1] && strings.ToLower(name[:1]) != name[:1]
}

// defaultModelFocus choisit le type central du diagramme quand aucun
// n'est demandé : celui qui contient le plus d'autres types de données
// (structs distinctes référencées par ses champs), départagé par le
// nombre de types qui le référencent. Ni le plus référencé (sur Jarvis :
// schema.Field, une brique utilisée partout), ni celui qui dépend du
// plus d'interfaces de service (Pipeline, un orchestrateur) : le cœur du
// modèle de données.
func (s *Server) defaultModelFocus() (codemap.TypeRef, bool) {
	isStruct := map[codemap.TypeRef]bool{}
	for _, p := range s.model.Packages {
		for _, t := range p.Types {
			isStruct[codemap.TypeRef{Package: t.Package, Name: t.Name}] = t.Kind == codemap.KindStruct
		}
	}
	out := map[codemap.TypeRef]map[codemap.TypeRef]bool{}
	in := map[codemap.TypeRef]int{}
	for _, p := range s.model.Packages {
		for _, t := range p.Types {
			from := codemap.TypeRef{Package: t.Package, Name: t.Name}
			for _, f := range t.Fields {
				for _, r := range f.Refs {
					if r == from || !isStruct[r] {
						continue
					}
					if out[from] == nil {
						out[from] = map[codemap.TypeRef]bool{}
					}
					out[from][r] = true
					in[r]++
				}
			}
		}
	}
	var best codemap.TypeRef
	bestOut, bestIn := 0, 0
	for r, targets := range out {
		o, i := len(targets), in[r]
		better := o > bestOut || (o == bestOut && i > bestIn) ||
			(o == bestOut && i == bestIn && r.Package+r.Name < best.Package+best.Name)
		if better {
			best, bestOut, bestIn = r, o, i
		}
	}
	return best, bestOut > 0
}

// modelPageView assemble la page Modèle : diagramme, fiche du type
// central, sélecteur de types.
func (s *Server) modelPageView(focus codemap.TypeRef, opts diagram.Options) (templates.ModelPageView, bool) {
	t, ok := s.model.Type(focus.Package, focus.Name)
	if !ok {
		return templates.ModelPageView{}, false
	}
	d, ok := diagram.Neighborhood(s.model, focus, opts)
	if !ok {
		return templates.ModelPageView{}, false
	}
	v := templates.ModelPageView{
		Diagram:    d,
		Focus:      s.typeDetailView(t),
		Depth:      opts.Depth,
		Implements: opts.Implements,
	}
	for _, p := range s.model.Packages {
		g := templates.TypeOptionGroup{Label: s.shortPath(p.Path)}
		for _, ti := range p.Types {
			if ti.Kind == codemap.KindOther {
				continue
			}
			g.Options = append(g.Options, templates.TypeOption{
				Value: p.Path + "#" + ti.Name, Label: ti.Name,
				Selected: p.Path == focus.Package && ti.Name == focus.Name,
			})
		}
		if len(g.Options) > 0 {
			v.Types = append(v.Types, g)
		}
	}
	return v, true
}

// testSourceView colore le code d'un test avec les imports de son fichier.
func (s *Server) testSourceView(tf testmap.TestFunc) templates.CodeView {
	opts := highlight.Options{Packages: setOf(tf.Imports)}
	if s.model != nil {
		opts.Types = s.knownTypeNames()
	}
	// La source commence au commentaire de doc éventuel, alors que
	// tf.Line est la ligne de "func" : on retranche les lignes de doc.
	docLines := 0
	for i, l := range strings.Split(tf.Source, "\n") {
		if strings.HasPrefix(l, "func ") {
			docLines = i
			break
		}
	}
	return toCodeView(tf.Source, tf.File, tf.Line-docLines, opts)
}
