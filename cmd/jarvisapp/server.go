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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/codemap"
	"github.com/JulianAndrieux/Jarvis/internal/diagram"
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

	// Bibliothèque de documents (jalon 17-18).
	r.Get("/documents", s.handleDocuments)
	r.Get("/documents/{id}", s.handleDocumentDetail)
	r.Get("/documents/{id}/pdf", s.handleDocumentPDF)
	r.Get("/documents/{id}/thumbnail", s.handleDocumentThumbnail)
	r.Get("/documents/{id}/panel", s.handleDocumentPanel)
	r.Post("/documents/{id}/tags", s.handleDocumentTags)
	r.Post("/documents/{id}/reprocess", s.handleDocumentReprocess)
	r.Delete("/documents/{id}", s.handleDocumentDelete)

	// Navigateur de classes + tests (ex-cmd/codebrowser).
	r.Get("/classes", s.handleClasses)
	r.Get("/classes/detail", s.handleClassDetail)
	r.Get("/model", s.handleModel)
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

	// Le type de document n'est plus choisi ici : il est déterminé par
	// classification automatique pendant le traitement (internal/classify,
	// câblé dans pipeline.Pipeline.RunAuto — voir main.go).
	job, err := s.Jobs.Submit(r.Context(), header.Filename, content)
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

// --- Bibliothèque de documents ---
//
// Un "document" de cette bibliothèque EST un webapp.Job : même donnée,
// juste une seconde porte d'entrée (naviguer par date/recherche plutôt
// que suivre l'upload qui vient de se terminer). Aucune duplication de
// modèle ni de logique de rendu : le statut/l'extraction réutilisent
// templates.JobFragment tel quel, y compris son polling HTMX existant
// (GET /jobs/{id}) — un document en cours de ré-extraction s'actualise
// donc sans code de polling supplémentaire.

func (s *Server) handleDocuments(w http.ResponseWriter, r *http.Request) {
	search := r.URL.Query().Get("q")
	jobs, err := s.Jobs.List(r.Context(), webapp.ListQuery{Search: search, SummaryOnly: true})
	if err != nil {
		http.Error(w, "erreur de lecture des documents : "+err.Error(), http.StatusInternalServerError)
		return
	}

	rows := make([]templates.DocumentRow, len(jobs))
	for i, j := range jobs {
		rows[i] = templates.DocumentRow{
			ID: j.ID, Filename: j.Filename, DocType: j.DocType,
			Status: string(j.Status), Tags: j.Tags,
			CreatedAt: j.CreatedAt.Format("2006-01-02 15:04"),
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.DocumentsPage(rows, search).Render(r.Context(), w); err != nil {
		fmt.Fprintf(os.Stderr, "jarvisapp: render documents: %v\n", err)
	}
}

func (s *Server) handleDocumentDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, ok, err := s.Jobs.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "erreur de lecture du document : "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	view := buildResultView(job)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.DocumentDetailPage(job, view, s.Registry.Names()).Render(r.Context(), w); err != nil {
		fmt.Fprintf(os.Stderr, "jarvisapp: render document detail %s: %v\n", id, err)
	}
}

func (s *Server) handleDocumentPDF(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, ok, err := s.Jobs.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "erreur de lecture du document : "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Write(job.Content)
}

func (s *Server) handleDocumentTags(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulaire invalide : "+err.Error(), http.StatusBadRequest)
		return
	}

	tags := splitTags(r.FormValue("tags"))
	if err := s.Jobs.SetTags(r.Context(), id, tags); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.TagsForm(id, tags).Render(r.Context(), w); err != nil {
		fmt.Fprintf(os.Stderr, "jarvisapp: render tags form %s: %v\n", id, err)
	}
}

func (s *Server) handleDocumentReprocess(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulaire invalide : "+err.Error(), http.StatusBadRequest)
		return
	}

	docType := r.FormValue("doc_type")
	if err := s.Jobs.Reprocess(r.Context(), id, docType); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.handleDocumentPanel(w, r)
}

// handleDocumentPanel rend le volet droit de la vue détail (jalon 22) :
// sondé par lui-même tant que le traitement tourne, et renvoyé par la
// relance d'extraction pour remplacer le volet en place.
func (s *Server) handleDocumentPanel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, ok, err := s.Jobs.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "erreur de lecture du document : "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.DocumentPanel(job, buildResultView(job), s.Registry.Names()).Render(r.Context(), w); err != nil {
		fmt.Fprintf(os.Stderr, "jarvisapp: render document panel %s: %v\n", id, err)
	}
}

// handleDocumentThumbnail sert la miniature PNG de la première page
// (jalon 22), générée au premier appel puis conservée en base. Elle ne
// change jamais pour un document donné : cache navigateur long.
func (s *Server) handleDocumentThumbnail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	png, ok, err := s.Jobs.Thumbnail(r.Context(), id)
	if err != nil {
		http.Error(w, "miniature indisponible : "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(png)
}

// handleDocumentDelete supprime définitivement un document (jalon 18).
// Répond avec HX-Redirect plutôt qu'un fragment : que l'appel vienne de
// la liste ou de la page de détail, le navigateur est renvoyé vers
// /documents, qui reflète alors l'état à jour — plus simple et plus
// robuste qu'essayer de retirer une ligne/naviguer différemment selon
// l'origine de l'appel.
func (s *Server) handleDocumentDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.Jobs.Delete(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("HX-Redirect", "/documents")
	w.WriteHeader(http.StatusOK)
}

// splitTags découpe une liste de tags séparés par des virgules, en
// ignorant les entrées vides (espaces superflus, virgules successives).
func splitTags(raw string) []string {
	var tags []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			tags = append(tags, part)
		}
	}
	return tags
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
		out = append(out, templates.PackageSummary{Path: pkg.Path, Label: s.shortPath(pkg.Path), Types: types})
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
	return s.typeDetailView(t), true
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

// handleModel affiche le diagramme navigable du modèle de données (jalon
// 24) centré sur un type : ?pkg=&name= (liens du diagramme) ou ?t=pkg#Nom
// (sélecteur), sinon le type le plus connecté. ?depth= (1-3) et
// ?impl=1 (relations "implémente") règlent l'étendue.
func (s *Server) handleModel(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pkg, name := q.Get("pkg"), q.Get("name")
	if t := q.Get("t"); t != "" {
		pkg, name, _ = strings.Cut(t, "#")
	}
	opts := diagram.Options{Depth: 1, Implements: q.Get("impl") == "1"}
	if d, err := strconv.Atoi(q.Get("depth")); err == nil && d >= 1 && d <= 3 {
		opts.Depth = d
	}

	s.mu.RLock()
	focus := codemap.TypeRef{Package: pkg, Name: name}
	if pkg == "" || name == "" {
		focus, _ = s.defaultModelFocus()
	}
	view, ok := s.modelPageView(focus, opts)
	s.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ModelPage(view).Render(r.Context(), w); err != nil {
		fmt.Fprintf(os.Stderr, "jarvisapp: render model: %v\n", err)
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
			row := templates.TestRowView{Name: tf.Name, File: tf.File, Line: tf.Line, Integration: tf.Integration, Code: s.testSourceView(tf)}
			if res, ok := s.results[resultKey(cat.Package, tf.Name)]; ok {
				row.Status = string(res.Status)
				row.Elapsed = res.Elapsed
				row.Output = res.Output
			}
			rows = append(rows, row)
		}
		out = append(out, templates.TestCategoryView{Package: cat.Package, Label: s.shortPath(cat.Package), Tests: rows})
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
