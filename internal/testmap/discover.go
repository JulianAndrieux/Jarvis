// Package testmap découvre statiquement les fonctions de test d'un
// module Go (go/parser sur les *_test.go, aucune exécution) et les
// regroupe par package — les "catégories" de la page de tests demandée :
// le découpage naturel de ce projet (un paquet = un étage ou une
// préoccuption architecturale). Chaque test est aussi marqué
// unitaire/integration selon la contrainte de build de son fichier,
// cohérent avec le tag `integration` déjà utilisé dans tout le module.
package testmap

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// TestFunc est une fonction de test top-level découverte statiquement
// (les sous-tests via t.Run ne sont pas énumérables sans exécution, et
// "lancer TestFoo" est de toute façon la granularité naturelle de `go
// test -run`).
type TestFunc struct {
	Name        string
	File        string // chemin relatif à moduleDir
	Line        int
	Integration bool
}

// Category regroupe les tests d'un même package.
type Category struct {
	Package string // chemin d'import
	Tests   []TestFunc
}

// Discover parcourt moduleDir à la recherche de fichiers *_test.go et
// retourne les catégories (une par package contenant au moins un test),
// triées par chemin d'import ; les tests de chaque catégorie sont triés
// par nom.
func Discover(moduleDir string) ([]Category, error) {
	modulePath, err := readModulePath(moduleDir)
	if err != nil {
		return nil, err
	}

	byPkg := map[string][]TestFunc{}

	err = filepath.WalkDir(moduleDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, err := filepath.Rel(moduleDir, path)
		if err != nil {
			return err
		}
		pkgPath := modulePath + "/" + filepath.ToSlash(filepath.Dir(rel))
		pkgPath = strings.TrimSuffix(pkgPath, "/.")

		tests, err := parseTestFile(path, rel)
		if err != nil {
			return fmt.Errorf("testmap: parse %s: %w", rel, err)
		}
		if len(tests) > 0 {
			byPkg[pkgPath] = append(byPkg[pkgPath], tests...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	categories := make([]Category, 0, len(byPkg))
	for pkg, tests := range byPkg {
		sort.Slice(tests, func(i, j int) bool { return tests[i].Name < tests[j].Name })
		categories = append(categories, Category{Package: pkg, Tests: tests})
	}
	sort.Slice(categories, func(i, j int) bool { return categories[i].Package < categories[j].Package })

	return categories, nil
}

func parseTestFile(path, rel string) ([]TestFunc, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	integration := hasIntegrationBuildTag(file)

	var tests []TestFunc
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil { // pas une méthode
			continue
		}
		if !strings.HasPrefix(fn.Name.Name, "Test") {
			continue
		}
		if !hasTestingTParam(fn) {
			continue
		}
		pos := fset.Position(fn.Pos())
		tests = append(tests, TestFunc{
			Name: fn.Name.Name, File: rel, Line: pos.Line, Integration: integration,
		})
	}
	return tests, nil
}

// hasTestingTParam vérifie que fn a exactement un paramètre de type
// *testing.T — texte de l'expression plutôt que type-checking complet
// (suffisant : on ne cherche pas à valider le code, juste à repérer les
// fonctions de test).
func hasTestingTParam(fn *ast.FuncDecl) bool {
	params := fn.Type.Params
	if params == nil || len(params.List) != 1 || len(params.List[0].Names) != 1 {
		return false
	}
	star, ok := params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	return ok && pkgIdent.Name == "testing" && sel.Sel.Name == "T"
}

// hasIntegrationBuildTag repère la contrainte de build `//go:build
// integration` (ou l'ancienne syntaxe `// +build integration`) parmi les
// commentaires précédant la clause package. Lit le texte brut de chaque
// *ast.Comment (pas CommentGroup.Text(), qui *retire* volontairement les
// commentaires-directives comme "//go:build" de son résultat).
func hasIntegrationBuildTag(file *ast.File) bool {
	for _, cg := range file.Comments {
		if file.Package.IsValid() && cg.Pos() >= file.Package {
			break // uniquement les commentaires avant "package"
		}
		for _, c := range cg.List {
			if strings.Contains(c.Text, "go:build") && strings.Contains(c.Text, "integration") {
				return true
			}
			if strings.Contains(c.Text, "+build") && strings.Contains(c.Text, "integration") {
				return true
			}
		}
	}
	return false
}

// readModulePath lit le "module ..." déclaré dans go.mod à la racine de
// moduleDir.
func readModulePath(moduleDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(moduleDir, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("testmap: read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(after), nil
		}
	}
	return "", fmt.Errorf("testmap: no module directive found in go.mod")
}
