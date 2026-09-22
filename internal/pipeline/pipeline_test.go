package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/doctype"
	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
	"github.com/JulianAndrieux/Jarvis/internal/vlm"
)

func factureRegistration(t *testing.T) doctype.Registration {
	t.Helper()
	reg, ok := doctype.NewDefaultRegistry().Get("facture")
	if !ok {
		t.Fatal(`doctype registry has no "facture" registration`)
	}
	return reg
}

func TestPipeline_Run_NativeOnly_SkipsVLM(t *testing.T) {
	extracted := json.RawMessage(`{
		"numero": {"value": "F-1", "confidence": 0.9, "source_snippet": "F-1"},
		"fournisseur": {"value": "Acme", "confidence": 0.9, "source_snippet": "Acme"},
		"total_ttc": {"value": 1.0, "confidence": 0.9, "source_snippet": "1.0"}
	}`)
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{
		{Page: 1, Text: "Facture F-1, Acme, 1.0 EUR - " + longEnoughText()},
	}}
	vlmClient := &vlm.FakeClient{} // ne doit jamais être appelé
	llmClient := &llm.FakeClient{Results: map[int]llm.ExtractResult{1: {JSON: extracted}}}

	p := Pipeline{
		TextExtractor: textExtractor,
		Renderer:      nil, // ne doit jamais être utilisé non plus
		VLM:           vlmClient,
		LLM:           llmClient,
	}

	got, err := p.Run(context.Background(), factureRegistration(t), "doc.pdf")
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	if !got.Triage.HasTextLayer {
		t.Errorf("Triage.HasTextLayer = false, want true")
	}
	if len(got.Parsing) != 0 {
		t.Errorf("Parsing = %v, want empty (no page needed the VLM)", got.Parsing)
	}
	if len(vlmClient.Calls) != 0 {
		t.Errorf("VLM.Calls = %v, want empty (VLM should never be called)", vlmClient.Calls)
	}
	if len(got.Extraction) != 1 || got.Extraction[0].Failed {
		t.Fatalf("Extraction = %+v, want 1 successful result", got.Extraction)
	}
	if !strings.Contains(got.SearchText, "Facture F-1") {
		t.Errorf("SearchText = %q, want it to contain the page's text (jalon 18, recherche dans les documents)", got.SearchText)
	}
}

func TestPipeline_Run_UnusablePage_GoesThroughVLM(t *testing.T) {
	extracted := json.RawMessage(`{
		"numero": {"value": "F-2", "confidence": 0.9, "source_snippet": "F-2"},
		"fournisseur": {"value": "Acme", "confidence": 0.9, "source_snippet": "Acme"},
		"total_ttc": {"value": 2.0, "confidence": 0.9, "source_snippet": "2.0"}
	}`)
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{{Page: 1, Text: ""}}}
	renderer := fakeRenderer{png: []byte("png-bytes")}
	vlmClient := &vlm.FakeClient{Results: map[int]vlm.ParseResult{
		1: {Markdown: "Facture F-2, Acme, 2.0 EUR"},
	}}
	llmClient := &llm.FakeClient{Results: map[int]llm.ExtractResult{1: {JSON: extracted}}}

	p := Pipeline{
		TextExtractor: textExtractor,
		Renderer:      renderer,
		VLM:           vlmClient,
		LLM:           llmClient,
	}

	got, err := p.Run(context.Background(), factureRegistration(t), "doc.pdf")
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	if got.Triage.HasTextLayer {
		t.Errorf("Triage.HasTextLayer = true, want false")
	}
	if len(got.Parsing) != 1 || got.Parsing[0].Failed {
		t.Fatalf("Parsing = %+v, want 1 successful result", got.Parsing)
	}
	if len(vlmClient.Calls) != 1 {
		t.Errorf("VLM.Calls = %v, want 1 call", vlmClient.Calls)
	}
	if len(got.Extraction) != 1 || got.Extraction[0].Failed {
		t.Fatalf("Extraction = %+v, want 1 successful result", got.Extraction)
	}
	if string(got.Extraction[0].JSON) != string(extracted) {
		t.Errorf("Extraction[0].JSON = %s, want %s", got.Extraction[0].JSON, extracted)
	}
}

func TestPipeline_Run_FailedVLMPage_SurfacedAsFailedInExtraction(t *testing.T) {
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{{Page: 1, Text: ""}}}
	renderer := fakeRenderer{err: errors.New("render boom")}
	vlmClient := &vlm.FakeClient{}
	llmClient := &llm.FakeClient{}

	p := Pipeline{
		TextExtractor: textExtractor,
		Renderer:      renderer,
		VLM:           vlmClient,
		LLM:           llmClient,
	}

	got, err := p.Run(context.Background(), factureRegistration(t), "doc.pdf")
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if len(got.Parsing) != 1 || !got.Parsing[0].Failed {
		t.Fatalf("Parsing = %+v, want 1 failed result", got.Parsing)
	}
	// La page ne doit pas disparaître silencieusement de Extraction : elle
	// y figure marquée Failed, avec la raison VLM, plutôt que d'être
	// simplement absente sans aucun signal (cf. withVLMFailures).
	if len(got.Extraction) != 1 || !got.Extraction[0].Failed {
		t.Fatalf("Extraction = %+v, want 1 Failed result surfacing the VLM error", got.Extraction)
	}
	if len(llmClient.Calls) != 0 {
		t.Errorf("LLM.Calls = %v, want empty (LLM should never be called for an unusable page)", llmClient.Calls)
	}
}

