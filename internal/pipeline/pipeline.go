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

	// VLMConcurrency et LLMConcurrency bornent (séparément) le nombre de
	// pages traitées en parallèle pour le VLM (Parsing) et pour
	// l'extraction LLM — 0 ou 1 (par défaut) = séquentiel, comportement
	// inchangé par rapport aux jalons précédents.
	//
	// Jalon 21 puis correction (cf. CLAUDE.md) : un unique champ
	// Concurrency partagé entre les deux étages a d'abord été introduit,
	// validé uniquement sur un document 100% texte natif — donc sans
	// appel VLM réel en parallèle, et avec un contenu par page trop
	// court pour révéler quoi que ce soit côté LLM non plus. Le retest
	// sur de vrais documents scannés a montré deux problèmes distincts de
	// contention sous charge, sur ce matériel :
	//   - VLM (appels multimodaux, images) : au-delà de 1 en parallèle,
	//     le serveur a renvoyé des 500 ("failed to process mtmd chunk")
	//     et des timeouts en cascade sur les pages suivantes.
	//   - LLM (extraction texte) : sûr et ~30% plus rapide en parallèle
	//     sur du texte natif court, mais au-delà de 1 en parallèle sur du
	//     Markdown dense produit par le VLM (grand tableau de prix), le
	//     serveur a renvoyé "Context size has been exceeded" sur la
	//     plupart des pages — la même page extraite seule, sans
	//     concurrence, réussit sans erreur. Contention réelle, pas une
	//     limite de contexte par page.
	// D'où deux champs indépendants, tous deux à 1 par défaut (séquentiel)
	// tant que le comportement des deux serveurs sous charge n'est pas
	// mieux compris — un appelant qui connaît son profil de contenu (texte
	// natif court, jamais de VLM) peut relever LLMConcurrency en toute
	// connaissance de cause.
	VLMConcurrency int
	LLMConcurrency int
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
	// SearchText est le texte du document (natif ou Markdown VLM, toutes
	// pages confondues) — renseigné même si aucun type n'a été identifié
	// (RunAuto) ou si l'extraction échoue, dès que Triage+Parsing ont
	// abouti. Sert à "chercher dans les documents" (jalon 18), pas
	// seulement dans leurs métadonnées/champs extraits.
	SearchText string

	// Pages est le texte de chaque page tel qu'envoyé à l'étage
	// Extraction (texte natif ou Markdown VLM), avec sa provenance — jalon
	// 22, onglet "Texte OCR" de l'interface web. Renseigné dès que Triage+
	// Parsing ont abouti, comme SearchText. Une page dont le parsing VLM a
	// échoué n'y figure pas (cf. Merge) ; son échec reste visible dans
	// Parsing et Extraction.
	Pages []PageContent

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
	return p.run(ctx, reg, path, &progressTracker{})
}

func (p Pipeline) run(ctx context.Context, reg doctype.Registration, path string, progress *progressTracker) (Result, error) {
	triageResult, parseResults, merged, pageTexts, err := p.prepare(ctx, path, progress)
	if err != nil {
		return Result{Path: path, Triage: triageResult, Parsing: parseResults}, err
	}
	searchText := concatPageTexts(pageTexts)

	extractionResults, err := p.extractPages(ctx, reg, pageTexts, progress)
	if err != nil {
		return Result{Path: path, DocType: reg.Name, Triage: triageResult, Parsing: parseResults, SearchText: searchText, Pages: merged}, err
	}
	extractionResults = withVLMFailures(extractionResults, parseResults)

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
		SearchText: searchText,
		Pages:      merged,
	}, nil
}

