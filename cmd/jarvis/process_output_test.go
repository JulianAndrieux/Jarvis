package main

import (
	"encoding/json"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/extraction"
	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
)

func TestToProcessOutput_IncludesMergedExtraction(t *testing.T) {
	mergedJSON := json.RawMessage(`{"numero":{"value":"2026-0271","confidence":1,"source_snippet":"..."}}`)
	r := pipeline.Result{
		Path: "doc.pdf",
		Merged: extraction.MergedResult{
			JSON:        mergedJSON,
			Model:       llm.ModelInfo{Name: "qwen3-8b", Version: "Q5"},
			Prompt:      "p",
			NeedsReview: true,
		},
	}

	got := toProcessOutput("facture", r)

	if string(got.Merged.JSON) != string(mergedJSON) {
		t.Errorf("Merged.JSON = %s, want %s", got.Merged.JSON, mergedJSON)
	}
	if got.Merged.Model != "qwen3-8b" || !got.Merged.NeedsReview {
		t.Errorf("Merged = %+v, want Model=qwen3-8b NeedsReview=true", got.Merged)
	}
}
