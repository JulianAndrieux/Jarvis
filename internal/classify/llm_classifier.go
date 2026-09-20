package classify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JulianAndrieux/Jarvis/internal/llm"
)

// DefaultPromptTemplate est le prompt envoyé au LLM quand
// LLMClassifier.PromptTemplate n'est pas renseigné. %s est remplacé par
// la liste des candidats (nom + description, un par ligne).
const DefaultPromptTemplate = "Identifie le type de ce document parmi la liste suivante, en te basant uniquement sur le texte fourni :\n%s\nRéponds avec le nom exact d'un type de la liste ci-dessus, ou \"unknown\" si aucun ne correspond clairement."

// DefaultMaxTextLength borne le texte envoyé au LLM pour la
// classification — elle n'a besoin que d'un signal, pas du document
// entier, et ça évite de dépasser le contexte sur un document long
// (l'étage Extraction, lui, traite chaque page séparément).
const DefaultMaxTextLength = 4000

// unknownDocType est la valeur que le LLM doit renvoyer quand aucun
// candidat ne correspond — jamais un champ vide ou un candidat au
// hasard.
const unknownDocType = "unknown"

// LLMClassifier utilise le LLM d'extraction déjà configuré (même
// modèle, aucun nouveau choix de modèle — cf. CLAUDE.md, "choix de
// modèle toujours argumenté") avec un JSON Schema dont l'énumération de
// doc_type est construite dynamiquement à partir des candidats reçus :
// le décodage contraint garantit structurellement que la réponse est un
// nom de candidat connu ou "unknown", jamais un texte libre.
type LLMClassifier struct {
	Client llm.Client
	// PromptTemplate : "" retombe sur DefaultPromptTemplate.
	PromptTemplate string
	// MaxTextLength : 0 retombe sur DefaultMaxTextLength.
	MaxTextLength int
}

// classificationOutput est la forme JSON demandée au LLM. Pas de
// schema.Field[T] ici : ce n'est pas une valeur extraite d'un document
// (qui porterait confiance + extrait source par champ), mais la
// décision du pipeline lui-même.
type classificationOutput struct {
	DocType    string  `json:"doc_type"`
	Confidence float64 `json:"confidence"`
}

func (c LLMClassifier) Classify(ctx context.Context, text string, candidates []Candidate) (Result, error) {
	if len(candidates) == 0 {
		return Result{}, nil
	}

	promptTemplate := c.PromptTemplate
	if promptTemplate == "" {
		promptTemplate = DefaultPromptTemplate
	}
	prompt := fmt.Sprintf(promptTemplate, formatCandidates(candidates))

	schemaJSON, err := json.Marshal(schemaFor(candidates))
	if err != nil {
		return Result{}, fmt.Errorf("classify: marshal schema: %w", err)
	}

	maxLen := c.MaxTextLength
	if maxLen == 0 {
		maxLen = DefaultMaxTextLength
	}
	if len(text) > maxLen {
		text = text[:maxLen]
	}

	out, err := c.Client.Extract(ctx, llm.ExtractRequest{
		Text:   text,
		Schema: schemaJSON,
		Prompt: prompt,
	})
	if err != nil {
		return Result{}, fmt.Errorf("classify: llm: %w", err)
	}

	var decoded classificationOutput
	if err := json.Unmarshal(out.JSON, &decoded); err != nil {
		return Result{}, fmt.Errorf("classify: invalid JSON from LLM: %w", err)
	}

	docType := decoded.DocType
	if !isKnownCandidate(docType, candidates) {
		// Défense en profondeur : la contrainte d'énumération du schéma
		// devrait déjà l'empêcher, mais on ne fait jamais confiance
		// aveuglément à une sortie de modèle.
		docType = ""
	}
	return Result{DocType: docType, Confidence: decoded.Confidence}, nil
}

// schemaFor construit le JSON Schema de classificationOutput à la main
// (pas via internal/schema.Derive, qui ne supporte pas d'énumération
// dynamique sur un type Go statique) : doc_type est contraint à
// l'ensemble des noms de candidats plus "unknown".
func schemaFor(candidates []Candidate) map[string]any {
	names := make([]any, 0, len(candidates)+1)
	for _, c := range candidates {
		names = append(names, c.Name)
	}
	names = append(names, unknownDocType)

	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"doc_type": map[string]any{
				"type":        "string",
				"enum":        names,
				"description": "Le type de document identifié, ou \"unknown\" si aucun ne correspond",
			},
			"confidence": map[string]any{
				"type":        "number",
				"description": "Score de confiance entre 0 et 1",
			},
		},
		"required": []any{"doc_type", "confidence"},
	}
}

func formatCandidates(candidates []Candidate) string {
	var b strings.Builder
	for _, c := range candidates {
		fmt.Fprintf(&b, "- %s : %s\n", c.Name, c.Description)
	}
	return b.String()
}

func isKnownCandidate(name string, candidates []Candidate) bool {
	for _, c := range candidates {
		if c.Name == name {
			return true
		}
	}
	return false
}
