package extraction

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/doctype"
	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
)

func factureRegistration(t *testing.T) doctype.Registration {
	t.Helper()
	reg, ok := doctype.NewDefaultRegistry().Get("facture")
	if !ok {
		t.Fatal(`doctype registry has no "facture" registration`)
	}
	return reg
}

func TestExtractor_ExtractPages_Success(t *testing.T) {
	highConfidence := json.RawMessage(`{
		"numero": {"value": "F-1", "confidence": 0.95, "source_snippet": "F-1"},
		"fournisseur": {"value": "Acme", "confidence": 0.9, "source_snippet": "Acme"},
		"total_ttc": {"value": 123.45, "confidence": 0.9, "source_snippet": "123.45"}
	}`)
	llmClient := &llm.FakeClient{Results: map[int]llm.ExtractResult{
		1: {JSON: highConfidence, Model: llm.ModelInfo{Name: "test-llm", Version: "1"}, Prompt: "p"},
	}}
	e := Extractor{LLM: llmClient}

	got, err := e.ExtractPages(context.Background(), factureRegistration(t), []triage.PageText{
		{Page: 1, Text: "Facture F-1, Acme, 123.45 EUR"},
	})
	if err != nil {
		t.Fatalf("ExtractPages() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(got))
	}
	if got[0].Failed {
		t.Errorf("results[0].Failed = true, want false: %+v", got[0])
	}
	if got[0].NeedsReview {
		t.Errorf("results[0].NeedsReview = true, want false (all fields above threshold)")
	}
	if string(got[0].JSON) != string(highConfidence) {
		t.Errorf("results[0].JSON = %s, want %s", got[0].JSON, highConfidence)
	}

	// Le schéma envoyé au LLM doit être celui dérivé du type de document.
	if len(llmClient.Calls) != 1 {
		t.Fatalf("len(Calls) = %d, want 1", len(llmClient.Calls))
	}
	var sentSchema map[string]any
	if err := json.Unmarshal(llmClient.Calls[0].Schema, &sentSchema); err != nil {
		t.Fatalf("sent schema is not valid JSON: %v", err)
	}
	if sentSchema["type"] != "object" {
		t.Errorf("sent schema type = %v, want object", sentSchema["type"])
	}
}

func TestExtractor_ExtractPages_LowConfidence_MarksNeedsReview(t *testing.T) {
	lowConfidence := json.RawMessage(`{
		"numero": {"value": "F-1", "confidence": 0.95, "source_snippet": "F-1"},
		"fournisseur": {"value": "Acme", "confidence": 0.9, "source_snippet": "Acme"},
		"total_ttc": {"value": 123.45, "confidence": 0.2, "source_snippet": "???"}
	}`)
	llmClient := &llm.FakeClient{Results: map[int]llm.ExtractResult{1: {JSON: lowConfidence}}}
	e := Extractor{LLM: llmClient}

	got, err := e.ExtractPages(context.Background(), factureRegistration(t), []triage.PageText{
		{Page: 1, Text: "Facture F-1, Acme, montant illisible"},
	})
	if err != nil {
		t.Fatalf("ExtractPages() error = %v, want nil", err)
	}
	if got[0].Failed {
		t.Errorf("results[0].Failed = true, want false (low confidence is not a failure)")
	}
	if !got[0].NeedsReview {
		t.Fatal("results[0].NeedsReview = false, want true")
	}
	if len(got[0].LowConfidenceFields) != 1 || got[0].LowConfidenceFields[0] != "total_ttc" {
		t.Errorf("LowConfidenceFields = %v, want [total_ttc]", got[0].LowConfidenceFields)
	}
}

func TestExtractor_ExtractPages_LLMFailure_MarksFailedAndContinues(t *testing.T) {
	ok := json.RawMessage(`{
		"numero": {"value": "F-2", "confidence": 0.9, "source_snippet": "F-2"},
		"fournisseur": {"value": "Acme", "confidence": 0.9, "source_snippet": "Acme"},
		"total_ttc": {"value": 10.0, "confidence": 0.9, "source_snippet": "10.0"}
	}`)
	llmClient := &llm.FakeClient{Results: map[int]llm.ExtractResult{2: {JSON: ok}}}
	e := Extractor{LLM: llmClient}

	got, err := e.ExtractPages(context.Background(), factureRegistration(t), []triage.PageText{
		{Page: 1, Text: "page sans résultat configuré -> erreur fake"},
		{Page: 2, Text: "Facture F-2, Acme, 10.0 EUR"},
	})
	if err != nil {
		t.Fatalf("ExtractPages() error = %v, want nil (per-page failures should not abort)", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(got))
	}
	if !got[0].Failed || got[0].Error == "" {
		t.Errorf("results[0] = %+v, want Failed=true with a non-empty Error", got[0])
	}
	if got[1].Failed {
		t.Errorf("results[1] = %+v, want page 2 to have succeeded despite page 1 failing", got[1])
	}
}

func TestExtractor_ExtractPages_MalformedLLMJSON_MarksFailed(t *testing.T) {
	llmClient := &llm.FakeClient{Results: map[int]llm.ExtractResult{
		1: {JSON: json.RawMessage(`not json`)},
	}}
	e := Extractor{LLM: llmClient}

	got, err := e.ExtractPages(context.Background(), factureRegistration(t), []triage.PageText{
		{Page: 1, Text: "x"},
	})
	if err != nil {
		t.Fatalf("ExtractPages() error = %v, want nil", err)
	}
	if !got[0].Failed || got[0].Error == "" {
		t.Errorf("results[0] = %+v, want Failed=true (malformed JSON from LLM)", got[0])
	}
}

func TestExtractor_ExtractPages_NoPages_ReturnsEmpty(t *testing.T) {
	e := Extractor{LLM: &llm.FakeClient{}}

	got, err := e.ExtractPages(context.Background(), factureRegistration(t), nil)
	if err != nil {
		t.Fatalf("ExtractPages() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("ExtractPages() = %v, want empty", got)
	}
}

func TestExtractor_ExtractPages_CanceledContext_ReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	e := Extractor{LLM: &llm.FakeClient{Results: map[int]llm.ExtractResult{1: {JSON: json.RawMessage(`{}`)}}}}

	_, err := e.ExtractPages(ctx, factureRegistration(t), []triage.PageText{{Page: 1, Text: "x"}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ExtractPages() error = %v, want it to wrap context.Canceled", err)
	}
}

func TestExtractor_ExtractPages_UsesDefaultConfidenceThreshold(t *testing.T) {
	borderline := json.RawMessage(`{
		"numero": {"value": "F-1", "confidence": 0.69, "source_snippet": "F-1"},
		"fournisseur": {"value": "Acme", "confidence": 0.9, "source_snippet": "Acme"},
		"total_ttc": {"value": 1.0, "confidence": 0.9, "source_snippet": "1.0"}
	}`)
	llmClient := &llm.FakeClient{Results: map[int]llm.ExtractResult{1: {JSON: borderline}}}
	e := Extractor{LLM: llmClient} // ConfidenceThreshold non renseigné -> défaut

	got, err := e.ExtractPages(context.Background(), factureRegistration(t), []triage.PageText{{Page: 1, Text: "x"}})
	if err != nil {
		t.Fatalf("ExtractPages() error = %v, want nil", err)
	}
	if !got[0].NeedsReview {
		t.Errorf("NeedsReview = false, want true (0.69 < default threshold %v)", DefaultConfidenceThreshold)
	}
}
