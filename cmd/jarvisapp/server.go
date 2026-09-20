// Package main implémente jarvisapp : l'application web unique qui
// regroupe ce qui était avant deux binaires séparés — cmd/jarvisweb
// (upload/suivi de documents) et cmd/codebrowser (navigateur de classes
// façon Smalltalk / Glamorous Toolkit + page de tests). Une seule page à
// ouvrir en local, une seule nav (Upload · Classes · Tests), un seul
// port. Voir CLAUDE.md, jalon 13.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/codemap"
	"github.com/JulianAndrieux/Jarvis/internal/doctype"
	"github.com/JulianAndrieux/Jarvis/internal/testmap"
	"github.com/JulianAndrieux/Jarvis/internal/testrunner"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

// maxUploadSize borne la taille d'un document accepté (64 Mio).
const maxUploadSize = 64 << 20

// Server expose les deux domaines fonctionnels sur un seul routeur :
// upload/suivi de documents (Jobs/Registry, ex-jarvisweb) et navigateur
// de code/tests (ModuleDir + le cache model/categories/results,
// ex-codebrowser). Les deux restent des préoccupations séparées en
// interne (aucune dépendance croisée entre elles) — seule la présentation
// (nav, layout) est commune.
type Server struct {
	Jobs     *webapp.JobManager
	Registry *doctype.Registry

	ModuleDir string

	mu         sync.RWMutex
	model      *codemap.Model
	categories []testmap.Category
	results    map[string]testrunner.TestResult // clé "pkg\x00nom" -> dernier résultat connu
	modulePath string                           // ex. "github.com/JulianAndrieux/Jarvis", lu depuis go.mod
}

func resultKey(pkg, name string) string { return pkg + "\x00" + name }

// Refresh relance l'analyse statique (codemap) et la découverte des
// tests (testmap) pour le navigateur de code — n'affecte pas Jobs. Les
// résultats d'exécution déjà connus sont conservés à travers un rescan
// (rescanner le code ne relance pas les tests).
func (s *Server) Refresh() error {
	model, err := codemap.Analyze(s.ModuleDir, "./...")
	if err != nil {
		return fmt.Errorf("jarvisapp: analyse du code: %w", err)
	}
	categories, err := testmap.Discover(s.ModuleDir)
	if err != nil {
		return fmt.Errorf("jarvisapp: découverte des tests: %w", err)
	}

	s.mu.Lock()
	s.model = model
	s.categories = categories
	if s.results == nil {
		s.results = map[string]testrunner.TestResult{}
	}
	if s.modulePath == "" {
		s.modulePath = readModulePath(s.ModuleDir)
	}
	s.mu.Unlock()
	return nil
}

func readModulePath(moduleDir string) string {
	b, err := os.ReadFile(moduleDir + "/go.mod")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

func (s *Server) Routes() chi.Router {
	r := chi.NewRouter()

	// Upload / suivi de documents (ex-cmd/jarvisweb).
	r.Get("/", s.handleIndex)
	r.Post("/jobs", s.handleSubmit)
	r.Get("/jobs/{id}", s.handleJobStatus)

	// Navigateur de classes + tests (ex-cmd/codebrowser).
	r.Get("/classes", s.handleClasses)
	r.Get("/classes/detail", s.handleClassDetail)
	r.Get("/tests", s.handleTests)
	r.Post("/tests/run", s.handleTestsRun)
	r.Post("/refresh", s.handleRefresh)

	return r
}

// --- Upload / suivi de documents ---

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.Upload(s.Registry.Names()).Render(r.Context(), w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		http.Error(w, "formulaire invalide : "+err.Error(), http.StatusBadRequest)
		return
	}

	docType := r.FormValue("doc_type")

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "fichier manquant : "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "impossible de lire le fichier : "+err.Error(), http.StatusInternalServerError)
		return
	}

	job, err := s.Jobs.Submit(r.Context(), docType, header.Filename, content)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.renderJob(w, r, job)
}

func (s *Server) handleJobStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, ok, err := s.Jobs.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "erreur de lecture du job : "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.renderJob(w, r, job)
}

func (s *Server) renderJob(w http.ResponseWriter, r *http.Request, job webapp.Job) {
	view := buildResultView(job)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.JobFragment(job, view).Render(r.Context(), w); err != nil {
		fmt.Fprintf(os.Stderr, "jarvisapp: render job %s: %v\n", job.ID, err)
	}
}

// --- Rescan (partagé) ---

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if err := s.Refresh(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/classes", http.StatusFound)
}

// --- Classes ---

func (s *Server) packageSummaries() []templates.PackageSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]templates.PackageSummary, 0, len(s.model.Packages))
	for _, pkg := range s.model.Packages {
		types := make([]templates.TypeSummary, 0, len(pkg.Types))
		for _, t := range pkg.Types {
			types = append(types, templates.TypeSummary{Name: t.Name, Kind: string(t.Kind)})
		}
		out = append(out, templates.PackageSummary{Path: pkg.Path, Types: types})
	}
	return out
}

