package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/classify"
	"github.com/JulianAndrieux/Jarvis/internal/doctype"
	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
	"github.com/JulianAndrieux/Jarvis/internal/vlm"
)

func TestPipeline_RunAuto_ClassifiesThenExtracts(t *testing.T) {
	extracted := json.RawMessage(`{
		"numero": {"value": "F-1", "confidence": 0.9, "source_snippet": "F-1"},
		"fournisseur": {"value": "Acme", "confidence": 0.9, "source_snippet": "Acme"},
		"total_ttc": {"value": 1.0, "confidence": 0.9, "source_snippet": "1.0"}
	}`)
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{
		{Page: 1, Text: "Facture F-1, Acme, 1.0 EUR - " + longEnoughText()},
	}}
	llmClient := &llm.FakeClient{Results: map[int]llm.ExtractResult{1: {JSON: extracted}}}
	classifier := &classify.FakeClassifier{Result: classify.Result{DocType: "facture", Confidence: 0.95}}

	p := Pipeline{
		TextExtractor: textExtractor,
		VLM:           &vlm.FakeClient{},
		LLM:           llmClient,
		Classifier:    classifier,
		Registry:      doctype.NewDefaultRegistry(),
	}

	got, err := p.RunAuto(context.Background(), "doc.pdf")
	if err != nil {
		t.Fatalf("RunAuto() error = %v, want nil", err)
	}
	if got.DocType != "facture" {
		t.Errorf("DocType = %q, want facture", got.DocType)
	}
	if got.ClassificationConfidence != 0.95 {
		t.Errorf("ClassificationConfidence = %v, want 0.95", got.ClassificationConfidence)
	}
	if len(got.Extraction) != 1 || got.Extraction[0].Failed {
		t.Fatalf("Extraction = %+v, want 1 successful result", got.Extraction)
	}
	// Ne présume pas du nombre total de types enregistrés (le registre
	// par défaut en gagne au fil des jalons) : vérifie seulement que le
	// candidat facture y figure bien, avec sa description.
	foundFacture := false
	for _, c := range classifier.GotCandidates {
		if c.Name == "facture" {
			foundFacture = true
			if c.Description == "" {
				t.Errorf("facture candidate has no description")
			}
		}
	}
	if !foundFacture {
		t.Errorf("classifier.GotCandidates = %+v, want it to include the registry's facture candidate", classifier.GotCandidates)
	}
}

func TestPipeline_RunAuto_UnknownType_SkipsExtractionWithoutError(t *testing.T) {
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{
		{Page: 1, Text: "Un texte quelconque - " + longEnoughText()},
	}}
	llmClient := &llm.FakeClient{} // ne doit jamais être appelé pour l'extraction
	classifier := &classify.FakeClassifier{Result: classify.Result{DocType: "", Confidence: 0.1}}

	p := Pipeline{
		TextExtractor: textExtractor,
		VLM:           &vlm.FakeClient{},
		LLM:           llmClient,
		Classifier:    classifier,
		Registry:      doctype.NewDefaultRegistry(),
	}

	got, err := p.RunAuto(context.Background(), "doc.pdf")
	if err != nil {
		t.Fatalf("RunAuto() error = %v, want nil (classification sans correspondance n'est pas un échec)", err)
	}
	if got.DocType != "" {
		t.Errorf("DocType = %q, want empty", got.DocType)
	}
	if len(got.Extraction) != 0 {
		t.Errorf("Extraction = %v, want empty (pas d'extracteur pour un type non reconnu)", got.Extraction)
	}
	if len(llmClient.Calls) != 0 {
		t.Errorf("LLM.Calls = %v, want empty", llmClient.Calls)
	}
}

func TestPipeline_RunAuto_MissingClassifier_ReturnsError(t *testing.T) {
	p := Pipeline{
		TextExtractor: triage.FakeExtractor{Pages: []triage.PageText{{Page: 1, Text: longEnoughText()}}},
		VLM:           &vlm.FakeClient{},
		LLM:           &llm.FakeClient{},
		Registry:      doctype.NewDefaultRegistry(),
		// Classifier volontairement nil.
	}

	_, err := p.RunAuto(context.Background(), "doc.pdf")
	if err == nil {
		t.Fatal("RunAuto() error = nil, want non-nil when Classifier is not configured")
	}
}

func TestPipeline_RunAuto_MissingRegistry_ReturnsError(t *testing.T) {
	p := Pipeline{
		TextExtractor: triage.FakeExtractor{Pages: []triage.PageText{{Page: 1, Text: longEnoughText()}}},
		VLM:           &vlm.FakeClient{},
		LLM:           &llm.FakeClient{},
		Classifier:    &classify.FakeClassifier{},
		// Registry volontairement nil.
	}

	_, err := p.RunAuto(context.Background(), "doc.pdf")
	if err == nil {
		t.Fatal("RunAuto() error = nil, want non-nil when Registry is not configured")
	}
}

func TestPipeline_RunAuto_ClassifierError_Propagates(t *testing.T) {
	p := Pipeline{
		TextExtractor: triage.FakeExtractor{Pages: []triage.PageText{{Page: 1, Text: longEnoughText()}}},
		VLM:           &vlm.FakeClient{},
		LLM:           &llm.FakeClient{},
		Classifier:    &classify.FakeClassifier{Err: errors.New("classify boom")},
		Registry:      doctype.NewDefaultRegistry(),
	}

	_, err := p.RunAuto(context.Background(), "doc.pdf")
	if err == nil {
		t.Fatal("RunAuto() error = nil, want the classifier error to propagate")
	}
}

