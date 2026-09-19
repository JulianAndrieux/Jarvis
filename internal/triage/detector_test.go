package triage

import (
	"context"
	"errors"
	"testing"
)

func TestDetector_Detect_DelegatesToExtractorAndScores(t *testing.T) {
	extractor := FakeExtractor{Pages: []PageText{
		{Page: 1, Text: "Facture n. 2026-0042 Fournisseur: Acme SARL Total: 123.45 EUR"},
	}}
	d := Detector{Extractor: extractor, Thresholds: DefaultThresholds()}

	got, err := d.Detect(context.Background(), "unused-path.pdf")
	if err != nil {
		t.Fatalf("Detect() error = %v, want nil", err)
	}
	if !got.HasTextLayer {
		t.Errorf("HasTextLayer = false, want true")
	}
}

func TestDetector_Detect_PropagatesExtractorError(t *testing.T) {
	wantErr := errors.New("boom")
	extractor := FakeExtractor{Err: wantErr}
	d := Detector{Extractor: extractor, Thresholds: DefaultThresholds()}

	_, err := d.Detect(context.Background(), "unused-path.pdf")
	if err == nil {
		t.Fatal("Detect() error = nil, want non-nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("Detect() error = %v, want it to wrap %v", err, wantErr)
	}
}

func TestDetector_Detect_UsesDefaultThresholdsWhenZeroValue(t *testing.T) {
	extractor := FakeExtractor{Pages: []PageText{
		{Page: 1, Text: "Facture n. 2026-0042 Fournisseur: Acme SARL Total: 123.45 EUR"},
	}}
	d := Detector{Extractor: extractor} // Thresholds non renseigné.

	got, err := d.Detect(context.Background(), "unused-path.pdf")
	if err != nil {
		t.Fatalf("Detect() error = %v, want nil", err)
	}
	if !got.HasTextLayer {
		t.Error("HasTextLayer = false, want true when zero-value Thresholds falls back to defaults")
	}
}
