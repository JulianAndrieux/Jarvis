package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/projectinfo"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

// Page Architecture (jalon 29) : choix techniques, jalons, commits et
// infrastructure, relus à chaque demande — dépôt git et CLAUDE.md sont la
// source, rien n'est recopié. La page sonde une empreinte et se recharge
// quand elle change ; le schéma d'infrastructure se sonde lui-même.

const (
	archCommitLimit = 500
	infraTimeout    = 3 * time.Second
)

func (s *Server) architectureRoutes(r chi.Router) {
	r.Get("/admin/architecture", s.handleArchitecture)
	r.Get("/admin/architecture/version", s.handleArchitectureVersion)
	r.Get("/admin/architecture/infra", s.handleArchitectureInfra)
	r.Get("/admin/architecture/commits/{hash}", s.handleArchitectureCommit)
}

func (s *Server) handleArchitecture(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	view := templates.ArchitectureView{}
	var err error
	if view.Fingerprint, err = projectinfo.Fingerprint(ctx, s.ModuleDir); err != nil {
		view.Errors = append(view.Errors, err.Error())
	}
	if src, err := os.ReadFile(filepath.Join(s.ModuleDir, "CLAUDE.md")); err != nil {
		view.Errors = append(view.Errors, "CLAUDE.md : "+err.Error())
	} else {
		view.Doc = projectinfo.ParseProjectDoc(string(src))
		if info, err := os.Stat(filepath.Join(s.ModuleDir, "CLAUDE.md")); err == nil {
			view.DocModified = info.ModTime()
		}
	}
	if view.Commits, err = projectinfo.Commits(ctx, s.ModuleDir, archCommitLimit); err != nil {
		view.Errors = append(view.Errors, err.Error())
	}
	if view.Changes, err = projectinfo.WorkingChanges(ctx, s.ModuleDir); err != nil {
		view.Errors = append(view.Errors, err.Error())
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ArchitecturePage(view).Render(ctx, w); err != nil {
		fmt.Fprintf(os.Stderr, "jarvisapp: render architecture: %v\n", err)
	}
}

// handleArchitectureVersion : 204 si rien n'a changé depuis l'empreinte v,
// sinon demande à HTMX de recharger la page.
func (s *Server) handleArchitectureVersion(w http.ResponseWriter, r *http.Request) {
	current, err := projectinfo.Fingerprint(r.Context(), s.ModuleDir)
	if err != nil || current == r.URL.Query().Get("v") {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleArchitectureInfra(w http.ResponseWriter, r *http.Request) {
	probed := s.Infra.Probe(r.Context(), infraTimeout)
	view := templates.InfraView{Diagram: probed, SVG: projectinfo.RenderSVG(probed), CheckedAt: time.Now()}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.InfraPanel(view).Render(r.Context(), w); err != nil {
		fmt.Fprintf(os.Stderr, "jarvisapp: render infra: %v\n", err)
	}
}

func (s *Server) handleArchitectureCommit(w http.ResponseWriter, r *http.Request) {
	_, diff, err := projectinfo.Show(r.Context(), s.ModuleDir, chi.URLParam(r, "hash"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.CommitDiff(diff).Render(r.Context(), w); err != nil {
		fmt.Fprintf(os.Stderr, "jarvisapp: render commit: %v\n", err)
	}
}

// InfraConfig : ce que main sait des composants à dessiner et sonder.
type InfraConfig struct {
	Addr                       string
	VLMURL, VLMModel           string
	LLMURL, LLMModel           string
	AgentURL, AgentModel       string
	MongoDB                    string
	JobsCollection, TicketsCol string
	WatchDir, OutDir           string
	WorktreesDir               string
	AgentDev                   bool
}

// buildInfra décrit l'infrastructure réelle : composants, rôle, sonde et
// flux. Grille : entrées en haut, application, traitements, modèles.
func (s *Server) buildInfra(cfg InfraConfig) projectinfo.Diagram {
	client := &http.Client{Timeout: infraTimeout}
	started := time.Now()
	nodes := []projectinfo.Node{
		{ID: "browser", Title: "Navigateur", Role: "interface web (HTMX)", Col: 0, Row: 0},
		{ID: "watch", Title: "Dossier surveillé", Role: "ingestion automatique", Col: 1, Row: 0, Check: projectinfo.DirCheck(cfg.WatchDir)},
		{ID: "launcher", Title: "Lanceur (Jarvis.app)", Role: "démarre VLM, LLM et l'application", Col: 2, Row: 0},

		{ID: "local", Title: "Copie locale", Role: "JSON par page (--out-dir)", Col: 0, Row: 1, Check: projectinfo.DirCheck(cfg.OutDir)},
		{ID: "app", Title: "jarvisapp " + cfg.Addr, Role: "Go · net/http · chi · templ", Col: 1, Row: 1,
			Check: projectinfo.FuncCheck(func(context.Context) (string, error) {
				return "en service depuis " + started.Format("15:04"), nil
			})},
		{ID: "mongo", Title: "MongoDB Atlas", Role: cfg.MongoDB + " : " + cfg.JobsCollection + " · GridFS · " + cfg.TicketsCol, Col: 2, Row: 1,
			Check: projectinfo.FuncCheck(s.mongoStatus)},

		{ID: "convert", Title: "Conversions", Role: "LibreOffice · sips (→ PDF)", Col: 0, Row: 2, Check: projectinfo.BinaryCheck("soffice", "libreoffice")},
		{ID: "pipeline", Title: "Pipeline documents", Role: "triage → VLM → LLM", Col: 1, Row: 2, Check: projectinfo.FuncCheck(s.queueStatus)},
		{ID: "agent", Title: "Agent des tickets", Role: "analyse, développe, vérifie", Col: 2, Row: 2,
			Check: projectinfo.FuncCheck(func(context.Context) (string, error) {
				if s.Tickets == nil {
					return "", fmt.Errorf("désactivé")
				}
				if !cfg.AgentDev {
					return "analyse seule", nil
				}
				return "analyse + développement", nil
			})},
		{ID: "git", Title: "Dépôt git", Role: "branches ticket/* en copies isolées", Col: 3, Row: 2, Check: projectinfo.DirCheck(cfg.WorktreesDir)},

		{ID: "poppler", Title: "poppler", Role: "pdftotext · pdftoppm (triage, rendu)", Col: 0, Row: 3, Check: projectinfo.BinaryCheck("pdftoppm")},
		{ID: "vlm", Title: "VLM " + cfg.VLMModel, Role: "llama.cpp · lecture des scans", Col: 1, Row: 3, Check: projectinfo.LlamaCheck(client, cfg.VLMURL)},
		{ID: "llm", Title: "LLM " + cfg.LLMModel, Role: "llama.cpp · classification, extraction", Col: 2, Row: 3, Check: projectinfo.LlamaCheck(client, cfg.LLMURL)},
	}
	edges := []projectinfo.Edge{
		{From: "browser", To: "app", Label: "HTTP"},
		{From: "watch", To: "app", Label: "fichiers"},
		{From: "launcher", To: "app", Label: "démarre"},
		{From: "app", To: "local"},
		{From: "app", To: "mongo", Label: "jobs, fichiers"},
		{From: "app", To: "convert", Label: "Office, images"},
		{From: "app", To: "pipeline", Label: "PDF"},
		{From: "app", To: "agent", Label: "tickets"},
		{From: "agent", To: "git", Label: "diff"},
		{From: "pipeline", To: "poppler"},
		{From: "pipeline", To: "vlm", Label: "pages scannées"},
		{From: "pipeline", To: "llm", Label: "texte → JSON"},
	}
	// Agent servi par un autre modèle (la machine dédiée) : son propre nœud.
	if cfg.AgentURL != "" && cfg.AgentURL != cfg.LLMURL {
		nodes = append(nodes, projectinfo.Node{ID: "agentllm", Title: "Modèle agent " + cfg.AgentModel, Role: "appels d'outils", Col: 3, Row: 3, Check: projectinfo.LlamaCheck(client, cfg.AgentURL)})
		edges = append(edges, projectinfo.Edge{From: "agent", To: "agentllm", Label: "outils"})
	} else {
		edges = append(edges, projectinfo.Edge{From: "agent", To: "llm", Label: "outils"})
	}
	return projectinfo.Diagram{Nodes: nodes, Edges: edges}
}

// mongoStatus : une vraie requête sur Atlas, avec sa latence.
func (s *Server) mongoStatus(ctx context.Context) (string, error) {
	start := time.Now()
	if _, err := s.Jobs.List(ctx, webapp.ListQuery{SummaryOnly: true, Limit: 1}); err != nil {
		return "", err
	}
	return fmt.Sprintf("joignable · %d ms", time.Since(start).Milliseconds()), nil
}

// queueStatus : documents en cours et en attente.
func (s *Server) queueStatus(ctx context.Context) (string, error) {
	running, err := s.Jobs.List(ctx, webapp.ListQuery{Status: webapp.StatusRunning, SummaryOnly: true})
	if err != nil {
		return "", err
	}
	pending, err := s.Jobs.List(ctx, webapp.ListQuery{Status: webapp.StatusPending, SummaryOnly: true})
	if err != nil {
		return "", err
	}
	if len(running)+len(pending) == 0 {
		return "au repos", nil
	}
	return fmt.Sprintf("%d en cours · %d en attente", len(running), len(pending)), nil
}