func TestPipeline_RunAuto_TriageExtractorError_ReturnsError(t *testing.T) {
	p := Pipeline{
		TextExtractor: triage.FakeExtractor{Err: errors.New("pdftotext boom")},
		VLM:           &vlm.FakeClient{},
		LLM:           &llm.FakeClient{},
		Classifier:    &classify.FakeClassifier{},
		Registry:      doctype.NewDefaultRegistry(),
	}

	_, err := p.RunAuto(context.Background(), "doc.pdf")
	if err == nil {
		t.Fatal("RunAuto() error = nil, want non-nil when triage extraction fails")
	}
}

func TestPipeline_RunAuto_SetsSearchTextFromPageContent(t *testing.T) {
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{
		{Page: 1, Text: "page-un contient le mot Kangourou - " + longEnoughText()},
	}}
	classifier := &classify.FakeClassifier{Result: classify.Result{DocType: "", Confidence: 0}}

	p := Pipeline{
		TextExtractor: textExtractor,
		VLM:           &vlm.FakeClient{},
		LLM:           &llm.FakeClient{},
		Classifier:    classifier,
		Registry:      doctype.NewDefaultRegistry(),
	}

	got, err := p.RunAuto(context.Background(), "doc.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.SearchText, "Kangourou") {
		t.Errorf("SearchText = %q, want it to contain the document's text (even when unclassified)", got.SearchText)
	}
}

func TestPipeline_RunWithType_ExtractsWithGivenType(t *testing.T) {
	extracted := json.RawMessage(`{
		"numero": {"value": "F-1", "confidence": 0.9, "source_snippet": "F-1"},
		"fournisseur": {"value": "Acme", "confidence": 0.9, "source_snippet": "Acme"},
		"total_ttc": {"value": 1.0, "confidence": 0.9, "source_snippet": "1.0"}
	}`)
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{
		{Page: 1, Text: "Facture F-1, Acme, 1.0 EUR - " + longEnoughText()},
	}}
	llmClient := &llm.FakeClient{Results: map[int]llm.ExtractResult{1: {JSON: extracted}}}

	p := Pipeline{
		TextExtractor: textExtractor,
		VLM:           &vlm.FakeClient{},
		LLM:           llmClient,
		Registry:      doctype.NewDefaultRegistry(),
	}

	got, err := p.RunWithType(context.Background(), "facture", "doc.pdf")
	if err != nil {
		t.Fatalf("RunWithType() error = %v, want nil", err)
	}
	if got.DocType != "facture" {
		t.Errorf("DocType = %q, want facture", got.DocType)
	}
	if len(got.Extraction) != 1 || got.Extraction[0].Failed {
		t.Fatalf("Extraction = %+v, want 1 successful result", got.Extraction)
	}
}

func TestPipeline_RunWithType_UnknownDocType_ReturnsError(t *testing.T) {
	p := Pipeline{
		TextExtractor: triage.FakeExtractor{Pages: []triage.PageText{{Page: 1, Text: longEnoughText()}}},
		VLM:           &vlm.FakeClient{},
		LLM:           &llm.FakeClient{},
		Registry:      doctype.NewDefaultRegistry(),
	}

	_, err := p.RunWithType(context.Background(), "ce-type-nexiste-pas", "doc.pdf")
	if err == nil {
		t.Fatal("RunWithType() error = nil, want non-nil for an unknown doc type")
	}
}

func TestPipeline_RunWithType_MissingRegistry_ReturnsError(t *testing.T) {
	p := Pipeline{
		TextExtractor: triage.FakeExtractor{Pages: []triage.PageText{{Page: 1, Text: longEnoughText()}}},
		VLM:           &vlm.FakeClient{},
		LLM:           &llm.FakeClient{},
	}

	_, err := p.RunWithType(context.Background(), "facture", "doc.pdf")
	if err == nil {
		t.Fatal("RunWithType() error = nil, want non-nil when Registry is not configured")
	}
}

func TestPipeline_RunAuto_ClassifierReceivesConcatenatedPageText(t *testing.T) {
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{
		{Page: 1, Text: "page-un " + longEnoughText()},
		{Page: 2, Text: "page-deux " + longEnoughText()},
	}}
	classifier := &classify.FakeClassifier{Result: classify.Result{DocType: "", Confidence: 0}}

	p := Pipeline{
		TextExtractor: textExtractor,
		VLM:           &vlm.FakeClient{},
		LLM:           &llm.FakeClient{},
		Classifier:    classifier,
		Registry:      doctype.NewDefaultRegistry(),
	}

	if _, err := p.RunAuto(context.Background(), "doc.pdf"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(classifier.GotText, "page-un") || !strings.Contains(classifier.GotText, "page-deux") {
		t.Errorf("classifier.GotText = %q, want it to contain both pages' text", classifier.GotText)
	}
}

// Jalon 22 : un document non classé n'a pas d'extraction, mais son
// texte OCR reste consultable (et persisté).
func TestPipeline_RunAuto_UnknownType_StillKeepsPages(t *testing.T) {
	text := "Un texte quelconque - " + longEnoughText()
	p := Pipeline{
		TextExtractor: triage.FakeExtractor{Pages: []triage.PageText{{Page: 1, Text: text}}},
		VLM:           &vlm.FakeClient{},
		LLM:           &llm.FakeClient{},
		Classifier:    &classify.FakeClassifier{Result: classify.Result{DocType: "", Confidence: 0.1}},
		Registry:      doctype.NewDefaultRegistry(),
	}

	got, err := p.RunAuto(context.Background(), "doc.pdf")
	if err != nil {
		t.Fatalf("RunAuto() error = %v, want nil", err)
	}
	if len(got.Pages) != 1 || got.Pages[0].Text != text || got.Pages[0].Source != SourceNative {
		t.Errorf("Pages = %+v, want the single native page", got.Pages)
	}
}
