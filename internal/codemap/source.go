package codemap

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/tools/go/packages"
)

// sourceSpan est le texte d'une déclaration et sa position.
type sourceSpan struct {
	text string
	file string // relatif à la racine du module
	line int
}

// packageSources indexe le texte des déclarations d'un package : types
// par nom, méthodes par "Type.Méthode".
type packageSources struct {
	types   map[string]sourceSpan
	methods map[string]sourceSpan
}

// collectSources lit, dans les fichiers du package, le texte de chaque
// déclaration de type et de méthode (commentaire de doc compris). Un
// fichier illisible est simplement ignoré : le code source est un bonus
// d'affichage, jamais une raison d'échouer l'analyse.
func collectSources(pkg *packages.Package) packageSources {
	out := packageSources{types: map[string]sourceSpan{}, methods: map[string]sourceSpan{}}
	if pkg.Fset == nil {
		return out
	}
	moduleDir := ""
	if pkg.Module != nil {
		moduleDir = pkg.Module.Dir
	}

	for _, file := range pkg.Syntax {
		tf := pkg.Fset.File(file.Pos())
		if tf == nil {
			continue
		}
		content, err := os.ReadFile(tf.Name())
		if err != nil {
			continue
		}
		rel := tf.Name()
		if moduleDir != "" {
			if r, err := filepath.Rel(moduleDir, tf.Name()); err == nil {
				rel = filepath.ToSlash(r)
			}
		}
		span := func(from, to token.Pos, prefix string) sourceSpan {
			start, end := tf.Offset(from), tf.Offset(to)
			return sourceSpan{text: prefix + string(content[start:end]), file: rel, line: tf.Line(from)}
		}

		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					ts := spec.(*ast.TypeSpec)
					if !d.Lparen.IsValid() {
						// "type X ..." seul : toute la déclaration, doc comprise.
						out.types[ts.Name.Name] = span(docStart(d.Doc, d.Pos()), d.End(), "")
						continue
					}
					// Bloc "type ( ... )" : la spécification seule, préfixée
					// de "type " pour rester du Go lisible hors contexte.
					from := ts.Pos()
					prefix := "type "
					if ts.Doc != nil {
						out.types[ts.Name.Name] = sourceSpan{
							text: string(content[tf.Offset(ts.Doc.Pos()):tf.Offset(ts.Pos())]) + prefix + string(content[tf.Offset(from):tf.Offset(ts.End())]),
							file: rel, line: tf.Line(ts.Doc.Pos()),
						}
						continue
					}
					out.types[ts.Name.Name] = span(from, ts.End(), prefix)
				}
			case *ast.FuncDecl:
				if d.Recv == nil || len(d.Recv.List) == 0 {
					continue
				}
				if recv := receiverTypeName(d.Recv.List[0].Type); recv != "" {
					out.methods[recv+"."+d.Name.Name] = span(docStart(d.Doc, d.Pos()), d.End(), "")
				}
			}
		}
	}
	return out
}

func docStart(doc *ast.CommentGroup, pos token.Pos) token.Pos {
	if doc != nil {
		return doc.Pos()
	}
	return pos
}

// receiverTypeName extrait "T" d'un récepteur "T", "*T", "T[K]" ou "*T[K, V]".
func receiverTypeName(expr ast.Expr) string {
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		case *ast.Ident:
			return e.Name
		default:
			return ""
		}
	}
}

// importNames retourne les noms sous lesquels le package importe
// d'autres packages : le nom déclaré de chaque import, plus les alias
// explicites des fichiers.
func importNames(pkg *packages.Package) []string {
	set := map[string]bool{}
	for _, imp := range pkg.Imports {
		if imp.Name != "" {
			set[imp.Name] = true
		}
	}
	for _, file := range pkg.Syntax {
		for _, imp := range file.Imports {
			if imp.Name != nil && imp.Name.Name != "_" && imp.Name.Name != "." {
				set[imp.Name.Name] = true
			}
		}
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
