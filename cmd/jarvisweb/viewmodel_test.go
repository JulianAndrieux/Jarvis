package main

import (
	"encoding/json"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/extraction"
	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

func TestBuildResultView_NilResult_ReturnsZeroValue(t *testing.T) {
	got := buildResultView(webapp.Job{})
	if len(got.Pages) != 0 || got.DocType != "" {
		t.Errorf("buildResultView(no result) = %+v, want zero value", got)
	}
}

func TestBuildResultView_FlattensFieldsAndSortsByPage(t *testing.T) {
	job := webapp.Job{
		DocType: "facture",
		Result: &pipeline.Result{
			Triage: triage.Result{Score: 1, HasTextLayer: true},
			Extraction: []extraction.Result{
				{Page: 2, JSON: json.RawMessage(`{"numero":{"value":"F-2","confidence":0.9,"source_snippet":"F-2"}}`)},
				{Page: 1, JSON: json.RawMessage(`{"numero":{"value":"F-1","confidence":0.5,"source_snippet":"F-1"}}`), NeedsReview: true},
			},
		},
	}

	got := buildResultView(job)

	if got.DocType != "facture" || got.TriageScore != 1 || !got.HasTextLayer {
		t.Errorf("view summary = %+v, want DocType=facture TriageScore=1 HasTextLayer=true", got)
	}
	if len(got.Pages) != 2 || got.Pages[0].Page != 1 || got.Pages[1].Page != 2 {
		t.Fatalf("Pages = %+v, want pages in order [1 2]", got.Pages)
	}
	if !got.Pages[0].NeedsReview {
		t.Error("Pages[0].NeedsReview = false, want true")
	}
	if len(got.Pages[0].Fields) != 1 || got.Pages[0].Fields[0].Value != "F-1" {
		t.Errorf("Pages[0].Fields = %+v, want [{numero F-1 ...}]", got.Pages[0].Fields)
	}
}

func TestBuildResultView_FailedPage_NoFields(t *testing.T) {
	job := webapp.Job{
		Result: &pipeline.Result{
			Extraction: []extraction.Result{{Page: 1, Failed: true, Error: "llm boom"}},
		},
	}

	got := buildResultView(job)

	if !got.Pages[0].Failed || got.Pages[0].Error != "llm boom" {
		t.Errorf("Pages[0] = %+v, want Failed=true Error='llm boom'", got.Pages[0])
	}
	if len(got.Pages[0].Fields) != 0 {
		t.Errorf("Pages[0].Fields = %v, want empty for a failed page", got.Pages[0].Fields)
	}
}

func TestFlattenExtractionJSON_NestedAndBBox(t *testing.T) {
	raw := json.RawMessage(`{
		"adresse": {"ville": {"value":"Paris","confidence":0.8,"source_snippet":"Paris","bbox":{"x_min":1,"y_min":2,"x_max":3,"y_max":4}}},
		"lignes": [{"value":"A","confidence":0.9,"source_snippet":"A"}]
	}`)

	got := flattenExtractionJSON(raw)

	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2: %+v", len(got), got)
	}
	// Triés par nom : "adresse.ville" < "lignes[0]"
	if got[0].Name != "adresse.ville" || got[0].Value != "Paris" {
		t.Errorf("got[0] = %+v, want Name=adresse.ville Value=Paris", got[0])
	}
	if got[0].BBox == "" {
		t.Error("got[0].BBox is empty, want a formatted bbox string")
	}
	if got[1].Name != "lignes[0]" || got[1].Value != "A" {
		t.Errorf("got[1] = %+v, want Name=lignes[0] Value=A", got[1])
	}
	if got[1].BBox != "" {
		t.Errorf("got[1].BBox = %q, want empty (no bbox in source)", got[1].BBox)
	}
}

func TestFlattenExtractionJSON_MalformedJSON_ReturnsNil(t *testing.T) {
	got := flattenExtractionJSON(json.RawMessage(`not json`))
	if got != nil {
		t.Errorf("flattenExtractionJSON(malformed) = %v, want nil", got)
	}
}
