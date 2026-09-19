package pipeline

import (
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/parsing"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
)

func TestMerge_UsablePageUsesNativeText(t *testing.T) {
	native := []triage.PageText{{Page: 1, Text: "texte natif"}}
	triageResult := triage.Result{Pages: []triage.PageResult{{Page: 1, Usable: true}}}

	got := Merge(native, triageResult, nil)

	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0] != (PageContent{Page: 1, Text: "texte natif", Source: SourceNative}) {
		t.Errorf("got[0] = %+v, want native text", got[0])
	}
}

func TestMerge_UnusablePageUsesVLMMarkdown(t *testing.T) {
	native := []triage.PageText{{Page: 1, Text: ""}}
	triageResult := triage.Result{Pages: []triage.PageResult{{Page: 1, Usable: false}}}
	parsed := []parsing.PageResult{{Page: 1, Markdown: "# Titre"}}

	got := Merge(native, triageResult, parsed)

	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0] != (PageContent{Page: 1, Text: "# Titre", Source: SourceVLM}) {
		t.Errorf("got[0] = %+v, want VLM markdown", got[0])
	}
}

func TestMerge_FailedVLMPage_IsExcluded(t *testing.T) {
	native := []triage.PageText{{Page: 1, Text: ""}}
	triageResult := triage.Result{Pages: []triage.PageResult{{Page: 1, Usable: false}}}
	parsed := []parsing.PageResult{{Page: 1, Failed: true, Error: "vlm timeout"}}

	got := Merge(native, triageResult, parsed)

	if len(got) != 0 {
		t.Errorf("Merge() = %v, want empty (failed VLM page has no usable content)", got)
	}
}

func TestMerge_UnusablePageWithNoParsingAttempt_IsExcluded(t *testing.T) {
	native := []triage.PageText{{Page: 1, Text: ""}}
	triageResult := triage.Result{Pages: []triage.PageResult{{Page: 1, Usable: false}}}

	got := Merge(native, triageResult, nil)

	if len(got) != 0 {
		t.Errorf("Merge() = %v, want empty", got)
	}
}

func TestMerge_MixedDocument_PreservesPageOrder(t *testing.T) {
	native := []triage.PageText{
		{Page: 1, Text: "page 1 native"},
		{Page: 2, Text: ""},
		{Page: 3, Text: "page 3 native"},
	}
	triageResult := triage.Result{Pages: []triage.PageResult{
		{Page: 1, Usable: true},
		{Page: 2, Usable: false},
		{Page: 3, Usable: true},
	}}
	parsed := []parsing.PageResult{{Page: 2, Markdown: "page 2 vlm"}}

	got := Merge(native, triageResult, parsed)

	want := []PageContent{
		{Page: 1, Text: "page 1 native", Source: SourceNative},
		{Page: 2, Text: "page 2 vlm", Source: SourceVLM},
		{Page: 3, Text: "page 3 native", Source: SourceNative},
	}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestMerge_NoPages_ReturnsEmpty(t *testing.T) {
	got := Merge(nil, triage.Result{}, nil)
	if len(got) != 0 {
		t.Errorf("Merge() = %v, want empty", got)
	}
}
