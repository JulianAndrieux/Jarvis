package llm

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestFakeClient_ReturnsConfiguredResultForPage(t *testing.T) {
	want := ExtractResult{
		JSON:   json.RawMessage(`{"numero":{"value":"F-1","confidence":0.9,"source_snippet":"F-1"}}`),
		Model:  ModelInfo{Name: "test-llm", Version: "0.0.0"},
		Prompt: "extract per schema",
	}
	f := &FakeClient{Results: map[int]ExtractResult{1: want}}

	got, err := f.Extract(context.Background(), ExtractRequest{Page: 1, Text: "Facture F-1"})
	if err != nil {
		t.Fatalf("Extract() error = %v, want nil", err)
	}
	if string(got.JSON) != string(want.JSON) || got.Model != want.Model || got.Prompt != want.Prompt {
		t.Errorf("Extract() = %+v, want %+v", got, want)
	}
}

func TestFakeClient_UnconfiguredPage_ReturnsError(t *testing.T) {
	f := &FakeClient{Results: map[int]ExtractResult{1: {}}}

	_, err := f.Extract(context.Background(), ExtractRequest{Page: 5, Text: "x"})
	if err == nil {
		t.Fatal("Extract() error = nil, want non-nil for a page with no configured result")
	}
}

func TestFakeClient_ConfiguredError_TakesPrecedence(t *testing.T) {
	wantErr := errors.New("boom")
	f := &FakeClient{
		Results: map[int]ExtractResult{1: {JSON: json.RawMessage(`{}`)}},
		Err:     wantErr,
	}

	_, err := f.Extract(context.Background(), ExtractRequest{Page: 1, Text: "x"})
	if !errors.Is(err, wantErr) {
		t.Errorf("Extract() error = %v, want %v", err, wantErr)
	}
}

func TestFakeClient_RecordsCalls(t *testing.T) {
	f := &FakeClient{Results: map[int]ExtractResult{1: {}, 2: {}}}

	req1 := ExtractRequest{Page: 1, Text: "a", Schema: json.RawMessage(`{}`), Prompt: "p1"}
	req2 := ExtractRequest{Page: 2, Text: "b", Schema: json.RawMessage(`{}`), Prompt: "p2"}
	if _, err := f.Extract(context.Background(), req1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Extract(context.Background(), req2); err != nil {
		t.Fatal(err)
	}

	if len(f.Calls) != 2 || f.Calls[0].Text != "a" || f.Calls[1].Text != "b" {
		t.Errorf("Calls = %+v, want [%+v %+v]", f.Calls, req1, req2)
	}
}
