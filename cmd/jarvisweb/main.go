// Command jarvisweb expose le pipeline jarvis (triage -> parsing ->
// extraction) derrière une interface web : upload d'un document,
// traitement asynchrone, résultat affiché dès qu'il est prêt. Pensé pour
// tourner en local aujourd'hui et être hébergé plus tard (net/http, pas
// de dépendance à l'environnement local au-delà des serveurs VLM/LLM
// configurés en flags).
package main

import (
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
	llmTimeout := flag.Duration("llm-timeout", 120*time.Second, "Timeout par appel LLM")
	uploadDir := flag.String("upload-dir", "", "Répertoire de stockage des documents uploadés (vide = répertoire temporaire du système)")
	outDir := flag.String("out-dir", "", "Répertoire de persistance des résultats (JSON par page + log de rejeu) ; vide = pas de persistance")
	flag.Parse()

	if *vlmURL == "" || *vlmModel == "" {
		log.Fatal("jarvisweb: --vlm-url et --vlm-model sont requis")
	}
	if *llmURL == "" || *llmModel == "" {
		log.Fatal("jarvisweb: --llm-url et --llm-model sont requis")
	}

	dir := *uploadDir
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "jarvisweb-uploads-*")
		if err != nil {
			log.Fatalf("jarvisweb: create upload dir: %v", err)
		}
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatalf("jarvisweb: create upload dir %s: %v", dir, err)
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
		},
	}

	jobs := webapp.NewJobManager(runner, registry)
	if *outDir != "" {
		jobs.OnFinish = persistJob(*outDir)
	}

	srv := &Server{Jobs: jobs, Registry: registry, UploadDir: dir}

	staticContent, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("jarvisweb: static assets: %v", err)
	}

	mux := srv.Routes()
	mux.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticContent))))

	log.Printf("jarvisweb: listening on http://%s (doc types: %v)", *addr, registry.Names())
	log.Printf("jarvisweb: uploads -> %s", dir)
	if *outDir != "" {
		log.Printf("jarvisweb: résultats persistés -> %s", *outDir)
	}
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("jarvisweb: %v", err)
	}
}

// persistJob branche la persistance sur disque (internal/store), la même
// que celle utilisée par `jarvis process --out-dir`, sur la fin de
// traitement d'un job web.
func persistJob(outDir string) func(webapp.Job) {
	return func(job webapp.Job) {
		if job.Status != webapp.StatusDone || job.Result == nil {
			return
		}
		hash, err := store.HashFile(job.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "jarvisweb: hash %s: %v\n", job.Path, err)
			return
		}
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
