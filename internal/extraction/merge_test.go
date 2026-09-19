package extraction

import (
	"encoding/json"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/llm"
)

func TestMergePages_SinglePage_ReturnsItsContent(t *testing.T) {
	raw := json.RawMessage(`{"numero":{"value":"F-1","confidence":0.9,"source_snippet":"F-1"}}`)
	results := []Result{
		{Page: 1, JSON: raw, Model: llm.ModelInfo{Name: "m", Version: "v"}, Prompt: "p"},
	}

	got := MergePages(results, 0.7)

	if got.Failed {
		t.Fatalf("Failed = true, want false: %+v", got)
	}
	if string(got.JSON) != string(raw) {
		t.Errorf("JSON = %s, want %s", got.JSON, raw)
	}
	if got.Model.Name != "m" || got.Prompt != "p" {
		t.Errorf("Model/Prompt = %+v/%q, want m/p", got.Model, got.Prompt)
	}
}

func TestMergePages_PicksHighestConfidencePerField(t *testing.T) {
	page1 := json.RawMessage(`{
		"numero": {"value": "2026-0271", "confidence": 1.0, "source_snippet": "Facture n. 2026-0271"},
		"fournisseur": {"value": "Global Tech", "confidence": 1.0, "source_snippet": "Global Tech"},
		"total_ttc": {"value": 0, "confidence": 0, "source_snippet": ""}
	}`)
	page2 := json.RawMessage(`{
		"numero": {"value": "", "confidence": 0, "source_snippet": ""},
		"fournisseur": {"value": "", "confidence": 0, "source_snippet": ""},
		"total_ttc": {"value": 3978.00, "confidence": 1.0, "source_snippet": "Total TTC: 3978.00 EUR"}
	}`)
	results := []Result{
		{Page: 1, JSON: page1},
		{Page: 2, JSON: page2},
	}

	got := MergePages(results, 0.7)
	if got.Failed {
		t.Fatalf("Failed = true, want false: %+v", got)
	}

	var merged map[string]map[string]any
	if err := json.Unmarshal(got.JSON, &merged); err != nil {
		t.Fatalf("merged JSON is invalid: %v", err)
	}
	if merged["numero"]["value"] != "2026-0271" {
		t.Errorf("numero.value = %v, want the page-1 value", merged["numero"]["value"])
	}
	if merged["total_ttc"]["value"] != 3978.00 {
		t.Errorf("total_ttc.value = %v, want the page-2 value", merged["total_ttc"]["value"])
	}
	if got.NeedsReview {
		t.Errorf("NeedsReview = true, want false (every field has a high-confidence source)")
	}
}

func TestMergePages_AllPagesFailed_ReturnsFailed(t *testing.T) {
	results := []Result{
		{Page: 1, Failed: true, Error: "boom1"},
		{Page: 2, Failed: true, Error: "boom2"},
	}

	got := MergePages(results, 0.7)

	if !got.Failed {
		t.Fatal("Failed = false, want true when every page failed")
	}
	if got.Error == "" {
		t.Error("Error is empty, want an explanation")
	}
}

func TestMergePages_SomePagesFailed_IgnoresThem(t *testing.T) {
	ok := json.RawMessage(`{"numero":{"value":"F-1","confidence":0.9,"source_snippet":"F-1"}}`)
	results := []Result{
		{Page: 1, Failed: true, Error: "boom"},
		{Page: 2, JSON: ok},
	}

	got := MergePages(results, 0.7)

	if got.Failed {
		t.Fatalf("Failed = true, want false (one page succeeded): %+v", got)
	}
	if string(got.JSON) != string(ok) {
		t.Errorf("JSON = %s, want %s", got.JSON, ok)
	}
}

func TestMergePages_NoPages_ReturnsFailed(t *testing.T) {
	got := MergePages(nil, 0.7)
	if !got.Failed {
		t.Error("Failed = false, want true for an empty page list")
	}
}

func TestMergePages_NeedsReviewWhenMergedFieldStillLowConfidence(t *testing.T) {
	// Aucune page n'a une bonne confiance pour total_ttc : le champ fusionné
	// reste sous le seuil, needs_review doit rester vrai.
	page1 := json.RawMessage(`{"total_ttc": {"value": 0, "confidence": 0.2, "source_snippet": "?"}}`)
	page2 := json.RawMessage(`{"total_ttc": {"value": 0, "confidence": 0.1, "source_snippet": "?"}}`)
	results := []Result{{Page: 1, JSON: page1}, {Page: 2, JSON: page2}}

	got := MergePages(results, 0.7)

	if !got.NeedsReview {
		t.Error("NeedsReview = false, want true (best available confidence is still below threshold)")
	}
	if len(got.LowConfidenceFields) != 1 || got.LowConfidenceFields[0] != "total_ttc" {
		t.Errorf("LowConfidenceFields = %v, want [total_ttc]", got.LowConfidenceFields)
	}
}

func TestMergePages_NestedObjectsMergedRecursively(t *testing.T) {
	page1 := json.RawMessage(`{"adresse": {"ville": {"value": "Paris", "confidence": 1.0, "source_snippet": "Paris"}, "code_postal": {"value": "", "confidence": 0, "source_snippet": ""}}}`)
	page2 := json.RawMessage(`{"adresse": {"ville": {"value": "", "confidence": 0, "source_snippet": ""}, "code_postal": {"value": "75001", "confidence": 1.0, "source_snippet": "75001"}}}`)
	results := []Result{{Page: 1, JSON: page1}, {Page: 2, JSON: page2}}

	got := MergePages(results, 0.7)

	var merged map[string]map[string]map[string]any
	if err := json.Unmarshal(got.JSON, &merged); err != nil {
		t.Fatalf("merged JSON is invalid: %v", err)
	}
	if merged["adresse"]["ville"]["value"] != "Paris" {
		t.Errorf("adresse.ville.value = %v, want Paris", merged["adresse"]["ville"]["value"])
	}
	if merged["adresse"]["code_postal"]["value"] != "75001" {
		t.Errorf("adresse.code_postal.value = %v, want 75001", merged["adresse"]["code_postal"]["value"])
	}
}
