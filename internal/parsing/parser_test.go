package parsing

import (
	"context"
	"errors"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/vlm"
)

func TestParser_ParsePages_Success(t *testing.T) {
	renderer := FakeRenderer{PNG: map[int][]byte{
		2: []byte("png-page-2"),
		3: []byte("png-page-3"),
	}}
	vlmClient := &vlm.FakeClient{Results: map[int]vlm.ParseResult{
		2: {Markdown: "## Page 2", Model: vlm.ModelInfo{Name: "test-vlm", Version: "1"}},
		3: {Markdown: "## Page 3", Model: vlm.ModelInfo{Name: "test-vlm", Version: "1"}},
	}}
	p := Parser{Renderer: renderer, VLM: vlmClient}

	got, err := p.ParsePages(context.Background(), "doc.pdf", []int{2, 3})
	if err != nil {
		t.Fatalf("ParsePages() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(got))
	}
	if got[0].Page != 2 || got[0].Failed || got[0].Markdown != "## Page 2" {
		t.Errorf("results[0] = %+v, want page 2 succeeded with markdown '## Page 2'", got[0])
	}
	if got[1].Page != 3 || got[1].Failed || got[1].Markdown != "## Page 3" {
		t.Errorf("results[1] = %+v, want page 3 succeeded with markdown '## Page 3'", got[1])
	}

	// La page rendue doit être celle transmise au VLM.
	if len(vlmClient.Calls) != 2 || string(vlmClient.Calls[0].PNG) != "png-page-2" {
		t.Errorf("VLM calls = %+v, want the rendered PNG bytes to be forwarded", vlmClient.Calls)
	}
}

func TestParser_ParsePages_RenderFailure_MarksPageFailedAndContinues(t *testing.T) {
	renderer := FakeRenderer{
		PNG: map[int][]byte{3: []byte("png-page-3")},
		Err: errors.New("render boom"), // s'applique à toute page absente de PNG
	}
	vlmClient := &vlm.FakeClient{Results: map[int]vlm.ParseResult{
		3: {Markdown: "## Page 3"},
	}}
	p := Parser{Renderer: renderer, VLM: vlmClient}

	got, err := p.ParsePages(context.Background(), "doc.pdf", []int{2, 3})
	if err != nil {
		t.Fatalf("ParsePages() error = %v, want nil (per-page failures should not abort)", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(got))
	}
	if !got[0].Failed || got[0].Error == "" {
		t.Errorf("results[0] = %+v, want Failed=true with a non-empty Error", got[0])
	}
	if got[1].Failed {
		t.Errorf("results[1] = %+v, want page 3 to have succeeded despite page 2 failing", got[1])
	}

	// La page en échec de rendu ne doit jamais atteindre le VLM.
	if len(vlmClient.Calls) != 1 || vlmClient.Calls[0].Page != 3 {
		t.Errorf("VLM calls = %+v, want only page 3 to have reached the VLM", vlmClient.Calls)
	}
}

func TestParser_ParsePages_VLMFailure_MarksPageFailedAndContinues(t *testing.T) {
	renderer := FakeRenderer{PNG: map[int][]byte{2: []byte("a"), 3: []byte("b")}}
	vlmClient := &vlm.FakeClient{
		Results: map[int]vlm.ParseResult{3: {Markdown: "## Page 3"}},
		Err:     nil,
	}
	// Page 2 n'a pas de résultat configuré => FakeClient renvoie une erreur pour elle.
	delete(vlmClient.Results, 2)

	p := Parser{Renderer: renderer, VLM: vlmClient}

	got, err := p.ParsePages(context.Background(), "doc.pdf", []int{2, 3})
	if err != nil {
		t.Fatalf("ParsePages() error = %v, want nil", err)
	}
	if !got[0].Failed || got[0].Error == "" {
		t.Errorf("results[0] = %+v, want Failed=true (vlm error) with a non-empty Error", got[0])
	}
	if got[1].Failed {
		t.Errorf("results[1] = %+v, want page 3 to have succeeded", got[1])
	}
}

func TestParser_ParsePages_NoPages_ReturnsEmpty(t *testing.T) {
	p := Parser{Renderer: FakeRenderer{}, VLM: &vlm.FakeClient{}}

	got, err := p.ParsePages(context.Background(), "doc.pdf", nil)
	if err != nil {
		t.Fatalf("ParsePages() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("ParsePages() = %v, want empty", got)
	}
}

func TestParser_ParsePages_CanceledContext_ReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := Parser{
		Renderer: FakeRenderer{PNG: map[int][]byte{1: []byte("a")}},
		VLM:      &vlm.FakeClient{Results: map[int]vlm.ParseResult{1: {}}},
	}

	_, err := p.ParsePages(ctx, "doc.pdf", []int{1})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ParsePages() error = %v, want it to wrap context.Canceled", err)
	}
}

func TestParser_ParsePages_DefaultDPI(t *testing.T) {
	renderer := &recordingRenderer{png: []byte("x")}
	p := Parser{Renderer: renderer, VLM: &vlm.FakeClient{Results: map[int]vlm.ParseResult{1: {}}}}

	if _, err := p.ParsePages(context.Background(), "doc.pdf", []int{1}); err != nil {
		t.Fatal(err)
	}
	if renderer.lastDPI != 200 {
		t.Errorf("DPI used = %d, want default 200", renderer.lastDPI)
	}
}

// recordingRenderer capture le DPI transmis, pour vérifier la valeur par défaut.
type recordingRenderer struct {
	png     []byte
	lastDPI int
}

func (r *recordingRenderer) RenderPage(ctx context.Context, path string, page int, dpi int) ([]byte, error) {
	r.lastDPI = dpi
	return r.png, nil
}
