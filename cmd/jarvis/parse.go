package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/parsing"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
	"github.com/JulianAndrieux/Jarvis/internal/vlm"
)

// parseOutput est le format JSON stable exposé par `jarvis parse`.
type parseOutput struct {
	Path         string            `json:"path"`
	TriageScore  float64           `json:"triage_score"`
	HasTextLayer bool              `json:"has_text_layer"`
	Pages        []parsePageOutput `json:"pages"`
}

type parsePageOutput struct {
	Page         int    `json:"page"`
	Markdown     string `json:"markdown"`
	Model        string `json:"model"`
	ModelVersion string `json:"model_version"`
	Prompt       string `json:"prompt"`
	Failed       bool   `json:"failed"`
	Error        string `json:"error,omitempty"`
}

func runParse(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("parse", flag.ContinueOnError)
	vlmURL := fs.String("vlm-url", "", "URL de base du serveur VLM compatible OpenAI (ex: http://localhost:8080/v1)")
	vlmModel := fs.String("vlm-model", "", "Identifiant du modèle servi (journalisé pour la reproductibilité)")
	vlmVersion := fs.String("vlm-model-version", "", "Version/quantization du modèle (journalisé pour la reproductibilité)")
	dpi := fs.Int("dpi", 200, "Résolution de rendu des pages (DPI)")
	timeout := fs.Duration("vlm-timeout", 120*time.Second, "Timeout par appel VLM")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return fmt.Errorf("usage: jarvis parse --vlm-url URL --vlm-model NAME <fichier.pdf>")
	}
	path := fs.Arg(0)

	// --vlm-url et --vlm-model sont volontairement sans valeur par défaut
	// silencieuse : deviner une URL de serveur ou un nom de modèle
	// produirait soit un échec de connexion confus, soit — pire — une
	// provenance trompeuse dans les résultats.
	if *vlmURL == "" {
		return fmt.Errorf("--vlm-url est requis (URL du serveur VLM compatible OpenAI, ex: llama.cpp server)")
	}
	if *vlmModel == "" {
		return fmt.Errorf("--vlm-model est requis (identifiant du modèle, journalisé pour la provenance)")
	}

	detector := triage.Detector{
		Extractor:  triage.PdftotextExtractor{},
		Thresholds: triage.DefaultThresholds(),
	}
	triageResult, err := detector.Detect(ctx, path)
	if err != nil {
		return fmt.Errorf("parse %s: triage: %w", path, err)
	}

	pagesToParse := parsing.PagesNeedingParsing(triageResult)

	parser := parsing.Parser{
		Renderer: parsing.PdftoppmRenderer{},
		VLM: vlm.HTTPClient{
			BaseURL:      *vlmURL,
			Model:        *vlmModel,
			ModelVersion: *vlmVersion,
			HTTP:         &http.Client{Timeout: *timeout},
		},
		DPI: *dpi,
	}

	results, err := parser.ParsePages(ctx, path, pagesToParse)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	return json.NewEncoder(stdout).Encode(toParseOutput(path, triageResult, results))
}

func toParseOutput(path string, triageResult triage.Result, results []parsing.PageResult) parseOutput {
	pages := make([]parsePageOutput, len(results))
	for i, r := range results {
		pages[i] = parsePageOutput{
			Page:         r.Page,
			Markdown:     r.Markdown,
			Model:        r.Model.Name,
			ModelVersion: r.Model.Version,
			Prompt:       r.Prompt,
			Failed:       r.Failed,
			Error:        r.Error,
		}
	}
	return parseOutput{
		Path:         path,
		TriageScore:  triageResult.Score,
		HasTextLayer: triageResult.HasTextLayer,
		Pages:        pages,
	}
}