func TestPipeline_Run_TriageExtractorError_ReturnsError(t *testing.T) {
	textExtractor := triage.FakeExtractor{Err: errors.New("pdftotext boom")}
	p := Pipeline{TextExtractor: textExtractor, VLM: &vlm.FakeClient{}, LLM: &llm.FakeClient{}}

	_, err := p.Run(context.Background(), factureRegistration(t), "doc.pdf")
	if err == nil {
		t.Fatal("Run() error = nil, want non-nil when triage extraction fails")
	}
}

// longEnoughText pousse le texte au-dessus du seuil MinCharsPerPage par
// défaut du triage, pour que la page soit bien jugée "usable" dans ces
// tests.
func longEnoughText() string {
	s := ""
	for i := 0; i < 5; i++ {
		s += "du texte tout à fait ordinaire et lisible. "
	}
	return s
}

type fakeRenderer struct {
	png []byte
	err error
}

func (f fakeRenderer) RenderPage(ctx context.Context, path string, page int, dpi int) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.png, nil
}

// --- Concurrency (jalon 21, puis VLM/LLM séparés) : vérifie que
// Pipeline.VLMConcurrency et Pipeline.LLMConcurrency sont bien transmis
// chacun à son étage (Parsing/VLM et Extraction/LLM respectivement), de
// façon indépendante — pas seulement documentés sur les champs.

type maxConcurrencyLLM struct {
	limit   int32
	current int32
	max     int32
}

func (m *maxConcurrencyLLM) Extract(ctx context.Context, req llm.ExtractRequest) (llm.ExtractResult, error) {
	cur := atomic.AddInt32(&m.current, 1)
	defer atomic.AddInt32(&m.current, -1)
	for {
		old := atomic.LoadInt32(&m.max)
		if cur <= old || atomic.CompareAndSwapInt32(&m.max, old, cur) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	if cur > m.limit {
		return llm.ExtractResult{}, fmt.Errorf("concurrency limit exceeded: %d > %d", cur, m.limit)
	}
	return llm.ExtractResult{JSON: json.RawMessage(`{
		"numero": {"value": "F", "confidence": 0.9, "source_snippet": "F"},
		"fournisseur": {"value": "Acme", "confidence": 0.9, "source_snippet": "Acme"},
		"total_ttc": {"value": 1.0, "confidence": 0.9, "source_snippet": "1.0"}
	}`)}, nil
}

func TestPipeline_Run_ConcurrencyThreadedToExtraction(t *testing.T) {
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{
		{Page: 1, Text: "page 1 - " + longEnoughText()},
		{Page: 2, Text: "page 2 - " + longEnoughText()},
		{Page: 3, Text: "page 3 - " + longEnoughText()},
	}}
	llmClient := &maxConcurrencyLLM{limit: 3}

	p := Pipeline{TextExtractor: textExtractor, VLM: &vlm.FakeClient{}, LLM: llmClient, LLMConcurrency: 3}

	got, err := p.Run(context.Background(), factureRegistration(t), "doc.pdf")
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	for _, r := range got.Extraction {
		if r.Failed {
			t.Errorf("page %d failed: %s (Pipeline.LLMConcurrency not respected)", r.Page, r.Error)
		}
	}
	if atomic.LoadInt32(&llmClient.max) < 2 {
		t.Errorf("max observed LLM concurrency = %d, want >= 2 (Pipeline.LLMConcurrency was not threaded to extraction.Extractor)", llmClient.max)
	}
}

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
		if cur <= old || atomic.CompareAndSwapInt32(&m.max, old, cur) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	if cur > m.limit {
		return vlm.ParseResult{}, fmt.Errorf("concurrency limit exceeded: %d > %d", cur, m.limit)
	}
	return vlm.ParseResult{Markdown: fmt.Sprintf("page-%d", img.Page)}, nil
}

func TestPipeline_Run_ConcurrencyThreadedToParsing(t *testing.T) {
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{
		{Page: 1, Text: ""}, {Page: 2, Text: ""}, {Page: 3, Text: ""},
	}}
	vlmClient := &maxConcurrencyVLM{limit: 3}
	llmClient := &llm.FakeClient{Results: map[int]llm.ExtractResult{
		1: {JSON: json.RawMessage(`{}`)}, 2: {JSON: json.RawMessage(`{}`)}, 3: {JSON: json.RawMessage(`{}`)},
	}}

	p := Pipeline{
		TextExtractor:  textExtractor,
		Renderer:       fakeRenderer{png: []byte("x")},
		VLM:            vlmClient,
		LLM:            llmClient,
		VLMConcurrency: 3,
	}

	got, err := p.Run(context.Background(), factureRegistration(t), "doc.pdf")
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	for _, r := range got.Parsing {
		if r.Failed {
			t.Errorf("page %d failed: %s (Pipeline.VLMConcurrency not respected)", r.Page, r.Error)
		}
	}
	if atomic.LoadInt32(&vlmClient.max) < 2 {
		t.Errorf("max observed VLM concurrency = %d, want >= 2 (Pipeline.VLMConcurrency was not threaded to parsing.Parser)", vlmClient.max)
	}
}

