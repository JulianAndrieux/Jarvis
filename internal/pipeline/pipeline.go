package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/JulianAndrieux/Jarvis/internal/bbox"
	"github.com/JulianAndrieux/Jarvis/internal/classify"
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

	// BBox est optionnel : nil désactive l'enrichissement bbox (pas
	// d'erreur, le JSON d'extraction reste tel quel). Ne s'applique qu'aux
	// pages dont le texte vient du triage (SourceNative) — les pages
	// passées par le VLM n'ont pas de mots positionnés disponibles
	// aujourd'hui (cf. CLAUDE.md).
	BBox bbox.Extractor

	// Classifier et Registry sont requis par RunAuto (classification
	// automatique du type de document avant extraction — cf. CLAUDE.md,
	// "Upload : classification automatique"). Run (type connu d'avance,
	// utilisé par la CLI) n'en a pas besoin.
	Classifier classify.Classifier
	Registry   *doctype.Registry
}

// Result rassemble les résultats des trois étages pour un document, pour
// inspection complète (logs, sortie CLI, stockage).
type Result struct {
	Path string
	// DocType est le type de document utilisé pour l'extraction — passé
	// explicitement (Run) ou déterminé par classification automatique
	// (RunAuto, vide si aucun type n'a été identifié avec confiance).
	DocType string
	// ClassificationConfidence n'est renseigné que par RunAuto (0 sinon).
	ClassificationConfidence float64

	Triage     triage.Result
	Parsing    []parsing.PageResult
	Extraction []extraction.Result
	// Merged est la fusion de Extraction en un enregistrement par
	// document (meilleure confiance par champ à travers les pages) — voir
	// extraction.MergePages. Répond à la limite "un JSON par page" quand
	// les champs d'un document sont répartis sur plusieurs pages (cf.
	// CLAUDE.md, jalon 10 finding 3 / jalon 11). Extraction reste la
	// source de vérité par page ; Merged est une vue additionnelle.
	Merged extraction.MergedResult
}

// Run exécute le pipeline complet sur path, pour le type de document reg
// (connu d'avance — utilisé par la CLI, où l'utilisateur le précise).
// Voir RunAuto pour la classification automatique.
func (p Pipeline) Run(ctx context.Context, reg doctype.Registration, path string) (Result, error) {
	triageResult, parseResults, merged, pageTexts, err := p.prepare(ctx, path)
	if err != nil {
		return Result{Path: path, Triage: triageResult, Parsing: parseResults}, err
	}

	extractionResults, err := p.extractPages(ctx, reg, pageTexts)
	if err != nil {
		return Result{Path: path, DocType: reg.Name, Triage: triageResult, Parsing: parseResults}, err
	}

	if p.BBox != nil {
		extractionResults = p.attachBBoxes(ctx, path, merged, extractionResults)
	}

	return Result{
		Path:       path,
		DocType:    reg.Name,
		Triage:     triageResult,
		Parsing:    parseResults,
		Extraction: extractionResults,
		Merged:     extraction.MergePages(extractionResults, p.ConfidenceThreshold),
	}, nil
}

// RunAuto exécute Triage puis Parsing comme Run, mais détermine le type
// de document par classification automatique (p.Classifier, parmi les
// types de p.Registry) au lieu de le recevoir en paramètre — répond à
// "uploader un document et laisser le modèle trouver le type". Si aucun
// type ne correspond avec confiance, l'Extraction est simplement vide
// (Result.DocType == "") : ce n'est pas une erreur, juste l'absence
// d'extracteur applicable, cohérent avec "jamais de valeur inventée".
func (p Pipeline) RunAuto(ctx context.Context, path string) (Result, error) {
	if p.Classifier == nil || p.Registry == nil {
		return Result{}, fmt.Errorf("pipeline: RunAuto requires Classifier and Registry to be set")
	}

	triageResult, parseResults, merged, pageTexts, err := p.prepare(ctx, path)
	if err != nil {
		return Result{Path: path, Triage: triageResult, Parsing: parseResults}, err
	}

	candidates := candidatesFromRegistry(p.Registry)
	classification, err := p.Classifier.Classify(ctx, concatPageTexts(pageTexts), candidates)
	if err != nil {
		return Result{Path: path, Triage: triageResult, Parsing: parseResults}, fmt.Errorf("pipeline: classify %s: %w", path, err)
	}

	if classification.DocType == "" {
		return Result{
			Path: path, Triage: triageResult, Parsing: parseResults,
			ClassificationConfidence: classification.Confidence,
		}, nil
	}

	reg, ok := p.Registry.Get(classification.DocType)
	if !ok {
		// Défensif : internal/classify ne devrait renvoyer que des noms
		// connus (contrainte d'énumération + vérification), mais on ne
		// fait jamais confiance aveuglément à une décision externe.
		return Result{Path: path, Triage: triageResult, Parsing: parseResults}, fmt.Errorf("pipeline: classifier returned unknown doc type %q", classification.DocType)
	}

	extractionResults, err := p.extractPages(ctx, reg, pageTexts)
	if err != nil {
		return Result{Path: path, DocType: reg.Name, Triage: triageResult, Parsing: parseResults}, err
	}

	if p.BBox != nil {
		extractionResults = p.attachBBoxes(ctx, path, merged, extractionResults)
	}

	return Result{
		Path:                     path,
		DocType:                  reg.Name,
		ClassificationConfidence: classification.Confidence,
		Triage:                   triageResult,
		Parsing:                  parseResults,
		Extraction:               extractionResults,
		Merged:                   extraction.MergePages(extractionResults, p.ConfidenceThreshold),
	}, nil
}

