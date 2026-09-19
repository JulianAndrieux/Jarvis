package extraction

import (
	"context"
	"encoding/json"
	"fmt"

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
}

// ExtractPages traite chaque page de pages dans l'ordre, pour le type de
// document reg. Une erreur de niveau document (typiquement : contexte
// annulé) interrompt le traitement et est retournée ; les échecs par page
// (LLM ou JSON non conforme) sont capturés dans Result.Failed/Error sans
// interrompre les autres pages.
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

	results := make([]Result, 0, len(pages))
	for _, page := range pages {
		if err := ctx.Err(); err != nil {
			return results, fmt.Errorf("extraction: %s: %w", reg.Name, err)
		}

		results = append(results, e.extractOnePage(ctx, page, schemaJSON, prompt, threshold))
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
