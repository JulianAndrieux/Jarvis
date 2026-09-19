package parsing

import (
	"context"
	"errors"
	"testing"
)

func TestFakeRenderer_ReturnsConfiguredPNG(t *testing.T) {
	r := FakeRenderer{PNG: map[int][]byte{1: []byte("png-bytes")}}

	got, err := r.RenderPage(context.Background(), "doc.pdf", 1, 200)
	if err != nil {
		t.Fatalf("RenderPage() error = %v, want nil", err)
	}
	if string(got) != "png-bytes" {
		t.Errorf("RenderPage() = %q, want %q", got, "png-bytes")
	}
}

func TestFakeRenderer_ConfiguredError(t *testing.T) {
	wantErr := errors.New("boom")
	r := FakeRenderer{Err: wantErr}

	_, err := r.RenderPage(context.Background(), "doc.pdf", 1, 200)
	if !errors.Is(err, wantErr) {
		t.Errorf("RenderPage() error = %v, want %v", err, wantErr)
	}
}

func TestFakeRenderer_UnconfiguredPage_ReturnsError(t *testing.T) {
	r := FakeRenderer{PNG: map[int][]byte{1: []byte("x")}}

	_, err := r.RenderPage(context.Background(), "doc.pdf", 99, 200)
	if err == nil {
		t.Fatal("RenderPage() error = nil, want non-nil for an unconfigured page")
	}
}