func (s *Server) typeDetail(pkgPath, name string) (templates.TypeDetailView, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	t, ok := s.model.Type(pkgPath, name)
	if !ok {
		return templates.TypeDetailView{}, false
	}

	d := templates.TypeDetailView{
		Name:    t.Name,
		Package: t.Package,
		Kind:    string(t.Kind),
	}
	for _, f := range t.Fields {
		d.Fields = append(d.Fields, templates.FieldInfoView{Name: f.Name, Type: f.Type, Tag: f.Tag, Anonymous: f.Anonymous})
	}
	for _, m := range t.Methods {
		d.Methods = append(d.Methods, templates.MethodView{Name: m.Name, Signature: m.Signature, PointerReceiver: m.PointerReceiver})
	}
	d.Embeds = refViews(t.Embeds)
	d.EmbeddedBy = refViews(t.EmbeddedBy)
	d.Implements = refViews(t.Implements)
	d.ImplementedBy = refViews(t.ImplementedBy)
	return d, true
}

func refViews(refs []codemap.TypeRef) []templates.RefView {
	out := make([]templates.RefView, 0, len(refs))
	for _, r := range refs {
		out = append(out, templates.RefView{Package: r.Package, Name: r.Name})
	}
	return out
}

func (s *Server) handleClasses(w http.ResponseWriter, r *http.Request) {
	packages := s.packageSummaries()

	var selected *templates.TypeDetailView
	if pkg, name := r.URL.Query().Get("pkg"), r.URL.Query().Get("name"); pkg != "" && name != "" {
		if d, ok := s.typeDetail(pkg, name); ok {
			selected = &d
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ClassesPage(packages, selected).Render(r.Context(), w); err != nil {
		fmt.Fprintf(os.Stderr, "jarvisapp: render classes: %v\n", err)
	}
}

func (s *Server) handleClassDetail(w http.ResponseWriter, r *http.Request) {
	pkg, name := r.URL.Query().Get("pkg"), r.URL.Query().Get("name")
	d, ok := s.typeDetail(pkg, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.TypeDetail(d).Render(r.Context(), w); err != nil {
		fmt.Fprintf(os.Stderr, "jarvisapp: render detail: %v\n", err)
	}
}

// --- Tests ---

func (s *Server) categoryViews() []templates.TestCategoryView {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]templates.TestCategoryView, 0, len(s.categories))
	for _, cat := range s.categories {
		rows := make([]templates.TestRowView, 0, len(cat.Tests))
		for _, tf := range cat.Tests {
			row := templates.TestRowView{Name: tf.Name, File: tf.File, Line: tf.Line, Integration: tf.Integration}
			if res, ok := s.results[resultKey(cat.Package, tf.Name)]; ok {
				row.Status = string(res.Status)
				row.Elapsed = res.Elapsed
				row.Output = res.Output
			}
			rows = append(rows, row)
		}
		out = append(out, templates.TestCategoryView{Package: cat.Package, Tests: rows})
	}
	return out
}

func (s *Server) categoryView(pkg string) (templates.TestCategoryView, bool) {
	for _, cat := range s.categoryViews() {
		if cat.Package == pkg {
			return cat, true
		}
	}
	return templates.TestCategoryView{}, false
}

func (s *Server) handleTests(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.TestsPage(s.categoryViews()).Render(r.Context(), w); err != nil {
		fmt.Fprintf(os.Stderr, "jarvisapp: render tests: %v\n", err)
	}
}

// handleTestsRun lance `go test` selon les paramètres de requête :
//   - aucun paramètre -> tout le module
//   - ?pkg=... -> uniquement ce package (une catégorie)
//   - ?pkg=...&name=... -> un seul test
//   - ?integration=1 -> ajoute -tags=integration
func (s *Server) handleTestsRun(w http.ResponseWriter, r *http.Request) {
	pkg := r.URL.Query().Get("pkg")
	name := r.URL.Query().Get("name")
	integration := r.URL.Query().Get("integration") == "1"

	opts := testrunner.Options{
		Dir:         s.ModuleDir,
		Patterns:    []string{"./..."},
		Integration: integration,
		Timeout:     5 * time.Minute,
	}
	if pkg != "" {
		s.mu.RLock()
		prefix := s.modulePath + "/"
		s.mu.RUnlock()
		opts.Patterns = []string{"./" + strings.TrimPrefix(pkg, prefix)}
	}
	if name != "" {
		opts.Run = "^" + name + "$"
	}

	ctx, cancel := context.WithTimeout(r.Context(), opts.Timeout+10*time.Second)
	defer cancel()

	results, err := testrunner.Run(ctx, opts)
	if err != nil {
		http.Error(w, "échec de lancement de go test : "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.recordResults(results)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	switch {
	case name != "" && pkg != "":
		cat, ok := s.categoryView(pkg)
		if !ok {
			http.NotFound(w, r)
			return
		}
		for _, row := range cat.Tests {
			if row.Name == name {
				if err := templates.TestRow(pkg, row).Render(r.Context(), w); err != nil {
					fmt.Fprintf(os.Stderr, "jarvisapp: render row: %v\n", err)
				}
				return
			}
		}
		http.NotFound(w, r)
	case pkg != "":
		cat, ok := s.categoryView(pkg)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if err := templates.TestCategoryCard(cat).Render(r.Context(), w); err != nil {
			fmt.Fprintf(os.Stderr, "jarvisapp: render category: %v\n", err)
		}
	default:
		if err := templates.TestCategories(s.categoryViews()).Render(r.Context(), w); err != nil {
			fmt.Fprintf(os.Stderr, "jarvisapp: render categories: %v\n", err)
		}
	}
}

// recordResults met à jour le cache des derniers résultats connus à
// partir d'une exécution de go test -json.
func (s *Server) recordResults(pkgResults []testrunner.PackageResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pr := range pkgResults {
		for _, tr := range pr.Tests {
			s.results[resultKey(pr.Package, tr.Name)] = tr
		}
	}
}
