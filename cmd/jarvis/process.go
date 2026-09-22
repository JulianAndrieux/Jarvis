package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/bbox"
	"github.com/JulianAndrieux/Jarvis/internal/doctype"
	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/parsing"
	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
	"github.com/JulianAndrieux/Jarvis/internal/store"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
	"github.com/JulianAndrieux/Jarvis/internal/vlm"
)

// processOutput est le format JSON stable exposé par `jarvis process` :
// le pipeline complet, triage -> parsing -> extraction.
type processOutput struct {
	Path       string                 `json:"path"`
	DocType    string                 `json:"doc_type"`
	Triage     processTriageOutput    `json:"triage"`
	Parsing    []parsePageOutput      `json:"parsing"`
	Extraction []extractionPageOutput `json:"extraction"`
	// Merged fusionne Extraction en un enregistrement par document
	// (meilleure confiance par champ à travers les pages) — voir
	// extraction.MergePages et CLAUDE.md, jalon 11.
	Merged mergedExtractionOutput `json:"merged"`
}

type processTriageOutput struct {
	Score        float64 `json:"score"`
	HasTextLayer bool    `json:"has_text_layer"`
}

type extractionPageOutput struct {
	Page                int             `json:"page"`
	JSON                json.RawMessage `json:"json,omitempty"`
	Model               string          `json:"model"`
	ModelVersion        string          `json:"model_version"`
	Prompt              string          `json:"prompt"`
	NeedsReview         bool            `json:"needs_review"`
	LowConfidenceFields []string        `json:"low_confidence_fields,omitempty"`
	Failed              bool            `json:"failed"`
	Error               string          `json:"error,omitempty"`
}

type mergedExtractionOutput struct {
	JSON                json.RawMessage `json:"json,omitempty"`
	Model               string          `json:"model"`
	ModelVersion        string          `json:"model_version"`
	Prompt              string          `json:"prompt"`
	NeedsReview         bool            `json:"needs_review"`
	LowConfidenceFields []string        `json:"low_confidence_fields,omitempty"`
	Failed              bool            `json:"failed"`
	Error               string          `json:"error,omitempty"`
}

func runProcess(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("process", flag.ContinueOnError)
	vlmURL := fs.String("vlm-url", "", "URL de base du serveur VLM compatible OpenAI")
	vlmModel := fs.String("vlm-model", "", "Identifiant du modèle VLM servi")
	vlmVersion := fs.String("vlm-model-version", "", "Version/quantization du modèle VLM")
	llmURL := fs.String("llm-url", "", "URL de base du serveur LLM compatible OpenAI")
	llmModel := fs.String("llm-model", "", "Identifiant du modèle LLM servi")
	llmVersion := fs.String("llm-model-version", "", "Version/quantization du modèle LLM")
	docType := fs.String("doc-type", "", "Type de document enregistré (ex: facture)")
	dpi := fs.Int("dpi", 200, "Résolution de rendu des pages (DPI)")
	confidenceThreshold := fs.Float64("confidence-threshold", 0, "Seuil de confiance par champ (0 = défaut d'extraction.DefaultConfidenceThreshold)")
	vlmTimeout := fs.Duration("vlm-timeout", 120*time.Second, "Timeout par appel VLM")
	llmTimeout := fs.Duration("llm-timeout", 180*time.Second, "Timeout par appel LLM")
	outDir := fs.String("out-dir", "", "Répertoire où persister les résultats (JSON par page + log de rejeu) ; vide = pas de persistance, stdout uniquement")
	concurrency := fs.Int("concurrency", 4, "Nombre de pages traitées en parallèle (VLM et extraction) ; 1 = séquentiel. À aligner sur les slots parallèles du serveur llama.cpp")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return fmt.Errorf("usage: jarvis process --vlm-url URL --vlm-model NAME --llm-url URL --llm-model NAME --doc-type NAME <fichier.pdf>")
	}
	path := fs.Arg(0)

	// Comme pour `jarvis parse` : pas de défaut silencieux sur les URLs,
	// noms de modèle, ou type de document — un défaut caché produirait
	// soit une erreur de connexion confuse, soit une provenance trompeuse.
	if *vlmURL == "" {
		return fmt.Errorf("--vlm-url est requis (URL du serveur VLM compatible OpenAI)")
	}
	if *vlmModel == "" {
		return fmt.Errorf("--vlm-model est requis (identifiant du modèle VLM, journalisé pour la provenance)")
	}
	if *llmURL == "" {
		return fmt.Errorf("--llm-url est requis (URL du serveur LLM compatible OpenAI)")
	}
	if *llmModel == "" {
		return fmt.Errorf("--llm-model est requis (identifiant du modèle LLM, journalisé pour la provenance)")
	}
	if *docType == "" {
		return fmt.Errorf("--doc-type est requis (un des types enregistrés, ex: facture)")
	}

	registry := doctype.NewDefaultRegistry()
	reg, ok := registry.Get(*docType)
	if !ok {
		return fmt.Errorf("--doc-type %q n'est pas enregistré (types disponibles : %v)", *docType, registry.Names())
	}

	p := pipeline.Pipeline{
		TextExtractor: triage.PdftotextExtractor{},
		Renderer:      parsing.PdftoppmRenderer{},
		// Enrichissement bbox best-effort sur les pages à texte natif
		// (cf. internal/pipeline/bbox.go) : un échec ne fait jamais
		// échouer le pipeline, le JSON reste valide sans "bbox".
		BBox: bbox.PdftotextBBoxExtractor{},
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
			// Qwen3 (le modèle d'extraction actuel) peut sinon générer un
			// nombre de tokens très variable avant de conclure sur du
			// contenu ambigu — voir CLAUDE.md, jalon 10 finding 4 / jalon
			// 11. Ce client HTTP reste générique (le champ existe, off par
			// défaut) ; c'est un choix d'application propre à ce modèle.
			DisableThinking: true,
		},
		ConfidenceThreshold: *confidenceThreshold,
		Concurrency:         *concurrency,
	}

	result, err := p.Run(ctx, reg, path)
	if err != nil {
		return fmt.Errorf("process %s: %w", path, err)
	}

	if *outDir != "" {
		if err := persistResult(*outDir, path, *docType, result); err != nil {
			return fmt.Errorf("process %s: %w", path, err)
		}
	}

	return json.NewEncoder(stdout).Encode(toProcessOutput(*docType, result))
}

