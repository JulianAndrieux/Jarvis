package parsing

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

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

// maxConcurrencyVLM piste le nombre d'appels ParsePage réellement en vol
// simultanément (via un compteur atomique) et échoue si ce nombre dépasse
// limit — prouve à la fois qu'un vrai parallélisme a lieu (max >= 2) et
// que la borne est respectée (jamais > limit). Le petit sleep laisse aux
// autres goroutines la chance de démarrer avant que l'appel ne retourne.
type maxConcurrencyVLM struct {
	limit   int32
	current int32
	max     int32
}

func (m *maxConcurrencyVLM) ParsePage(ctx context.Context, img vlm.PageImage) (vlm.ParseResult, error) {
	cur := atomic.AddInt32(&m.current, 1)
	defer atomic.AddInt32(&m.current, -1)
	for {
		old := atomic.LoadInt32(&m.max)
		if cur <= old {
			break
		}
		if atomic.CompareAndSwapInt32(&m.max, old, cur) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	if cur > m.limit {
		return vlm.ParseResult{}, fmt.Errorf("concurrency limit exceeded: %d > %d", cur, m.limit)
	}
	return vlm.ParseResult{Markdown: fmt.Sprintf("page-%d", img.Page)}, nil
}

func TestParser_ParsePages_Concurrency0Or1_ProcessesSequentially(t *testing.T) {
	tracker := &maxConcurrencyVLM{limit: 1}
	p := Parser{Renderer: FakeRenderer{PNG: map[int][]byte{1: []byte("a"), 2: []byte("b"), 3: []byte("c")}}, VLM: tracker}

	got, err := p.ParsePages(context.Background(), "doc.pdf", []int{1, 2, 3})
	if err != nil {
		t.Fatalf("ParsePages() error = %v, want nil (default Concurrency must stay sequential)", err)
	}
	for _, r := range got {
		if r.Failed {
			t.Errorf("page %d failed: %s (concurrency limit exceeded even at default)", r.Page, r.Error)
		}
	}
}

func TestParser_ParsePages_Concurrency_ProcessesInParallelWithinBound(t *testing.T) {
	tracker := &maxConcurrencyVLM{limit: 2}
	p := Parser{
		Renderer:    FakeRenderer{PNG: map[int][]byte{1: []byte("a"), 2: []byte("b"), 3: []byte("c"), 4: []byte("d")}},
		VLM:         tracker,
		Concurrency: 2,
	}

	got, err := p.ParsePages(context.Background(), "doc.pdf", []int{1, 2, 3, 4})
	if err != nil {
		t.Fatalf("ParsePages() error = %v, want nil", err)
	}
	if len(got) != 4 {
		t.Fatalf("len(results) = %d, want 4", len(got))
	}
	for _, r := range got {
		if r.Failed {
			t.Errorf("page %d failed: %s (concurrency limit of 2 was exceeded)", r.Page, r.Error)
		}
	}
	if atomic.LoadInt32(&tracker.max) < 2 {
		t.Errorf("max observed concurrency = %d, want >= 2 (pages were not actually processed in parallel)", tracker.max)
	}

	// L'ordre des résultats doit rester celui des pages demandées malgré
	// le traitement parallèle.
	for i, r := range got {
		if r.Page != i+1 {
			t.Errorf("results[%d].Page = %d, want %d (order must be preserved)", i, r.Page, i+1)
		}
	}
}

func TestParser_ParsePages_Concurrency_CanceledContext_ReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := Parser{
		Renderer:    FakeRenderer{PNG: map[int][]byte{1: []byte("a")}},
		VLM:         &vlm.FakeClient{Results: map[int]vlm.ParseResult{1: {}}},
		Concurrency: 4,
	}

	_, err := p.ParsePages(ctx, "doc.pdf", []int{1})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ParsePages() error = %v, want it to wrap context.Canceled", err)
	}
}

// Jalon 23 : OnPage est appelé dès qu'une page est terminée — c'est ce
// qui permet d'enregistrer le texte OCR au fur et à mesure au lieu
// d'attendre la fin du document (~3-4 min par page dense).
func TestParser_OnPage_Sequential_CalledAfterEachPageBeforeTheNextStarts(t *testing.T) {
	renderer := FakeRenderer{PNG: map[int][]byte{1: []byte("a"), 3: []byte("c")}, Err: errors.New("render boom")}
	vlmClient := &vlm.FakeClient{Results: map[int]vlm.ParseResult{1: {Markdown: "p1"}, 3: {Markdown: "p3"}}}

	var got []PageResult
	var vlmCallsAtCallback []int
	p := Parser{Renderer: renderer, VLM: vlmClient, OnPage: func(r PageResult) {
		got = append(got, r)
		vlmCallsAtCallback = append(vlmCallsAtCallback, len(vlmClient.Calls))
	}}

	if _, err := p.ParsePages(context.Background(), "doc.pdf", []int{1, 2, 3}); err != nil {
		t.Fatalf("ParsePages() error = %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("OnPage called %d times, want 3 (one per page, failures included)", len(got))
	}
	if got[0].Page != 1 || got[0].Markdown != "p1" || !got[1].Failed || got[1].Page != 2 || got[2].Markdown != "p3" {
		t.Errorf("OnPage results = %+v, want p1, page 2 failed, p3 in order", got)
	}
	// Page 2 échoue au rendu (pas d'appel VLM) : 1, 1, 2 appels VLM au
	// moment des trois rappels — chaque rappel arrive avant la page suivante.
	if want := []int{1, 1, 2}; fmt.Sprint(vlmCallsAtCallback) != fmt.Sprint(want) {
		t.Errorf("VLM calls at each callback = %v, want %v", vlmCallsAtCallback, want)
	}
}

// En parallèle, les rappels sont sérialisés par le Parser : l'appelant
// (qui écrit en base) n'a pas à être sûr en concurrence.
func TestParser_OnPage_Parallel_CalledOncePerPageNeverConcurrently(t *testing.T) {
	var inCallback, overlaps int32
	seen := map[int]int{}
	p := Parser{
		Renderer:    FakeRenderer{PNG: map[int][]byte{1: []byte("a"), 2: []byte("b"), 3: []byte("c"), 4: []byte("d")}},
		VLM:         &maxConcurrencyVLM{limit: 4},
		Concurrency: 4,
		OnPage: func(r PageResult) {
			if atomic.AddInt32(&inCallback, 1) > 1 {
				atomic.AddInt32(&overlaps, 1)
			}
			seen[r.Page]++ // map non protégée : -race détecterait un appel concurrent
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt32(&inCallback, -1)
		},
	}

	if _, err := p.ParsePages(context.Background(), "doc.pdf", []int{1, 2, 3, 4}); err != nil {
		t.Fatalf("ParsePages() error = %v", err)
	}
	if overlaps != 0 {
		t.Errorf("OnPage ran concurrently %d time(s), want never", overlaps)
	}
	for page := 1; page <= 4; page++ {
		if seen[page] != 1 {
			t.Errorf("OnPage called %d time(s) for page %d, want 1", seen[page], page)
		}
	}
}