func TestPipeline_Run_VLMAndLLMConcurrencyAreIndependent(t *testing.T) {
	// VLMConcurrency=1 (séquentiel) mais LLMConcurrency=3 (parallèle) :
	// prouve que les deux champs ne sont pas liés — c'est exactement la
	// configuration recommandée après le retest sur documents réels (VLM
	// séquentiel, LLM parallèle), donc la plus importante à couvrir.
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{
		{Page: 1, Text: ""}, {Page: 2, Text: ""}, {Page: 3, Text: ""},
	}}
	vlmClient := &maxConcurrencyVLM{limit: 1}
	llmClient := &maxConcurrencyLLM{limit: 3}

	p := Pipeline{
		TextExtractor:  textExtractor,
		Renderer:       fakeRenderer{png: []byte("x")},
		VLM:            vlmClient,
		LLM:            llmClient,
		VLMConcurrency: 1,
		LLMConcurrency: 3,
	}

	got, err := p.Run(context.Background(), factureRegistration(t), "doc.pdf")
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	for _, r := range got.Parsing {
		if r.Failed {
			t.Errorf("parsing page %d failed: %s", r.Page, r.Error)
		}
	}
	for _, r := range got.Extraction {
		if r.Failed {
			t.Errorf("extraction page %d failed: %s", r.Page, r.Error)
		}
	}
	if atomic.LoadInt32(&vlmClient.max) > 1 {
		t.Errorf("max observed VLM concurrency = %d, want <= 1 (VLMConcurrency=1 should stay sequential regardless of LLMConcurrency)", vlmClient.max)
	}
	if atomic.LoadInt32(&llmClient.max) < 2 {
		t.Errorf("max observed LLM concurrency = %d, want >= 2 (LLMConcurrency=3 should parallelize regardless of VLMConcurrency)", llmClient.max)
	}
}

// TestPipeline_Run_VLMFailureOnOnePage_StillSurfacedInExtraction reproduit
// en bout en bout (pas seulement au niveau de withVLMFailures) le vrai
// problème trouvé en retestant sur un document scanné réel : une page
// dont le parsing VLM échoue disparaissait purement et simplement de
// Result.Extraction, avec la page suivante ré-numérotée comme si de rien
// n'était — aucun signal visible dans la sortie CLI ni l'UI web, malgré
// l'erreur réelle déjà connue de internal/parsing.
func TestPipeline_Run_VLMFailureOnOnePage_StillSurfacedInExtraction(t *testing.T) {
	textExtractor := triage.FakeExtractor{Pages: []triage.PageText{
		{Page: 1, Text: ""}, {Page: 2, Text: ""},
	}}
	vlmClient := &vlm.FakeClient{Results: map[int]vlm.ParseResult{
		1: {Markdown: "page 1 ok"},
		// Page 2 volontairement absente : ParsePage échoue pour elle.
	}}
	llmClient := &llm.FakeClient{Results: map[int]llm.ExtractResult{
		1: {JSON: json.RawMessage(`{}`)},
		2: {JSON: json.RawMessage(`{}`)},
	}}

	p := Pipeline{
		TextExtractor: textExtractor,
		Renderer:      fakeRenderer{png: []byte("x")},
		VLM:           vlmClient,
		LLM:           llmClient,
	}

	got, err := p.Run(context.Background(), factureRegistration(t), "doc.pdf")
	if err != nil {
		t.Fatalf("Run() error = %v, want nil (une page en échec VLM ne doit pas faire échouer tout le document)", err)
	}

	if len(got.Extraction) != 2 {
		t.Fatalf("len(Extraction) = %d, want 2 (page 2 ne doit pas disparaître silencieusement)", len(got.Extraction))
	}
	if got.Extraction[0].Page != 1 || got.Extraction[0].Failed {
		t.Errorf("Extraction[0] = %+v, want page 1 successful", got.Extraction[0])
	}
	if got.Extraction[1].Page != 2 || !got.Extraction[1].Failed {
		t.Errorf("Extraction[1] = %+v, want page 2 marked Failed", got.Extraction[1])
	}
	if got.Extraction[1].Error == "" {
		t.Error("Extraction[1].Error is empty, want the VLM failure reason surfaced")
	}

	for _, call := range llmClient.Calls {
		if call.Page == 2 {
			t.Error("LLM was called for page 2, want it skipped (pas de contenu exploitable, jamais de valeur inventée)")
		}
	}
}
