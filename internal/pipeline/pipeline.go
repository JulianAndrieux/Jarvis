package pipeline

import (
	"context"
	"fmt"

	"github.com/JulianAndrieux/Jarvis/internal/doctype"
	"github.com/JulianAndrieux/Jarvis/internal/extraction"
	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/parsing"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
	"github.com/JulianAndrieux/Jarvis/internal/vlm"
)

// Pipeline enchaîne Triage, Parsing et Extraction pour un document. Tous
// les ports sont injectés (fakes en test, implémentations HTTP réelles en
// production) — Pipeline ne fait qu'orchestrer, comme chaque étage pris
// individuellement.
type Pipeline struct {
	TextExtractor triage.TextExtractor
	Thresholds    triage.Thresholds // valeur zéro -> triage.DefaultThresholds()

	Renderer parsing.Renderer
	VLM      vlm.Client
	DPI      int // valeur zéro -> défaut de parsing.Parser (200)

	LLM                 llm.Client
	ConfidenceThreshold float64 // valeur zéro -> extraction.DefaultConfidenceThreshold
}

// Result rassemble les résultats des trois étages pour un document, pour
// inspection complète (logs, sortie CLI, stockage).
type Result struct {
	Path       string
	Triage     triage.Result
	Parsing    []parsing.PageResult
	Extraction []extraction.Result
}

// Run exécute le pipeline complet sur path, pour le type de document reg.
func (p Pipeline) Run(ctx context.Context, reg doctype.Registration, path string) (Result, error) {
	nativePages, err := p.TextExtractor.ExtractPerPage(ctx, path)
	if err != nil {
		return Result{}, fmt.Errorf("pipeline: triage extract %s: %w", path, err)
	}

	thresholds := p.Thresholds
	var zeroThresholds triage.Thresholds
	if thresholds == zeroThresholds {
		thresholds = triage.DefaultThresholds()
	}
	triageResult := triage.Score(nativePages, thresholds)

	pagesToParse := parsing.PagesNeedingParsing(triageResult)
	parser := parsing.Parser{Renderer: p.Renderer, VLM: p.VLM, DPI: p.DPI}
	parseResults, err := parser.ParsePages(ctx, path, pagesToParse)
	if err != nil {
		return Result{Path: path, Triage: triageResult}, fmt.Errorf("pipeline: parsing %s: %w", path, err)
	}

	merged := Merge(nativePages, triageResult, parseResults)
	pageTexts := make([]triage.PageText, len(merged))
	for i, m := range merged {
		pageTexts[i] = triage.PageText{Page: m.Page, Text: m.Text}
	}

	extractor := extraction.Extractor{LLM: p.LLM, ConfidenceThreshold: p.ConfidenceThreshold}
	extractionResults, err := extractor.ExtractPages(ctx, reg, pageTexts)
	if err != nil {
		return Result{Path: path, Triage: triageResult, Parsing: parseResults}, fmt.Errorf("pipeline: extraction %s: %w", path, err)
	}

	return Result{
		Path:       path,
		Triage:     triageResult,
		Parsing:    parseResults,
		Extraction: extractionResults,
	}, nil
}
