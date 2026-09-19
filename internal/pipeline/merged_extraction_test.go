package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
	"github.com/JulianAndrieux/Jarvis/internal/vlm"
)

func TestPipeline_Run_MergesFieldsSplitAcrossPages(t *testing.T) {
	// Reproduit le cas réel observé (facture_multipage.pdf, jalon 10
	// finding 3) : numéro/fournisseur page 1, total page 2.
	page1JSON := json.RawMessage(`{
		"numero": {"value": "2026-0271", "confidence": 1.0, "source_snippet": "Facture n. 2026-0271"},
		"fournisseur": {"value": "Global Tech", "confidence": 1.0, "source_snippet": "Global Tech"},
		"total_ttc": {"value": 0, "confidence": 0, "source_snippet": ""}
	}`)
	page2JSON := json.RawMessage(`{
		"numero": {"value": "", "confidence": 0, "source_snippet": ""},
		"fournisseur": {"value": "", "confidence": 0, "source_snippet": ""},
		"total_ttc": {"value": 3978.00, "confidence": 1.0, "source_snippet": "Total TTC: 3978.00 EUR"}
	}`)

	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{
		{Page: 1, Text: "Facture n. 2026-0271 Global Tech - " + longEnoughText()},
		{Page: 2, Text: "Total TTC: 3978.00 EUR - " + longEnoughText()},
	}}
	llmClient := &llm.FakeClient{Results: map[int]llm.ExtractResult{
		1: {JSON: page1JSON},
		2: {JSON: page2JSON},
	}}

	p := Pipeline{TextExtractor: textExtractor, VLM: &vlm.FakeClient{}, LLM: llmClient}

	got, err := p.Run(context.Background(), factureRegistration(t), "doc.pdf")
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	if got.Merged.Failed {
		t.Fatalf("Merged.Failed = true, want false: %+v", got.Merged)
	}

	var merged map[string]map[string]any
	if err := json.Unmarshal(got.Merged.JSON, &merged); err != nil {
		t.Fatalf("Merged.JSON is invalid: %v", err)
	}
	if merged["numero"]["value"] != "2026-0271" {
		t.Errorf("merged numero = %v, want the page-1 value", merged["numero"]["value"])
	}
	if merged["total_ttc"]["value"] != 3978.00 {
		t.Errorf("merged total_ttc = %v, want the page-2 value", merged["total_ttc"]["value"])
	}
	if got.Merged.NeedsReview {
		t.Errorf("Merged.NeedsReview = true, want false: every field has a high-confidence source across the two pages")
	}

	// Extraction par page reste inchangée (vue additionnelle, pas un
	// remplacement).
	if len(got.Extraction) != 2 {
		t.Errorf("len(Extraction) = %d, want 2 (per-page results preserved)", len(got.Extraction))
	}
}
