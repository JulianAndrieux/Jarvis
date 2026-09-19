package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/doctype"
	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/parsing"
	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
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
	llmTimeout := fs.Duration("llm-timeout", 120*time.Second, "Timeout par appel LLM")
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
		ConfidenceThreshold: *confidenceThreshold,
	}

	result, err := p.Run(ctx, reg, path)
	if err != nil {
		return fmt.Errorf("process %s: %w", path, err)
	}

	return json.NewEncoder(stdout).Encode(toProcessOutput(*docType, result))
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
	}
}