// RunWithType exécute le pipeline pour path avec un type de document
// imposé explicitement (docType), sans classification — utilisé quand un
// type est réattribué manuellement à un document déjà traité
// (bibliothèque de documents, jalon 17). Requiert Registry (comme
// RunAuto) pour résoudre docType en Registration ; simple enveloppe
// autour de Run.
func (p Pipeline) RunWithType(ctx context.Context, docType, path string) (Result, error) {
	if p.Registry == nil {
		return Result{}, fmt.Errorf("pipeline: RunWithType requires Registry to be set")
	}
	reg, ok := p.Registry.Get(docType)
	if !ok {
		return Result{}, fmt.Errorf("pipeline: unknown doc type %q", docType)
	}
	return p.Run(ctx, reg, path)
}

// prepare exécute Triage puis Parsing — la partie commune à Run et
// RunAuto, indépendante du type de document.
func (p Pipeline) prepare(ctx context.Context, path string) (triageResult triage.Result, parseResults []parsing.PageResult, merged []PageContent, pageTexts []triage.PageText, err error) {
	nativePages, err := p.TextExtractor.ExtractPerPage(ctx, path)
	if err != nil {
		return triage.Result{}, nil, nil, nil, fmt.Errorf("pipeline: triage extract %s: %w", path, err)
	}

	thresholds := p.Thresholds
	var zeroThresholds triage.Thresholds
	if thresholds == zeroThresholds {
		thresholds = triage.DefaultThresholds()
	}
	triageResult = triage.Score(nativePages, thresholds)

	pagesToParse := parsing.PagesNeedingParsing(triageResult)
	parser := parsing.Parser{Renderer: p.Renderer, VLM: p.VLM, DPI: p.DPI}
	parseResults, err = parser.ParsePages(ctx, path, pagesToParse)
	if err != nil {
		return triageResult, nil, nil, nil, fmt.Errorf("pipeline: parsing %s: %w", path, err)
	}

	merged = Merge(nativePages, triageResult, parseResults)
	pageTexts = make([]triage.PageText, len(merged))
	for i, m := range merged {
		pageTexts[i] = triage.PageText{Page: m.Page, Text: m.Text}
	}
	return triageResult, parseResults, merged, pageTexts, nil
}

// extractPages appelle l'étage Extraction pour reg — factorisé entre Run
// et RunAuto.
func (p Pipeline) extractPages(ctx context.Context, reg doctype.Registration, pageTexts []triage.PageText) ([]extraction.Result, error) {
	extractor := extraction.Extractor{LLM: p.LLM, ConfidenceThreshold: p.ConfidenceThreshold}
	results, err := extractor.ExtractPages(ctx, reg, pageTexts)
	if err != nil {
		return nil, fmt.Errorf("pipeline: extraction %s: %w", reg.Name, err)
	}
	return results, nil
}

// candidatesFromRegistry projette les Registration du registre en
// classify.Candidate — classify ne dépend pas de doctype, cette
// conversion vit donc ici, au point de jonction des deux paquets.
func candidatesFromRegistry(registry *doctype.Registry) []classify.Candidate {
	regs := registry.Registrations()
	candidates := make([]classify.Candidate, len(regs))
	for i, r := range regs {
		candidates[i] = classify.Candidate{Name: r.Name, Description: r.Description}
	}
	return candidates
}

// concatPageTexts assemble le texte de toutes les pages (déjà triées par
// Merge) en une seule chaîne pour la classification — celle-ci n'a besoin
// que d'un signal sur l'ensemble du document, contrairement à
// l'Extraction qui reste par page.
func concatPageTexts(pageTexts []triage.PageText) string {
	parts := make([]string, len(pageTexts))
	for i, pt := range pageTexts {
		parts[i] = pt.Text
	}
	return strings.Join(parts, "\n\n")
}