// RunAuto exécute Triage puis Parsing comme Run, mais détermine le type
// de document par classification automatique (p.Classifier, parmi les
// types de p.Registry) au lieu de le recevoir en paramètre — répond à
// "uploader un document et laisser le modèle trouver le type". Si aucun
// type ne correspond avec confiance, l'Extraction est simplement vide
// (Result.DocType == "") : ce n'est pas une erreur, juste l'absence
// d'extracteur applicable, cohérent avec "jamais de valeur inventée".
//
// onProgress (nil accepté) reçoit l'avancement après le triage puis après
// chaque page lue ou extraite — jalon 23.
func (p Pipeline) RunAuto(ctx context.Context, path string, onProgress ProgressFunc) (Result, error) {
	if p.Classifier == nil || p.Registry == nil {
		return Result{}, fmt.Errorf("pipeline: RunAuto requires Classifier and Registry to be set")
	}

	progress := &progressTracker{fn: onProgress}
	triageResult, parseResults, merged, pageTexts, err := p.prepare(ctx, path, progress)
	if err != nil {
		return Result{Path: path, Triage: triageResult, Parsing: parseResults}, err
	}
	searchText := concatPageTexts(pageTexts)

	progress.stage(StageClassifying)
	candidates := candidatesFromRegistry(p.Registry)
	classification, err := p.Classifier.Classify(ctx, searchText, candidates)
	if err != nil {
		return Result{Path: path, Triage: triageResult, Parsing: parseResults, SearchText: searchText, Pages: merged}, fmt.Errorf("pipeline: classify %s: %w", path, err)
	}

	if classification.DocType == "" {
		return Result{
			Path: path, Triage: triageResult, Parsing: parseResults,
			ClassificationConfidence: classification.Confidence,
			SearchText:               searchText,
			Pages:                    merged,
		}, nil
	}

	reg, ok := p.Registry.Get(classification.DocType)
	if !ok {
		// Défensif : internal/classify ne devrait renvoyer que des noms
		// connus (contrainte d'énumération + vérification), mais on ne
		// fait jamais confiance aveuglément à une décision externe.
		return Result{Path: path, Triage: triageResult, Parsing: parseResults, SearchText: searchText, Pages: merged}, fmt.Errorf("pipeline: classifier returned unknown doc type %q", classification.DocType)
	}

	extractionResults, err := p.extractPages(ctx, reg, pageTexts, progress)
	if err != nil {
		return Result{Path: path, DocType: reg.Name, Triage: triageResult, Parsing: parseResults, SearchText: searchText, Pages: merged}, err
	}
	extractionResults = withVLMFailures(extractionResults, parseResults)

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
		SearchText:               searchText,
		Pages:                    merged,
	}, nil
}

// RunWithType exécute le pipeline pour path avec un type de document
// imposé explicitement (docType), sans classification — utilisé quand un
// type est réattribué manuellement à un document déjà traité
// (bibliothèque de documents, jalon 17). Requiert Registry (comme
// RunAuto) pour résoudre docType en Registration ; simple enveloppe
// autour de Run.
func (p Pipeline) RunWithType(ctx context.Context, docType, path string, onProgress ProgressFunc) (Result, error) {
	if p.Registry == nil {
		return Result{}, fmt.Errorf("pipeline: RunWithType requires Registry to be set")
	}
	reg, ok := p.Registry.Get(docType)
	if !ok {
		return Result{}, fmt.Errorf("pipeline: unknown doc type %q", docType)
	}
	return p.run(ctx, reg, path, &progressTracker{fn: onProgress})
}

// prepare exécute Triage puis Parsing — la partie commune à Run et
// RunAuto, indépendante du type de document.
func (p Pipeline) prepare(ctx context.Context, path string, progress *progressTracker) (triageResult triage.Result, parseResults []parsing.PageResult, merged []PageContent, pageTexts []triage.PageText, err error) {
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
	progress.triaged(len(nativePages), len(pagesToParse), Merge(nativePages, triageResult, nil))

	parser := parsing.Parser{Renderer: p.Renderer, VLM: p.VLM, DPI: p.DPI, Concurrency: p.VLMConcurrency, OnPage: progress.pageParsed}
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
func (p Pipeline) extractPages(ctx context.Context, reg doctype.Registration, pageTexts []triage.PageText, progress *progressTracker) ([]extraction.Result, error) {
	progress.extracting(len(pageTexts))
	extractor := extraction.Extractor{LLM: p.LLM, ConfidenceThreshold: p.ConfidenceThreshold, Concurrency: p.LLMConcurrency, OnPage: progress.pageExtracted}
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
