package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/doctype"
	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
	"github.com/JulianAndrieux/Jarvis/internal/vlm"
)

func factureRegistration(t *testing.T) doctype.Registration {
	t.Helper()
	reg, ok := doctype.NewDefaultRegistry().Get("facture")
	if !ok {
		t.Fatal(`doctype registry has no "facture" registration`)
	}
	return reg
}

func TestPipeline_Run_NativeOnly_SkipsVLM(t *testing.T) {
	extracted := json.RawMessage(`{
		"numero": {"value": "F-1", "confidence": 0.9, "source_snippet": "F-1"},
		"fournisseur": {"value": "Acme", "confidence": 0.9, "source_snippet": "Acme"},
		"total_ttc": {"value": 1.0, "confidence": 0.9, "source_snippet": "1.0"}
	}`)
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{
		{Page: 1, Text: "Facture F-1, Acme, 1.0 EUR - " + longEnoughText()},
	}}
	vlmClient := &vlm.FakeClient{} // ne doit jamais être appelé
	llmClient := &llm.FakeClient{Results: map[int]llm.ExtractResult{1: {JSON: extracted}}}

	p := Pipeline{
		TextExtractor: textExtractor,
		Renderer:      nil, // ne doit jamais être utilisé non plus
		VLM:           vlmClient,
		LLM:           llmClient,
	}

	got, err := p.Run(context.Background(), factureRegistration(t), "doc.pdf")
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	if !got.Triage.HasTextLayer {
		t.Errorf("Triage.HasTextLayer = false, want true")
	}
	if len(got.Parsing) != 0 {
		t.Errorf("Parsing = %v, want empty (no page needed the VLM)", got.Parsing)
	}
	if len(vlmClient.Calls) != 0 {
		t.Errorf("VLM.Calls = %v, want empty (VLM should never be called)", vlmClient.Calls)
	}
	if len(got.Extraction) != 1 || got.Extraction[0].Failed {
		t.Fatalf("Extraction = %+v, want 1 successful result", got.Extraction)
	}
}

func TestPipeline_Run_UnusablePage_GoesThroughVLM(t *testing.T) {
	extracted := json.RawMessage(`{
		"numero": {"value": "F-2", "confidence": 0.9, "source_snippet": "F-2"},
		"fournisseur": {"value": "Acme", "confidence": 0.9, "source_snippet": "Acme"},
		"total_ttc": {"value": 2.0, "confidence": 0.9, "source_snippet": "2.0"}
	}`)
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{{Page: 1, Text: ""}}}
	renderer := fakeRenderer{png: []byte("png-bytes")}
	vlmClient := &vlm.FakeClient{Results: map[int]vlm.ParseResult{
		1: {Markdown: "Facture F-2, Acme, 2.0 EUR"},
	}}
	llmClient := &llm.FakeClient{Results: map[int]llm.ExtractResult{1: {JSON: extracted}}}

	p := Pipeline{
		TextExtractor: textExtractor,
		Renderer:      renderer,
		VLM:           vlmClient,
		LLM:           llmClient,
	}

	got, err := p.Run(context.Background(), factureRegistration(t), "doc.pdf")
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	if got.Triage.HasTextLayer {
		t.Errorf("Triage.HasTextLayer = true, want false")
	}
	if len(got.Parsing) != 1 || got.Parsing[0].Failed {
		t.Fatalf("Parsing = %+v, want 1 successful result", got.Parsing)
	}
	if len(vlmClient.Calls) != 1 {
		t.Errorf("VLM.Calls = %v, want 1 call", vlmClient.Calls)
	}
	if len(got.Extraction) != 1 || got.Extraction[0].Failed {
		t.Fatalf("Extraction = %+v, want 1 successful result", got.Extraction)
	}
	if string(got.Extraction[0].JSON) != string(extracted) {
		t.Errorf("Extraction[0].JSON = %s, want %s", got.Extraction[0].JSON, extracted)
	}
}

func TestPipeline_Run_FailedVLMPage_SkipsExtractionForThatPage(t *testing.T) {
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{{Page: 1, Text: ""}}}
	renderer := fakeRenderer{err: errors.New("render boom")}
	vlmClient := &vlm.FakeClient{}
	llmClient := &llm.FakeClient{}

	p := Pipeline{
		TextExtractor: textExtractor,
		Renderer:      renderer,
		VLM:           vlmClient,
		LLM:           llmClient,
	}

	got, err := p.Run(context.Background(), factureRegistration(t), "doc.pdf")
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if len(got.Parsing) != 1 || !got.Parsing[0].Failed {
		t.Fatalf("Parsing = %+v, want 1 failed result", got.Parsing)
	}
	if len(got.Extraction) != 0 {
		t.Errorf("Extraction = %v, want empty (no usable content for the failed page)", got.Extraction)
	}
	if len(llmClient.Calls) != 0 {
		t.Errorf("LLM.Calls = %v, want empty (LLM should never be called for an unusable page)", llmClient.Calls)
	}
}

func TestPipeline_Run_TriageExtractorError_ReturnsError(t *testing.T) {
	textExtractor := triage.FakeExtractor{Err: errors.New("pdftotext boom")}
	p := Pipeline{TextExtractor: textExtractor, VLM: &vlm.FakeClient{}, LLM: &llm.FakeClient{}}

	_, err := p.Run(context.Background(), factureRegistration(t), "doc.pdf")
	if err == nil {
		t.Fatal("Run() error = nil, want non-nil when triage extraction fails")
	}
}

// longEnoughText pousse le texte au-dessus du seuil MinCharsPerPage par
// défaut du triage, pour que la page soit bien jugée "usable" dans ces
// tests.
func longEnoughText() string {
	s := ""
	for i := 0; i < 5; i++ {
		s += "du texte tout à fait ordinaire et lisible. "
	}
	return s
}

type fakeRenderer struct {
	png []byte
	err error
}

func (f fakeRenderer) RenderPage(ctx context.Context, path string, page int, dpi int) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.png, nil
}
