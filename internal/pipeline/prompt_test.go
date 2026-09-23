package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
	"github.com/JulianAndrieux/Jarvis/internal/vlm"
)

// Prompt d'extraction modifiable depuis l'interface (onglet Agents) :
// relu pour chaque document traité.
func TestPipeline_Run_UsesCurrentExtractionPrompt(t *testing.T) {
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{{Page: 1, Text: "F-1 Acme 1.0 - " + longEnoughText()}}}
	llmClient := &llm.FakeClient{Results: map[int]llm.ExtractResult{1: {JSON: json.RawMessage(`{}`)}}}
	current := "V1 {{type_document}}"
	p := Pipeline{TextExtractor: textExtractor, VLM: &vlm.FakeClient{}, LLM: llmClient, ExtractionPrompt: func() string { return current }}

	p.Run(context.Background(), factureRegistration(t), "doc.pdf")
	current = "V2 {{type_document}}"
	p.Run(context.Background(), factureRegistration(t), "doc.pdf")

	if len(llmClient.Calls) != 2 || !strings.HasPrefix(llmClient.Calls[0].Prompt, "V1 Facture") || !strings.HasPrefix(llmClient.Calls[1].Prompt, "V2 Facture") {
		t.Errorf("prompts sent = %q, %q", llmClient.Calls[0].Prompt, llmClient.Calls[len(llmClient.Calls)-1].Prompt)
	}
}
