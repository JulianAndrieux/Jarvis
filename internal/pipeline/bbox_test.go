package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/bbox"
	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
	"github.com/JulianAndrieux/Jarvis/internal/vlm"
)

func TestPipeline_Run_AttachesBBoxOnNativePages(t *testing.T) {
	extracted := json.RawMessage(`{
		"numero": {"value": "F-1", "confidence": 0.9, "source_snippet": "F-1"},
		"fournisseur": {"value": "Acme", "confidence": 0.9, "source_snippet": "Acme"},
		"total_ttc": {"value": 1.0, "confidence": 0.9, "source_snippet": "1.0"}
	}`)
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{
		{Page: 1, Text: "F-1 Acme 1.0 - " + longEnoughText()},
	}}
	bboxExtractor := bbox.FakeExtractor{Pages: []bbox.PageWords{
		{Page: 1, Width: 612, Height: 792, Words: []bbox.Word{
			{Text: "F-1", XMin: 1, YMin: 2, XMax: 3, YMax: 4},
		}},
	}}
	llmClient := &llm.FakeClient{Results: map[int]llm.ExtractResult{1: {JSON: extracted}}}

	p := Pipeline{
		TextExtractor: textExtractor,
		VLM:           &vlm.FakeClient{},
		LLM:           llmClient,
		BBox:          bboxExtractor,
	}

	got, err := p.Run(context.Background(), factureRegistration(t), "doc.pdf")
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	var decoded map[string]map[string]any
	if err := json.Unmarshal(got.Extraction[0].JSON, &decoded); err != nil {
		t.Fatalf("extraction JSON is not valid: %v", err)
	}
	if _, ok := decoded["numero"]["bbox"]; !ok {
		t.Errorf("numero.bbox missing, want it attached from the native page's words: %+v", decoded["numero"])
	}
}

func TestPipeline_Run_NoBBoxExtractor_LeavesJSONUnchanged(t *testing.T) {
	extracted := json.RawMessage(`{
		"numero": {"value": "F-1", "confidence": 0.9, "source_snippet": "F-1"},
		"fournisseur": {"value": "Acme", "confidence": 0.9, "source_snippet": "Acme"},
		"total_ttc": {"value": 1.0, "confidence": 0.9, "source_snippet": "1.0"}
	}`)
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{
		{Page: 1, Text: "F-1 Acme 1.0 - " + longEnoughText()},
	}}
	llmClient := &llm.FakeClient{Results: map[int]llm.ExtractResult{1: {JSON: extracted}}}

	p := Pipeline{TextExtractor: textExtractor, VLM: &vlm.FakeClient{}, LLM: llmClient} // BBox non renseigné

	got, err := p.Run(context.Background(), factureRegistration(t), "doc.pdf")
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if string(got.Extraction[0].JSON) != string(extracted) {
		t.Errorf("JSON = %s, want unchanged %s", got.Extraction[0].JSON, extracted)
	}
}

func TestPipeline_Run_BBoxExtractorError_DegradesGracefully(t *testing.T) {
	extracted := json.RawMessage(`{
		"numero": {"value": "F-1", "confidence": 0.9, "source_snippet": "F-1"},
		"fournisseur": {"value": "Acme", "confidence": 0.9, "source_snippet": "Acme"},
		"total_ttc": {"value": 1.0, "confidence": 0.9, "source_snippet": "1.0"}
	}`)
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{
		{Page: 1, Text: "F-1 Acme 1.0 - " + longEnoughText()},
	}}
	llmClient := &llm.FakeClient{Results: map[int]llm.ExtractResult{1: {JSON: extracted}}}
	bboxExtractor := bbox.FakeExtractor{Err: errors.New("pdftotext -bbox boom")}

	p := Pipeline{TextExtractor: textExtractor, VLM: &vlm.FakeClient{}, LLM: llmClient, BBox: bboxExtractor}

	got, err := p.Run(context.Background(), factureRegistration(t), "doc.pdf")
	if err != nil {
		t.Fatalf("Run() error = %v, want nil (bbox enrichment failure must not fail the pipeline)", err)
	}
	if string(got.Extraction[0].JSON) != string(extracted) {
		t.Errorf("JSON = %s, want unchanged (bbox degraded gracefully) %s", got.Extraction[0].JSON, extracted)
	}
}

func TestPipeline_Run_VLMPage_NeverGetsBBox(t *testing.T) {
	extracted := json.RawMessage(`{
		"numero": {"value": "F-2", "confidence": 0.9, "source_snippet": "F-2"},
		"fournisseur": {"value": "Acme", "confidence": 0.9, "source_snippet": "Acme"},
		"total_ttc": {"value": 2.0, "confidence": 0.9, "source_snippet": "2.0"}
	}`)
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{{Page: 1, Text: ""}}}
	renderer := fakeRenderer{png: []byte("png")}
	vlmClient := &vlm.FakeClient{Results: map[int]vlm.ParseResult{1: {Markdown: "F-2 Acme 2.0"}}}
	llmClient := &llm.FakeClient{Results: map[int]llm.ExtractResult{1: {JSON: extracted}}}
	// Mots positionnés fournis même pour cette page : ne doivent jamais
	// être utilisés puisque la page vient du VLM, pas du texte natif.
	bboxExtractor := bbox.FakeExtractor{Pages: []bbox.PageWords{
		{Page: 1, Words: []bbox.Word{{Text: "F-2", XMin: 1, YMin: 2, XMax: 3, YMax: 4}}},
	}}

	p := Pipeline{TextExtractor: textExtractor, Renderer: renderer, VLM: vlmClient, LLM: llmClient, BBox: bboxExtractor}

	got, err := p.Run(context.Background(), factureRegistration(t), "doc.pdf")
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	var decoded map[string]map[string]any
	if err := json.Unmarshal(got.Extraction[0].JSON, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["numero"]["bbox"]; ok {
		t.Errorf("numero.bbox present for a VLM-sourced page, want absent: %+v", decoded["numero"])
	}
}
