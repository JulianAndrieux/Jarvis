package classify

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/llm"
)

func candidates() []Candidate {
	return []Candidate{
		{Name: "facture", Description: "Facture commerciale : numéro, fournisseur, montant total TTC"},
		{Name: "piece_identite", Description: "Pièce d'identité : nom, date de naissance, numéro de document"},
	}
}

func TestLLMClassifier_Classify_ReturnsDecodedResult(t *testing.T) {
	fake := &llm.FakeClient{Results: map[int]llm.ExtractResult{
		0: {JSON: json.RawMessage(`{"doc_type":"facture","confidence":0.9}`)},
	}}
	c := LLMClassifier{Client: fake}

	got, err := c.Classify(context.Background(), "Facture n. 42, Total TTC 100 EUR", candidates())
	if err != nil {
		t.Fatalf("Classify() error = %v, want nil", err)
	}
	if got.DocType != "facture" || got.Confidence != 0.9 {
		t.Errorf("Classify() = %+v, want {facture 0.9}", got)
	}
}

func TestLLMClassifier_Classify_SendsSchemaConstrainedToKnownCandidates(t *testing.T) {
	fake := &llm.FakeClient{Results: map[int]llm.ExtractResult{
		0: {JSON: json.RawMessage(`{"doc_type":"facture","confidence":0.9}`)},
	}}
	c := LLMClassifier{Client: fake}

	if _, err := c.Classify(context.Background(), "peu importe", candidates()); err != nil {
		t.Fatal(err)
	}

	if len(fake.Calls) != 1 {
		t.Fatalf("len(Calls) = %d, want 1", len(fake.Calls))
	}
	var schema map[string]any
	if err := json.Unmarshal(fake.Calls[0].Schema, &schema); err != nil {
		t.Fatalf("Schema is not valid JSON: %v", err)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no \"properties\": %v", schema)
	}
	docType, ok := props["doc_type"].(map[string]any)
	if !ok {
		t.Fatalf("schema.properties has no \"doc_type\": %v", props)
	}
	enumRaw, ok := docType["enum"].([]any)
	if !ok {
		t.Fatalf("doc_type has no \"enum\" (decodage doit etre contraint aux candidats connus) : %v", docType)
	}
	enum := make([]string, len(enumRaw))
	for i, v := range enumRaw {
		enum[i] = v.(string)
	}
	wantEnum := []string{"facture", "piece_identite", "unknown"}
	for _, w := range wantEnum {
		found := false
		for _, e := range enum {
			if e == w {
				found = true
			}
		}
		if !found {
			t.Errorf("enum = %v, want it to contain %q", enum, w)
		}
	}
}

func TestLLMClassifier_Classify_PromptMentionsCandidateNamesAndDescriptions(t *testing.T) {
	fake := &llm.FakeClient{Results: map[int]llm.ExtractResult{
		0: {JSON: json.RawMessage(`{"doc_type":"facture","confidence":0.9}`)},
	}}
	c := LLMClassifier{Client: fake}

	if _, err := c.Classify(context.Background(), "peu importe", candidates()); err != nil {
		t.Fatal(err)
	}

	prompt := fake.Calls[0].Prompt
	if !strings.Contains(prompt, "facture") || !strings.Contains(prompt, "Facture commerciale") {
		t.Errorf("prompt = %q, want it to mention the candidate name and description", prompt)
	}
	if !strings.Contains(prompt, "piece_identite") {
		t.Errorf("prompt = %q, want it to mention every candidate", prompt)
	}
}

func TestLLMClassifier_Classify_UnknownDocType_ReturnsEmptyDocType(t *testing.T) {
	fake := &llm.FakeClient{Results: map[int]llm.ExtractResult{
		0: {JSON: json.RawMessage(`{"doc_type":"unknown","confidence":0.15}`)},
	}}
	c := LLMClassifier{Client: fake}

	got, err := c.Classify(context.Background(), "un texte ambigu", candidates())
	if err != nil {
		t.Fatalf("Classify() error = %v, want nil", err)
	}
	if got.DocType != "" {
		t.Errorf("DocType = %q, want empty for \"unknown\"", got.DocType)
	}
	if got.Confidence != 0.15 {
		t.Errorf("Confidence = %v, want 0.15 (conservee meme si non classifie)", got.Confidence)
	}
}

func TestLLMClassifier_Classify_HallucinatedDocType_TreatedAsUnknown(t *testing.T) {
	// Défense en profondeur : même si l'enum devrait l'empêcher, on ne
	// fait jamais confiance aveuglément à la sortie du LLM.
	fake := &llm.FakeClient{Results: map[int]llm.ExtractResult{
		0: {JSON: json.RawMessage(`{"doc_type":"ce-type-nexiste-pas","confidence":0.8}`)},
	}}
	c := LLMClassifier{Client: fake}

	got, err := c.Classify(context.Background(), "texte", candidates())
	if err != nil {
		t.Fatalf("Classify() error = %v, want nil", err)
	}
	if got.DocType != "" {
		t.Errorf("DocType = %q, want empty for a hallucinated type not in candidates", got.DocType)
	}
}

func TestLLMClassifier_Classify_NoCandidates_ReturnsEmptyWithoutCallingLLM(t *testing.T) {
	fake := &llm.FakeClient{}
	c := LLMClassifier{Client: fake}

	got, err := c.Classify(context.Background(), "texte", nil)
	if err != nil {
		t.Fatalf("Classify() error = %v, want nil", err)
	}
	if got.DocType != "" {
		t.Errorf("DocType = %q, want empty", got.DocType)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("len(Calls) = %d, want 0 (no LLM call with zero candidates)", len(fake.Calls))
	}
}

func TestLLMClassifier_Classify_TruncatesLongText(t *testing.T) {
	fake := &llm.FakeClient{Results: map[int]llm.ExtractResult{
		0: {JSON: json.RawMessage(`{"doc_type":"facture","confidence":0.9}`)},
	}}
	c := LLMClassifier{Client: fake, MaxTextLength: 10}

	if _, err := c.Classify(context.Background(), "un texte beaucoup trop long pour la classification", candidates()); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls[0].Text) != 10 {
		t.Errorf("len(Text) = %d, want 10 (MaxTextLength)", len(fake.Calls[0].Text))
	}
}

func TestLLMClassifier_Classify_LLMError_Propagates(t *testing.T) {
	fake := &llm.FakeClient{Err: errors.New("llm boom")}
	c := LLMClassifier{Client: fake}

	_, err := c.Classify(context.Background(), "texte", candidates())
	if err == nil {
		t.Fatal("Classify() error = nil, want the LLM error to propagate")
	}
}

func TestLLMClassifier_Classify_InvalidJSON_ReturnsError(t *testing.T) {
	fake := &llm.FakeClient{Results: map[int]llm.ExtractResult{
		0: {JSON: json.RawMessage(`not json`)},
	}}
	c := LLMClassifier{Client: fake}

	_, err := c.Classify(context.Background(), "texte", candidates())
	if err == nil {
		t.Fatal("Classify() error = nil, want an error for malformed JSON")
	}
}
