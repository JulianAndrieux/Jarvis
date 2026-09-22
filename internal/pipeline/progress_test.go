package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/classify"
	"github.com/JulianAndrieux/Jarvis/internal/doctype"
	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
	"github.com/JulianAndrieux/Jarvis/internal/vlm"
)

// mixedProgressPipeline : page 1 native, pages 2 et 3 via le VLM, page 3
// en échec VLM. Classifiée "facture".
func mixedProgressPipeline() Pipeline {
	extracted := json.RawMessage(`{"numero": {"value": "F-1", "confidence": 0.9, "source_snippet": "F-1"}}`)
	return Pipeline{
		TextExtractor: triage.FakeExtractor{Pages: []triage.PageText{
			{Page: 1, Text: "Facture F-1 - " + longEnoughText()},
			{Page: 2, Text: ""},
			{Page: 3, Text: ""},
		}},
		Renderer:   fakeRenderer{png: []byte("png")},
		VLM:        &vlm.FakeClient{Results: map[int]vlm.ParseResult{2: {Markdown: "texte VLM page 2"}}},
		LLM:        &llm.FakeClient{Results: map[int]llm.ExtractResult{1: {JSON: extracted}, 2: {JSON: extracted}}},
		Classifier: &classify.FakeClassifier{Result: classify.Result{DocType: "facture", Confidence: 0.9}},
		Registry:   doctype.NewDefaultRegistry(),
	}
}

func TestPipeline_RunAuto_ReportsProgressAfterEachStepAndPage(t *testing.T) {
	var events []Progress
	_, err := mixedProgressPipeline().RunAuto(context.Background(), "doc.pdf", func(p Progress) { events = append(events, p) })
	if err != nil {
		t.Fatalf("RunAuto() error = %v", err)
	}

	type step struct {
		stage                  Stage
		parseDone, extractDone int
		pages, failures        int
	}
	want := []step{
		{StageParsing, 0, 0, 1, 0},     // triage fait : page native déjà lisible
		{StageParsing, 1, 0, 2, 0},     // page 2 lue par le VLM
		{StageParsing, 2, 0, 2, 1},     // page 3 en échec VLM
		{StageClassifying, 2, 0, 2, 1}, //
		{StageExtracting, 2, 0, 2, 1},  //
		{StageExtracting, 2, 1, 2, 1},  // page 1 extraite
		{StageExtracting, 2, 2, 2, 1},  // page 2 extraite
	}
	if len(events) != len(want) {
		t.Fatalf("got %d progress events, want %d: %+v", len(events), len(want), events)
	}
	for i, w := range want {
		e := events[i]
		got := step{e.Stage, e.ParseDone, e.ExtractDone, len(e.Pages), len(e.ParseFailures)}
		if got != w {
			t.Errorf("event %d = %+v, want %+v", i, got, w)
		}
		if e.PageCount != 3 || e.ParseTotal != 2 {
			t.Errorf("event %d: PageCount=%d ParseTotal=%d, want 3 and 2", i, e.PageCount, e.ParseTotal)
		}
	}
	if events[4].ExtractTotal != 2 {
		t.Errorf("ExtractTotal = %d, want 2 (page 3 has no text to extract)", events[4].ExtractTotal)
	}

	// Le texte déjà disponible est celui qu'on enregistrera : natif puis VLM, trié par page.
	last := events[len(events)-1]
	if last.Pages[0].Source != SourceNative || last.Pages[1].Text != "texte VLM page 2" || last.Pages[1].Source != SourceVLM {
		t.Errorf("Pages = %+v, want native page 1 then VLM page 2", last.Pages)
	}
	if last.ParseFailures[0].Page != 3 || last.ParseFailures[0].Error == "" {
		t.Errorf("ParseFailures = %+v, want page 3 with its error", last.ParseFailures)
	}
}

// Chaque événement est un instantané : le destinataire peut le garder
// (l'écrire en base plus tard) sans le voir changer sous ses pieds.
func TestPipeline_RunAuto_ProgressEventsAreIndependentSnapshots(t *testing.T) {
	var events []Progress
	mixedProgressPipeline().RunAuto(context.Background(), "doc.pdf", func(p Progress) { events = append(events, p) })

	if len(events[0].Pages) != 1 {
		t.Errorf("first event now has %d pages, want 1 — later events must not mutate earlier snapshots", len(events[0].Pages))
	}
}

func TestPipeline_RunWithType_ReportsProgressWithoutClassificationStep(t *testing.T) {
	var stages []Stage
	_, err := mixedProgressPipeline().RunWithType(context.Background(), "facture", "doc.pdf", func(p Progress) { stages = append(stages, p.Stage) })
	if err != nil {
		t.Fatalf("RunWithType() error = %v", err)
	}
	for _, s := range stages {
		if s == StageClassifying {
			t.Fatalf("stages = %v, RunWithType must not report a classification step (type imposed)", stages)
		}
	}
	if stages[len(stages)-1] != StageExtracting {
		t.Errorf("last stage = %s, want extraction", stages[len(stages)-1])
	}
}