// persistResult calcule le hash du document source et écrit les
// enregistrements (document + pages) ainsi qu'une entrée de log de rejeu
// sous outDir. N'est appelé qu'après un pipeline.Run réussi : un échec de
// niveau document n'a pas de résultat cohérent à persister.
func persistResult(outDir, path, docType string, result pipeline.Result) error {
	hash, err := store.HashFile(path)
	if err != nil {
		return fmt.Errorf("persist: %w", err)
	}

	doc, pages := store.BuildRecords(hash, path, docType, time.Now().UTC(), result)
	if err := store.WriteRecords(outDir, doc, pages); err != nil {
		return fmt.Errorf("persist: %w", err)
	}

	entry := store.RunLogEntry{
		Timestamp:  doc.ProcessedAt,
		SourceHash: hash,
		SourcePath: path,
		DocType:    docType,
		PagesTotal: len(pages),
	}
	for _, p := range pages {
		if p.Extraction != nil && p.Extraction.NeedsReview {
			entry.PagesNeedingReview++
		}
		pageFailed := (p.Parsing != nil && p.Parsing.Failed) ||
			(p.Extraction != nil && p.Extraction.Failed) ||
			(p.Source == "" && p.Extraction == nil)
		if pageFailed {
			entry.PagesFailed++
		}
	}

	if err := store.AppendRunLog(outDir, hash, entry); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	return nil
}

func toProcessOutput(docType string, r pipeline.Result) processOutput {
	extractionPages := make([]extractionPageOutput, len(r.Extraction))
	for i, e := range r.Extraction {
		extractionPages[i] = extractionPageOutput{
			Page:                e.Page,
			JSON:                e.JSON,
			Model:               e.Model.Name,
			ModelVersion:        e.Model.Version,
			Prompt:              e.Prompt,
			NeedsReview:         e.NeedsReview,
			LowConfidenceFields: e.LowConfidenceFields,
			Failed:              e.Failed,
			Error:               e.Error,
		}
	}

	return processOutput{
		Path:    r.Path,
		DocType: docType,
		Triage: processTriageOutput{
			Score:        r.Triage.Score,
			HasTextLayer: r.Triage.HasTextLayer,
		},
		Parsing:    toParsePageOutputs(r.Parsing),
		Extraction: extractionPages,
		Merged: mergedExtractionOutput{
			JSON:                r.Merged.JSON,
			Model:               r.Merged.Model.Name,
			ModelVersion:        r.Merged.Model.Version,
			Prompt:              r.Merged.Prompt,
			NeedsReview:         r.Merged.NeedsReview,
			LowConfidenceFields: r.Merged.LowConfidenceFields,
			Failed:              r.Merged.Failed,
			Error:               r.Merged.Error,
		},
	}
}
