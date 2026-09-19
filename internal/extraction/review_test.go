package extraction

import (
	"encoding/json"
	"testing"
)

func TestLowConfidenceFields_AllAboveThreshold_ReturnsEmpty(t *testing.T) {
	raw := json.RawMessage(`{
		"numero": {"value": "F-1", "confidence": 0.95, "source_snippet": "F-1"},
		"total_ttc": {"value": 123.45, "confidence": 0.9, "source_snippet": "123.45"}
	}`)

	got, err := LowConfidenceFields(raw, 0.7)
	if err != nil {
		t.Fatalf("LowConfidenceFields() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("LowConfidenceFields() = %v, want empty", got)
	}
}

func TestLowConfidenceFields_OneFieldBelowThreshold(t *testing.T) {
	raw := json.RawMessage(`{
		"numero": {"value": "F-1", "confidence": 0.95, "source_snippet": "F-1"},
		"total_ttc": {"value": 123.45, "confidence": 0.4, "source_snippet": "123.45"}
	}`)

	got, err := LowConfidenceFields(raw, 0.7)
	if err != nil {
		t.Fatalf("LowConfidenceFields() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != "total_ttc" {
		t.Errorf("LowConfidenceFields() = %v, want [total_ttc]", got)
	}
}

func TestLowConfidenceFields_NestedObject(t *testing.T) {
	raw := json.RawMessage(`{
		"adresse": {
			"ville": {"value": "Paris", "confidence": 0.3, "source_snippet": "Paris"}
		}
	}`)

	got, err := LowConfidenceFields(raw, 0.7)
	if err != nil {
		t.Fatalf("LowConfidenceFields() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != "adresse.ville" {
		t.Errorf("LowConfidenceFields() = %v, want [adresse.ville]", got)
	}
}

func TestLowConfidenceFields_ArrayOfObjects(t *testing.T) {
	raw := json.RawMessage(`{
		"lignes": [
			{"value": "A", "confidence": 0.9, "source_snippet": "A"},
			{"value": "B", "confidence": 0.2, "source_snippet": "B"}
		]
	}`)

	got, err := LowConfidenceFields(raw, 0.7)
	if err != nil {
		t.Fatalf("LowConfidenceFields() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != "lignes[1]" {
		t.Errorf("LowConfidenceFields() = %v, want [lignes[1]]", got)
	}
}

func TestLowConfidenceFields_ExactlyAtThreshold_IsAccepted(t *testing.T) {
	raw := json.RawMessage(`{"numero": {"value": "F-1", "confidence": 0.7, "source_snippet": "F-1"}}`)

	got, err := LowConfidenceFields(raw, 0.7)
	if err != nil {
		t.Fatalf("LowConfidenceFields() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("LowConfidenceFields() = %v, want empty (0.7 meets threshold 0.7)", got)
	}
}

func TestLowConfidenceFields_MalformedJSON_ReturnsError(t *testing.T) {
	_, err := LowConfidenceFields(json.RawMessage(`not json`), 0.7)
	if err == nil {
		t.Fatal("LowConfidenceFields() error = nil, want non-nil for malformed JSON")
	}
}

func TestLowConfidenceFields_EmptyObject_ReturnsEmpty(t *testing.T) {
	got, err := LowConfidenceFields(json.RawMessage(`{}`), 0.7)
	if err != nil {
		t.Fatalf("LowConfidenceFields() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("LowConfidenceFields() = %v, want empty", got)
	}
}
