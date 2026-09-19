package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/extraction"
	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/parsing"
	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
	"github.com/JulianAndrieux/Jarvis/internal/vlm"
)

var fixedTime = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

func TestBuildRecords_NativePage(t *testing.T) {
	result := pipeline.Result{
		Path: "doc.pdf",
		Triage: triage.Result{
			Score:        1,
			HasTextLayer: true,
			Pages:        []triage.PageResult{{Page: 1, Usable: true}},
		},
		Extraction: []extraction.Result{
			{Page: 1, JSON: json.RawMessage(`{"a":1}`), Model: llm.ModelInfo{Name: "qwen3-8b", Version: "Q5"}, Prompt: "p"},
		},
	}

	doc, pages := BuildRecords("HASH123", "doc.pdf", "facture", fixedTime, result)

	if doc.SourceHash != "HASH123" || doc.SourcePath != "doc.pdf" || doc.DocType != "facture" {
		t.Errorf("doc = %+v, want matching hash/path/doctype", doc)
	}
	if doc.TriageScore != 1 || !doc.HasTextLayer {
		t.Errorf("doc triage summary = %+v, want score=1 hasTextLayer=true", doc)
	}
	if len(pages) != 1 {
		t.Fatalf("len(pages) = %d, want 1", len(pages))
	}
	p := pages[0]
	if p.Page != 1 || p.Source != SourceNative {
		t.Errorf("page = %+v, want Page=1 Source=native", p)
	}
	if p.Parsing != nil {
		t.Errorf("page.Parsing = %+v, want nil (native page never goes through the VLM)", p.Parsing)
	}
	if p.Extraction == nil || string(p.Extraction.JSON) != `{"a":1}` {
		t.Fatalf("page.Extraction = %+v, want the extraction result attached", p.Extraction)
	}
	if p.Extraction.Model != "qwen3-8b" || p.Extraction.ModelVersion != "Q5" {
		t.Errorf("page.Extraction model = %+v, want qwen3-8b/Q5", p.Extraction)
	}
}

func TestBuildRecords_VLMPage(t *testing.T) {
	result := pipeline.Result{
		Path: "doc.pdf",
		Triage: triage.Result{
			Pages: []triage.PageResult{{Page: 1, Usable: false}},
		},
		Parsing: []parsing.PageResult{
			{Page: 1, Markdown: "# Titre", Model: vlm.ModelInfo{Name: "olmocr2", Version: "Q6"}, Prompt: "pp"},
		},
		Extraction: []extraction.Result{
			{Page: 1, JSON: json.RawMessage(`{"a":2}`)},
		},
	}

	_, pages := BuildRecords("H", "doc.pdf", "facture", fixedTime, result)

	if len(pages) != 1 {
		t.Fatalf("len(pages) = %d, want 1", len(pages))
	}
	p := pages[0]
	if p.Source != SourceVLM {
		t.Errorf("page.Source = %q, want vlm", p.Source)
	}
	if p.Parsing == nil || p.Parsing.Markdown != "# Titre" {
		t.Fatalf("page.Parsing = %+v, want the markdown attached", p.Parsing)
	}
	if p.Parsing.Model != "olmocr2" || p.Parsing.ModelVersion != "Q6" {
		t.Errorf("page.Parsing model = %+v, want olmocr2/Q6", p.Parsing)
	}
}

func TestBuildRecords_FailedVLMPage_NoExtraction(t *testing.T) {
	result := pipeline.Result{
		Path: "doc.pdf",
		Triage: triage.Result{
			Pages: []triage.PageResult{{Page: 1, Usable: false}},
		},
		Parsing: []parsing.PageResult{
			{Page: 1, Failed: true, Error: "vlm timeout"},
		},
		// Pas d'extraction pour cette page : le pipeline ne l'a jamais tentée.
	}

	_, pages := BuildRecords("H", "doc.pdf", "facture", fixedTime, result)

	if len(pages) != 1 {
		t.Fatalf("len(pages) = %d, want 1", len(pages))
	}
	p := pages[0]
	if p.Parsing == nil || !p.Parsing.Failed || p.Parsing.Error != "vlm timeout" {
		t.Errorf("page.Parsing = %+v, want Failed=true Error='vlm timeout'", p.Parsing)
	}
	if p.Extraction != nil {
		t.Errorf("page.Extraction = %+v, want nil (no usable content reached extraction)", p.Extraction)
	}
}

func TestBuildRecords_NeedsReviewPropagated(t *testing.T) {
	result := pipeline.Result{
		Path: "doc.pdf",
		Triage: triage.Result{
			Pages: []triage.PageResult{{Page: 1, Usable: true}},
		},
		Extraction: []extraction.Result{
			{Page: 1, JSON: json.RawMessage(`{}`), NeedsReview: true, LowConfidenceFields: []string{"total_ttc"}},
		},
	}

	_, pages := BuildRecords("H", "doc.pdf", "facture", fixedTime, result)

	if !pages[0].Extraction.NeedsReview {
		t.Error("Extraction.NeedsReview = false, want true")
	}
	if len(pages[0].Extraction.LowConfidenceFields) != 1 || pages[0].Extraction.LowConfidenceFields[0] != "total_ttc" {
		t.Errorf("LowConfidenceFields = %v, want [total_ttc]", pages[0].Extraction.LowConfidenceFields)
	}
}

func TestBuildRecords_MultiplePages_SortedByPageNumber(t *testing.T) {
	result := pipeline.Result{
		Path: "doc.pdf",
		Triage: triage.Result{
			Pages: []triage.PageResult{
				{Page: 3, Usable: true},
				{Page: 1, Usable: true},
				{Page: 2, Usable: false},
			},
		},
		Parsing: []parsing.PageResult{{Page: 2, Markdown: "m2"}},
	}

	doc, pages := BuildRecords("H", "doc.pdf", "facture", fixedTime, result)

	if len(pages) != 3 {
		t.Fatalf("len(pages) = %d, want 3", len(pages))
	}
	for i, want := range []int{1, 2, 3} {
		if pages[i].Page != want {
			t.Errorf("pages[%d].Page = %d, want %d", i, pages[i].Page, want)
		}
	}
	if len(doc.Pages) != 3 {
		t.Errorf("doc.Pages = %v, want [1 2 3]", doc.Pages)
	}
}

func TestBuildRecords_ProcessedAtStamped(t *testing.T) {
	result := pipeline.Result{Path: "doc.pdf"}
	doc, _ := BuildRecords("H", "doc.pdf", "facture", fixedTime, result)
	if !doc.ProcessedAt.Equal(fixedTime) {
		t.Errorf("doc.ProcessedAt = %v, want %v", doc.ProcessedAt, fixedTime)
	}
}
