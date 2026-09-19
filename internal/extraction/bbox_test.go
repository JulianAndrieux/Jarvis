package extraction

import (
	"encoding/json"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/bbox"
)

func TestAttachBBoxes_AttachesFoundBBox(t *testing.T) {
	raw := json.RawMessage(`{"numero":{"value":"F-1","confidence":0.9,"source_snippet":"F-1"}}`)
	words := []bbox.Word{{Text: "F-1", XMin: 10, YMin: 20, XMax: 30, YMax: 40}}

	got, err := AttachBBoxes(raw, words)
	if err != nil {
		t.Fatalf("AttachBBoxes() error = %v, want nil", err)
	}

	var decoded map[string]map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	b, ok := decoded["numero"]["bbox"].(map[string]any)
	if !ok {
		t.Fatalf("numero.bbox missing or wrong type: %+v", decoded["numero"])
	}
	if b["x_min"] != 10.0 || b["y_min"] != 20.0 || b["x_max"] != 30.0 || b["y_max"] != 40.0 {
		t.Errorf("bbox = %+v, want {10 20 30 40}", b)
	}

	// Les champs d'origine doivent rester intacts.
	if decoded["numero"]["value"] != "F-1" {
		t.Errorf("value = %v, want F-1 (unchanged)", decoded["numero"]["value"])
	}
}

func TestAttachBBoxes_NoMatch_LeavesFieldWithoutBBox(t *testing.T) {
	raw := json.RawMessage(`{"numero":{"value":"F-1","confidence":0.9,"source_snippet":"introuvable"}}`)
	words := []bbox.Word{{Text: "autre-chose", XMin: 0, YMin: 0, XMax: 1, YMax: 1}}

	got, err := AttachBBoxes(raw, words)
	if err != nil {
		t.Fatalf("AttachBBoxes() error = %v, want nil", err)
	}

	var decoded map[string]map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["numero"]["bbox"]; ok {
		t.Errorf("numero.bbox = %v, want absent when no match is found", decoded["numero"]["bbox"])
	}
}

func TestAttachBBoxes_NestedAndArrayFields(t *testing.T) {
	raw := json.RawMessage(`{
		"adresse": {"ville": {"value":"Paris","confidence":0.9,"source_snippet":"Paris"}},
		"lignes": [{"value":"A","confidence":0.9,"source_snippet":"A"}]
	}`)
	words := []bbox.Word{
		{Text: "Paris", XMin: 1, YMin: 2, XMax: 3, YMax: 4},
		{Text: "A", XMin: 5, YMin: 6, XMax: 7, YMax: 8},
	}

	got, err := AttachBBoxes(raw, words)
	if err != nil {
		t.Fatalf("AttachBBoxes() error = %v, want nil", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	adresse := decoded["adresse"].(map[string]any)
	ville := adresse["ville"].(map[string]any)
	if _, ok := ville["bbox"]; !ok {
		t.Errorf("adresse.ville.bbox missing: %+v", ville)
	}
	lignes := decoded["lignes"].([]any)
	ligne0 := lignes[0].(map[string]any)
	if _, ok := ligne0["bbox"]; !ok {
		t.Errorf("lignes[0].bbox missing: %+v", ligne0)
	}
}

func TestAttachBBoxes_MalformedJSON_ReturnsError(t *testing.T) {
	_, err := AttachBBoxes(json.RawMessage(`not json`), nil)
	if err == nil {
		t.Fatal("AttachBBoxes() error = nil, want non-nil for malformed JSON")
	}
}

func TestAttachBBoxes_EmptyWords_NoOp(t *testing.T) {
	raw := json.RawMessage(`{"numero":{"value":"F-1","confidence":0.9,"source_snippet":"F-1"}}`)

	got, err := AttachBBoxes(raw, nil)
	if err != nil {
		t.Fatalf("AttachBBoxes() error = %v, want nil", err)
	}
	var decoded map[string]map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["numero"]["bbox"]; ok {
		t.Error("bbox attached despite no words available")
	}
}
