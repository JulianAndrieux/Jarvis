package main

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisweb/templates"
	"github.com/JulianAndrieux/Jarvis/internal/doctype"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

// maxUploadSize borne la taille d'un document accepté (64 Mio) pour éviter
// qu'un upload ne remplisse le disque ou la mémoire du formulaire.
const maxUploadSize = 64 << 20

// Server expose le pipeline jarvis sur HTTP : formulaire d'upload, suivi
// asynchrone d'un job, résultat une fois prêt.
type Server struct {
	Jobs      *webapp.JobManager
	Registry  *doctype.Registry
	UploadDir string
}

// Routes construit le routeur chi. Séparé de main() pour être testable
// via httptest sans démarrer de vrai serveur réseau.
func (s *Server) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", s.handleIndex)
	r.Post("/jobs", s.handleSubmit)
	r.Get("/jobs/{id}", s.handleJobStatus)
	return r
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.Index(s.Registry.Names()).Render(r.Context(), w); err != nil {
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

	// os.CreateTemp garantit un nom de fichier unique même en cas
	// d'uploads concurrents portant le même nom d'origine.
	dest, err := os.CreateTemp(s.UploadDir, "upload-*.pdf")
	if err != nil {
		http.Error(w, "impossible d'enregistrer le fichier : "+err.Error(), http.StatusInternalServerError)
		return
	}
	destPath := dest.Name()
	if _, err := io.Copy(dest, file); err != nil {
		dest.Close()
		os.Remove(destPath)
		http.Error(w, "impossible d'enregistrer le fichier : "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := dest.Close(); err != nil {
		os.Remove(destPath)
		http.Error(w, "impossible d'enregistrer le fichier : "+err.Error(), http.StatusInternalServerError)
		return
	}

	job, err := s.Jobs.Submit(docType, header.Filename, destPath)
	if err != nil {
		os.Remove(destPath)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.renderJob(w, r, job)
}

func (s *Server) handleJobStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, ok := s.Jobs.Get(id)
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
		fmt.Fprintf(os.Stderr, "jarvisweb: render job %s: %v\n", job.ID, err)
	}
}
