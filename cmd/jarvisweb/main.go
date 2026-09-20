// Command jarvisweb expose le pipeline jarvis (triage -> parsing ->
// extraction) derrière une interface web : upload d'un document,
// traitement asynchrone, résultat affiché dès qu'il est prêt.
//
// Les jobs (documents uploadés, statut, résultat) sont persistés dans
// MongoDB (Atlas en production) — voir CLAUDE.md pour la portée de
// l'exception explicite à "aucune donnée ne sort de la machine" que ça
// représente. L'inférence (VLM, LLM), elle, continue de tourner en local
// via les serveurs pointés par --vlm-url/--llm-url.
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/bbox"
	"github.com/JulianAndrieux/Jarvis/internal/doctype"
	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/parsing"
	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
	"github.com/JulianAndrieux/Jarvis/internal/store"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
	"github.com/JulianAndrieux/Jarvis/internal/vlm"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

//go:embed static
var staticFS embed.FS

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "Adresse d'écoute HTTP")
	vlmURL := flag.String("vlm-url", "", "URL de base du serveur VLM compatible OpenAI")
	vlmModel := flag.String("vlm-model", "", "Identifiant du modèle VLM servi")
	vlmVersion := flag.String("vlm-model-version", "", "Version/quantization du modèle VLM")
	llmURL := flag.String("llm-url", "", "URL de base du serveur LLM compatible OpenAI")
	llmModel := flag.String("llm-model", "", "Identifiant du modèle LLM servi")
	llmVersion := flag.String("llm-model-version", "", "Version/quantization du modèle LLM")
	dpi := flag.Int("dpi", 200, "Résolution de rendu des pages (DPI)")
	vlmTimeout := flag.Duration("vlm-timeout", 120*time.Second, "Timeout par appel VLM")
	llmTimeout := flag.Duration("llm-timeout", 180*time.Second, "Timeout par appel LLM")
	workDir := flag.String("work-dir", "", "Répertoire des fichiers temporaires de traitement (vide = répertoire temporaire du système)")
	outDir := flag.String("out-dir", "", "Répertoire de persistance locale additionnelle des résultats (JSON par page + log de rejeu) ; vide = pas de copie locale")
	mongoURI := flag.String("mongo-uri", "", "URI de connexion MongoDB (ex: Atlas) — stocke les jobs (document source, statut, résultat)")
	mongoDB := flag.String("mongo-db", "jarvis", "Base MongoDB")
	mongoCollection := flag.String("mongo-collection", "jobs", "Collection MongoDB pour les jobs")
	flag.Parse()

	if *vlmURL == "" || *vlmModel == "" {
		log.Fatal("jarvisweb: --vlm-url et --vlm-model sont requis")
	}
	if *llmURL == "" || *llmModel == "" {
		log.Fatal("jarvisweb: --llm-url et --llm-model sont requis")
	}
	if *mongoURI == "" {
		log.Fatal("jarvisweb: --mongo-uri est requis (les jobs sont persistés dans MongoDB)")
	}

	registry := doctype.NewDefaultRegistry()

	runner := pipeline.Pipeline{
		TextExtractor: triage.PdftotextExtractor{},
		Renderer:      parsing.PdftoppmRenderer{},
		BBox:          bbox.PdftotextBBoxExtractor{},
		VLM: vlm.HTTPClient{
			BaseURL:      *vlmURL,
			Model:        *vlmModel,
			ModelVersion: *vlmVersion,
			HTTP:         &http.Client{Timeout: *vlmTimeout},
		},
		DPI: *dpi,
		LLM: llm.HTTPClient{
			BaseURL:      *llmURL,
			Model:        *llmModel,
			ModelVersion: *llmVersion,
			HTTP:         &http.Client{Timeout: *llmTimeout},
			// Voir cmd/jarvis/process.go et CLAUDE.md (jalon 10 finding 4
			// / jalon 11) : Qwen3 peut sinon générer un nombre de tokens
			// très variable sur du contenu ambigu.
			DisableThinking: true,
		},
	}

	connectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	jobStore, err := webapp.NewMongoStore(connectCtx, *mongoURI, *mongoDB, *mongoCollection)
	if err != nil {
		log.Fatalf("jarvisweb: %v", err)
	}

	jobs := webapp.NewJobManager(jobStore, runner, registry)
	jobs.WorkDir = *workDir
	if *outDir != "" {
		jobs.OnFinish = persistJobLocally(*outDir)
	}

	srv := &Server{Jobs: jobs, Registry: registry}

	staticContent, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("jarvisweb: static assets: %v", err)
	}

	mux := srv.Routes()
	mux.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticContent))))

	log.Printf("jarvisweb: listening on http://%s (doc types: %v)", *addr, registry.Names())
	log.Printf("jarvisweb: jobs -> mongodb %s/%s", *mongoDB, *mongoCollection)
	if *outDir != "" {
		log.Printf("jarvisweb: copie locale des résultats -> %s", *outDir)
	}
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("jarvisweb: %v", err)
	}
}

// persistJobLocally branche une copie locale additionnelle (internal/store,
// la même que `jarvis process --out-dir`) sur la fin de traitement d'un
// job web — utile pour inspecter/déboguer sans requêter MongoDB, ou comme
// filet de secours local. MongoDB (jobStore) reste la source de vérité.
func persistJobLocally(outDir string) func(webapp.Job) {
	return func(job webapp.Job) {
		if job.Status != webapp.StatusDone || job.Result == nil {
			return
		}
		hash := store.HashBytes(job.Content)
		doc, pages := store.BuildRecords(hash, job.Filename, job.DocType, time.Now().UTC(), *job.Result)
		if err := store.WriteRecords(outDir, doc, pages); err != nil {
			fmt.Fprintf(os.Stderr, "jarvisweb: write records for %s: %v\n", job.Filename, err)
			return
		}
		entry := store.RunLogEntry{
			Timestamp:  doc.ProcessedAt,
			SourceHash: hash,
			SourcePath: job.Filename,
			DocType:    job.DocType,
			PagesTotal: len(pages),
		}
		for _, p := range pages {
			if p.Extraction != nil && p.Extraction.NeedsReview {
				entry.PagesNeedingReview++
			}
			if (p.Parsing != nil && p.Parsing.Failed) || (p.Extraction != nil && p.Extraction.Failed) {
				entry.PagesFailed++
			}
		}
		if err := store.AppendRunLog(outDir, hash, entry); err != nil {
			fmt.Fprintf(os.Stderr, "jarvisweb: append run log for %s: %v\n", job.Filename, err)
		}
	}
}
