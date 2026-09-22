package extraction

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/JulianAndrieux/Jarvis/internal/doctype"
	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
)

// DefaultConfidenceThreshold est le seuil de confiance par défaut sous
// lequel un champ extrait est marqué pour revue humaine.
const DefaultConfidenceThreshold = 0.7

// ResolveConfidenceThreshold retombe sur DefaultConfidenceThreshold pour
// une valeur zéro. Partagée par Extractor.ExtractPages et MergePages pour
// ne pas dupliquer la règle "0 = défaut".
func ResolveConfidenceThreshold(t float64) float64 {
	if t == 0 {
		return DefaultConfidenceThreshold
	}
	return t
}

// DefaultPromptTemplate est le prompt envoyé au LLM quand
// Extractor.PromptTemplate n'est pas renseigné. %s est remplacé par
// doctype.Registration.Description.
const DefaultPromptTemplate = "Extrait les informations suivantes du texte, au format JSON strictement conforme au schéma fourni. Pour chaque champ, indique un score de confiance entre 0 et 1 et l'extrait exact du texte source qui justifie la valeur. Type de document : %s"

// Result est le résultat de l'étage Extraction pour une page.
//
// Conformément à la stratégie d'échec retenue (échec immédiat, pas de
// retry — voir CLAUDE.md), un échec LLM ou un JSON non conforme sur une
// page ne fait jamais échouer tout le document : la page est marquée
// Failed et le traitement continue sur les pages suivantes. Une confiance
// insuffisante sur un champ n'est PAS un échec : NeedsReview le signale
// sans bloquer le résultat, conformément à "jamais acceptée silencieusement,
// mais jamais rejetée non plus".
type Result struct {
	Page                int
	JSON                json.RawMessage
	Model               llm.ModelInfo
	Prompt              string
	NeedsReview         bool
	LowConfidenceFields []string
	Failed              bool
	Error               string
}

// Extractor orchestre l'appel au LLM puis la vérification de confiance
// pour chaque page.
type Extractor struct {
	LLM llm.Client
	// ConfidenceThreshold : 0 retombe sur DefaultConfidenceThreshold.
	ConfidenceThreshold float64
	// PromptTemplate : "" retombe sur DefaultPromptTemplate.
	PromptTemplate string
	// Concurrency borne le nombre de pages extraites en parallèle ; 0 ou 1
	// (par défaut) = séquentiel, comportement inchangé par rapport aux
	// jalons précédents. Même principe que parsing.Parser.Concurrency —
	// à aligner sur les "slots" parallèles du serveur LLM (jalon 21).
	Concurrency int
	// OnPage, si non-nil, est appelé dès qu'une page est extraite (succès
	// ou échec) — jalon 23, même contrat que parsing.Parser.OnPage : appels
	// sérialisés, y compris en mode parallèle.
	OnPage func(Result)
}

// ExtractPages traite chaque page de pages, pour le type de document reg.
// Une erreur de niveau document (typiquement : contexte annulé)
// interrompt le traitement et est retournée ; les échecs par page (LLM ou
// JSON non conforme) sont capturés dans Result.Failed/Error sans
// interrompre les autres pages. L'ordre des résultats correspond toujours
// à celui de pages, y compris en mode parallèle (Concurrency > 1).
func (e Extractor) ExtractPages(ctx context.Context, reg doctype.Registration, pages []triage.PageText) ([]Result, error) {
	threshold := ResolveConfidenceThreshold(e.ConfidenceThreshold)

	promptTemplate := e.PromptTemplate
	if promptTemplate == "" {
		promptTemplate = DefaultPromptTemplate
	}
	prompt := fmt.Sprintf(promptTemplate, reg.Description)

	docSchema, err := reg.Schema()
	if err != nil {
		return nil, fmt.Errorf("extraction: derive schema for %q: %w", reg.Name, err)
	}
	schemaJSON, err := json.Marshal(docSchema)
	if err != nil {
		return nil, fmt.Errorf("extraction: marshal schema for %q: %w", reg.Name, err)
	}

	if e.Concurrency <= 1 {
		return e.extractPagesSequential(ctx, reg.Name, pages, schemaJSON, prompt, threshold)
	}
	return e.extractPagesParallel(ctx, reg.Name, pages, schemaJSON, prompt, threshold)
}

func (e Extractor) extractPagesSequential(ctx context.Context, regName string, pages []triage.PageText, schemaJSON json.RawMessage, prompt string, threshold float64) ([]Result, error) {
	results := make([]Result, 0, len(pages))
	for _, page := range pages {
		if err := ctx.Err(); err != nil {
			return results, fmt.Errorf("extraction: %s: %w", regName, err)
		}
		r := e.extractOnePage(ctx, page, schemaJSON, prompt, threshold)
		results = append(results, r)
		if e.OnPage != nil {
			e.OnPage(r)
		}
	}
	return results, nil
}

// extractPagesParallel traite jusqu'à Concurrency pages simultanément.
// Les résultats sont écrits par index (pas un append concurrent) pour
// préserver l'ordre de pages malgré l'exécution parallèle.
func (e Extractor) extractPagesParallel(ctx context.Context, regName string, pages []triage.PageText, schemaJSON json.RawMessage, prompt string, threshold float64) ([]Result, error) {
	results := make([]Result, len(pages))
	sem := make(chan struct{}, e.Concurrency)
	var wg sync.WaitGroup
	var onPageMu sync.Mutex

	for i, page := range pages {
		if err := ctx.Err(); err != nil {
			wg.Wait()
			return results, fmt.Errorf("extraction: %s: %w", regName, err)
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return results, fmt.Errorf("extraction: %s: %w", regName, ctx.Err())
		}

		wg.Add(1)
		go func(i int, page triage.PageText) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = e.extractOnePage(ctx, page, schemaJSON, prompt, threshold)
			if e.OnPage != nil {
				onPageMu.Lock()
				e.OnPage(results[i])
				onPageMu.Unlock()
			}
		}(i, page)
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return results, fmt.Errorf("extraction: %s: %w", regName, err)
	}
	return results, nil
}

func (e Extractor) extractOnePage(ctx context.Context, page triage.PageText, schemaJSON json.RawMessage, prompt string, threshold float64) Result {
	out, err := e.LLM.Extract(ctx, llm.ExtractRequest{
		Page:   page.Page,
		Text:   page.Text,
		Schema: schemaJSON,
		Prompt: prompt,
	})
	if err != nil {
		return Result{Page: page.Page, Failed: true, Error: fmt.Sprintf("llm: %v", err)}
	}

	lowFields, err := LowConfidenceFields(out.JSON, threshold)
	if err != nil {
		return Result{Page: page.Page, Failed: true, Error: fmt.Sprintf("invalid JSON from LLM: %v", err)}
	}

	return Result{
		Page:                page.Page,
		JSON:                out.JSON,
		Model:               out.Model,
		Prompt:              out.Prompt,
		NeedsReview:         len(lowFields) > 0,
		LowConfidenceFields: lowFields,
	}
}
