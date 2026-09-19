//go:build integration

// Dépend du binaire externe `pdftotext` — voir
// internal/triage/extractor_integration_test.go pour la justification du
// build tag.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestRun_Triage_NativePDF_ReportsHasTextLayer(t *testing.T) {
	var stdout, stderr bytes.Buffer
	path := filepath.Join("..", "..", "testdata", "fixtures", "native.pdf")

	err := run(context.Background(), []string{"triage", path}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}

	var got triageOutput
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (stdout=%q)", err, stdout.String())
	}
	if !got.HasTextLayer {
		t.Errorf("HasTextLayer = false, want true for a native-text PDF (got %+v)", got)
	}
}

func TestRun_Triage_ScannedPDF_ReportsNoTextLayer(t *testing.T) {
	var stdout, stderr bytes.Buffer
	path := filepath.Join("..", "..", "testdata", "fixtures", "scanned.pdf")

	err := run(context.Background(), []string{"triage", path}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}

	var got triageOutput
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (stdout=%q)", err, stdout.String())
	}
	if got.HasTextLayer {
		t.Errorf("HasTextLayer = true, want false for a scanned/image-only PDF (got %+v)", got)
	}
}
