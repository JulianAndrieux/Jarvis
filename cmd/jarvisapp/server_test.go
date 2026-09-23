package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/codemap"
	"github.com/JulianAndrieux/Jarvis/internal/doctype"
	"github.com/JulianAndrieux/Jarvis/internal/extraction"
	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/parsing"
	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
	"github.com/JulianAndrieux/Jarvis/internal/testmap"
	"github.com/JulianAndrieux/Jarvis/internal/testrunner"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
	"github.com/JulianAndrieux/Jarvis/internal/vlm"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

// --- Upload / suivi de documents (portés depuis l'ex-cmd/jarvisweb,
// adaptés à la classification automatique du jalon 15) ---

type blockingRunner struct {
	result  pipeline.Result
	err     error
	proceed chan struct{}
}

func (r *blockingRunner) RunAuto(ctx context.Context, path string, onProgress pipeline.ProgressFunc) (pipeline.Result, error) {
	if r.proceed != nil {
		<-r.proceed
	}
	return r.result, r.err
}

func (r *blockingRunner) RunWithType(ctx context.Context, docType, path string, onProgress pipeline.ProgressFunc) (pipeline.Result, error) {
	if r.proceed != nil {
		<-r.proceed
	}
	return r.result, r.err
}

func newTestServer(t *testing.T, runner webapp.Runner) (*Server, *webapp.FakeStore) {
	t.Helper()
	fakeStore := webapp.NewFakeStore()
	jobs := webapp.NewJobManager(fakeStore, runner)
	jobs.WorkDir = t.TempDir()
	s := &Server{Jobs: jobs, Registry: doctype.NewDefaultRegistry()}
	return s, fakeStore
}

func multipartUpload(t *testing.T, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, w.FormDataContentType()
}

func TestHandleIndex_MentionsRecognizedDocTypes(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	s.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "facture") {
		t.Errorf("body does not mention the registered doc type %q: %s", "facture", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "<select") {
		t.Errorf("body still has a doc-type selector, want none (classification is automatic): %s", rec.Body.String())
	}
}

func TestHandleSubmit_ValidUpload_ReturnsRunningFragment(t *testing.T) {
	proceed := make(chan struct{})
	s, fakeStore := newTestServer(t, &blockingRunner{proceed: proceed})
	defer close(proceed)

	body, contentType := multipartUpload(t, "doc.pdf", []byte("%PDF-1.4 fake"))
	req := httptest.NewRequest(http.MethodPost, "/jobs", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	s.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "doc.pdf") {
		t.Errorf("body does not mention the filename: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hx-get") {
		t.Errorf("body has no polling attribute for a running job: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Enregistré dans MongoDB") {
		t.Errorf("body does not confirm persistence: %s", rec.Body.String())
	}

	id := extractJobID(t, rec.Body.String())
	// Jalon 25 : le fichier est stocké à part (FileOriginal), plus dans le job.
	stored, ok, err := fakeStore.ReadFile(context.Background(), id, webapp.FileOriginal)
	if err != nil || !ok {
		t.Fatalf("fakeStore.ReadFile(%s) = ok %v, err %v", id, ok, err)
	}
	if string(stored) != "%PDF-1.4 fake" {
		t.Errorf("stored content = %q, want the uploaded bytes", stored)
	}
}

func TestHandleJobStatus_UnknownID_ReturnsNotFound(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})

	req := httptest.NewRequest(http.MethodGet, "/jobs/does-not-exist", nil)
	rec := httptest.NewRecorder()

	s.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandleJobStatus_DoneJob_ClassifiedType_RendersResultWithoutPolling(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{result: pipeline.Result{
		DocType: "facture",
		Triage:  pipeline.Result{}.Triage,
	}})

	body, contentType := multipartUpload(t, "doc.pdf", []byte("x"))
	req := httptest.NewRequest(http.MethodPost, "/jobs", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)

	id := extractJobID(t, rec.Body.String())

	deadline := time.Now().Add(2 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/jobs/"+id, nil)
		rec := httptest.NewRecorder()
		s.Routes().ServeHTTP(rec, req)
		last = rec.Body.String()
		if strings.Contains(last, "status-done") {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	if !strings.Contains(last, "status-done") {
		t.Fatalf("job never reached status-done in time, last body: %s", last)
	}
	if strings.Contains(last, "hx-get") {
		t.Errorf("done fragment still carries a polling attribute: %s", last)
	}
	if !strings.Contains(last, "facture") {
		t.Errorf("done fragment does not mention the classified doc type: %s", last)
	}
}

func TestHandleJobStatus_DoneJob_UnclassifiedType_ShowsNoTypeRecognized(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{result: pipeline.Result{
		DocType:                  "",
		ClassificationConfidence: 0.2,
	}})

	body, contentType := multipartUpload(t, "doc.pdf", []byte("x"))
	req := httptest.NewRequest(http.MethodPost, "/jobs", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	id := extractJobID(t, rec.Body.String())

	deadline := time.Now().Add(2 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/jobs/"+id, nil)
		rec := httptest.NewRecorder()
		s.Routes().ServeHTTP(rec, req)
		last = rec.Body.String()
		if strings.Contains(last, "status-done") {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	if !strings.Contains(last, "Aucun type de document reconnu") {
		t.Errorf("body does not explain that no type was recognized: %s", last)
	}
	if strings.Contains(last, "hx-get") {
		t.Errorf("done fragment still carries a polling attribute: %s", last)
	}
}

func TestHandleJobStatus_FailedJob_ShowsError(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{err: errors.New("pipeline boom")})

	body, contentType := multipartUpload(t, "doc.pdf", []byte("x"))
	req := httptest.NewRequest(http.MethodPost, "/jobs", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	id := extractJobID(t, rec.Body.String())

	deadline := time.Now().Add(2 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/jobs/"+id, nil)
		rec := httptest.NewRecorder()
		s.Routes().ServeHTTP(rec, req)
		last = rec.Body.String()
		if strings.Contains(last, "pipeline boom") {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	if !strings.Contains(last, "pipeline boom") {
		t.Fatalf("error message not found in failed job fragment: %s", last)
	}
}

// extractJobID récupère l'identifiant de job depuis un fragment rendu, en
// repérant l'attribut hx-get="/jobs/<id>".
func extractJobID(t *testing.T, html string) string {
	t.Helper()
	const marker = `hx-get="/jobs/`
	i := strings.Index(html, marker)
	if i < 0 {
		t.Fatalf("no hx-get=\"/jobs/...\" found in: %s", html)
	}
	rest := html[i+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatalf("malformed hx-get attribute in: %s", html)
	}
	return rest[:end]
}

// --- Navigateur de classes/tests (portés depuis l'ex-cmd/codebrowser) ---
//
// Le modèle/les catégories sont injectés directement (pas de vrai
// Refresh/Analyze ici) : internal/codemap et internal/testmap ont déjà
// leurs propres tests contre le vrai module (coûteux, ~8s). Ici on ne
// teste que le câblage propre à jarvisapp (routes, rendu, cache de
// résultats de tests).

func newClassesTestServer() *Server {
	return &Server{
		model: &codemap.Model{Packages: []*codemap.Package{
			{
				Path: "example.com/fixture/widget",
				Name: "widget",
				Types: []*codemap.TypeInfo{
					{
						Name: "Gadget", Package: "example.com/fixture/widget", Kind: codemap.KindStruct, Exported: true,
						Fields:  []codemap.FieldInfo{{Name: "Label", Type: "string"}},
						Methods: []codemap.MethodInfo{{Name: "String", Signature: "func() string"}},
						Embeds:  []codemap.TypeRef{{Package: "example.com/fixture/widget", Name: "Base"}},
					},
					{
						Name: "Base", Package: "example.com/fixture/widget", Kind: codemap.KindStruct, Exported: true,
						EmbeddedBy: []codemap.TypeRef{{Package: "example.com/fixture/widget", Name: "Gadget"}},
					},
				},
			},
		}},
		categories: []testmap.Category{
			{Package: "example.com/fixture/widget", Tests: []testmap.TestFunc{
				{Name: "TestGadget_String", File: "widget/gadget_test.go", Line: 7},
			}},
		},
		results: map[string]testrunner.TestResult{},
	}
}

func TestHandleClasses_ListsPackagesAndTypes(t *testing.T) {
	s := newClassesTestServer()
	req := httptest.NewRequest(http.MethodGet, "/classes", nil)
	rec := httptest.NewRecorder()

	s.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Gadget") {
		t.Errorf("body does not list the Gadget type: %s", rec.Body.String())
	}
}

func TestHandleClasses_SelectedType_ShowsEmbedRelation(t *testing.T) {
	s := newClassesTestServer()
	req := httptest.NewRequest(http.MethodGet, "/classes?pkg=example.com/fixture/widget&name=Gadget", nil)
	rec := httptest.NewRecorder()

	s.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Base") {
		t.Errorf("body does not show the embedded Base type: %s", rec.Body.String())
	}
}

func TestHandleClassDetail_UnknownType_ReturnsNotFound(t *testing.T) {
	s := newClassesTestServer()
	req := httptest.NewRequest(http.MethodGet, "/classes/detail?pkg=example.com/fixture/widget&name=DoesNotExist", nil)
	rec := httptest.NewRecorder()

	s.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandleTests_ListsCategoriesAndTests(t *testing.T) {
	s := newClassesTestServer()
	req := httptest.NewRequest(http.MethodGet, "/tests", nil)
	rec := httptest.NewRecorder()

	s.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "TestGadget_String") {
		t.Errorf("body does not list the known test: %s", rec.Body.String())
	}
}

// --- handleTestsRun : une vraie invocation de `go test` sur un fixture
// synthétique, pour valider le câblage query-params -> testrunner.Options
// -> mise en cache des résultats (pas une re-vérification de
// internal/testrunner lui-même, déjà testé en isolation).

func TestHandleTestsRun_SinglePackage_UpdatesCachedResults(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "go.mod"), "module example.com/fixture\n\ngo 1.25\n")
	mustWriteFile(t, filepath.Join(dir, "pkg", "x_test.go"), `package pkg

import "testing"

func TestOK(t *testing.T) {}
`)

	s := &Server{
		ModuleDir:  dir,
		modulePath: "example.com/fixture",
		categories: []testmap.Category{
			{Package: "example.com/fixture/pkg", Tests: []testmap.TestFunc{{Name: "TestOK", File: "pkg/x_test.go", Line: 5}}},
		},
		results: map[string]testrunner.TestResult{},
	}

	req := httptest.NewRequest(http.MethodPost, "/tests/run?pkg=example.com/fixture/pkg", nil)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "status-pass") {
		t.Errorf("body does not show a pass status: %s", rec.Body.String())
	}

	// Le cache doit maintenant refléter ce résultat sur /tests aussi.
	req2 := httptest.NewRequest(http.MethodGet, "/tests", nil)
	rec2 := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec2, req2)
	if !strings.Contains(rec2.Body.String(), "status-pass") {
		t.Errorf("/tests does not reflect the cached pass result: %s", rec2.Body.String())
	}
}

// --- Bibliothèque de documents (jalon 17) ---

func TestHandleDocuments_ListsSubmittedJobs(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{result: pipeline.Result{DocType: "facture"}})
	body, contentType := multipartUpload(t, "facture-a.pdf", []byte("x"))
	req := httptest.NewRequest(http.MethodPost, "/jobs", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	id := extractJobID(t, rec.Body.String())
	waitForJobDone(t, s, id)

	req2 := httptest.NewRequest(http.MethodGet, "/documents", nil)
	rec2 := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "facture-a.pdf") {
		t.Errorf("body does not list the submitted document: %s", rec2.Body.String())
	}
}

func TestHandleDocuments_FiltersBySearchQuery(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})
	for _, name := range []string{"alpha.pdf", "beta.pdf"} {
		body, contentType := multipartUpload(t, name, []byte("x"))
		req := httptest.NewRequest(http.MethodPost, "/jobs", body)
		req.Header.Set("Content-Type", contentType)
		rec := httptest.NewRecorder()
		s.Routes().ServeHTTP(rec, req)
	}

	req := httptest.NewRequest(http.MethodGet, "/documents?q=alpha", nil)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "alpha.pdf") {
		t.Errorf("body does not list alpha.pdf: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "beta.pdf") {
		t.Errorf("body lists beta.pdf, want it filtered out by the search query: %s", rec.Body.String())
	}
}

func TestHandleDocumentDetail_RendersJobAndPreviewLink(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{result: pipeline.Result{DocType: "facture"}})
	body, contentType := multipartUpload(t, "doc.pdf", []byte("%PDF-1.4"))
	req := httptest.NewRequest(http.MethodPost, "/jobs", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	id := extractJobID(t, rec.Body.String())
	waitForJobDone(t, s, id)

	req2 := httptest.NewRequest(http.MethodGet, "/documents/"+id, nil)
	rec2 := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "/documents/"+id+"/pdf") {
		t.Errorf("body does not link to the PDF preview: %s", rec2.Body.String())
	}
}

func TestHandleDocumentDetail_UnknownID_ReturnsNotFound(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})
	req := httptest.NewRequest(http.MethodGet, "/documents/does-not-exist", nil)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandleDocumentPDF_ServesRawContent(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})
	body, contentType := multipartUpload(t, "doc.pdf", []byte("%PDF-1.4 le contenu brut"))
	req := httptest.NewRequest(http.MethodPost, "/jobs", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	id := extractJobID(t, rec.Body.String())

	req2 := httptest.NewRequest(http.MethodGet, "/documents/"+id+"/pdf", nil)
	rec2 := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec2.Code)
	}
	if rec2.Header().Get("Content-Type") != "application/pdf" {
		t.Errorf("Content-Type = %q, want application/pdf", rec2.Header().Get("Content-Type"))
	}
	if rec2.Body.String() != "%PDF-1.4 le contenu brut" {
		t.Errorf("body = %q, want the raw PDF content", rec2.Body.String())
	}
}

func TestHandleDocumentTags_UpdatesAndReturnsTagsForm(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})
	body, contentType := multipartUpload(t, "doc.pdf", []byte("x"))
	req := httptest.NewRequest(http.MethodPost, "/jobs", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	id := extractJobID(t, rec.Body.String())

	form := url.Values{"tags": {"urgent, client-x"}}
	req2 := httptest.NewRequest(http.MethodPost, "/documents/"+id+"/tags", strings.NewReader(form.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec2.Code, rec2.Body.String())
	}

	job, ok, err := s.Jobs.Get(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("Get() = %+v, %v, %v", job, ok, err)
	}
	if len(job.Tags) != 2 || job.Tags[0] != "urgent" || job.Tags[1] != "client-x" {
		t.Errorf("job.Tags = %v, want [urgent client-x]", job.Tags)
	}
}

func TestHandleDocumentReprocess_StartsRunningAndReturnsPollingFragment(t *testing.T) {
	proceed := make(chan struct{})
	s, _ := newTestServer(t, &blockingRunner{result: pipeline.Result{DocType: "facture"}, proceed: proceed})
	defer close(proceed)

	body, contentType := multipartUpload(t, "doc.pdf", []byte("x"))
	req := httptest.NewRequest(http.MethodPost, "/jobs", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	id := extractJobID(t, rec.Body.String())

	form := url.Values{"doc_type": {"facture"}}
	req2 := httptest.NewRequest(http.MethodPost, "/documents/"+id+"/reprocess", strings.NewReader(form.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "hx-get") {
		t.Errorf("body has no polling attribute for a running reprocess: %s", rec2.Body.String())
	}
}

func TestHandleDocumentDelete_RemovesDocumentAndRedirects(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})
	body, contentType := multipartUpload(t, "doc.pdf", []byte("x"))
	req := httptest.NewRequest(http.MethodPost, "/jobs", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	id := extractJobID(t, rec.Body.String())

	req2 := httptest.NewRequest(http.MethodDelete, "/documents/"+id, nil)
	rec2 := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec2.Code, rec2.Body.String())
	}
	if rec2.Header().Get("HX-Redirect") != "/documents" {
		t.Errorf("HX-Redirect = %q, want /documents", rec2.Header().Get("HX-Redirect"))
	}

	_, ok, err := s.Jobs.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("document still exists after DELETE, want it removed")
	}
}

func TestHandleDocumentDelete_UnknownID_ReturnsNotFound(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})

	req := httptest.NewRequest(http.MethodDelete, "/documents/does-not-exist", nil)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// waitForJobDone poll /jobs/{id} jusqu'à voir status-done dans le
// fragment rendu — utilisé par les tests de la bibliothèque de
// documents qui ont besoin d'un job déjà terminé.
func waitForJobDone(t *testing.T, s *Server, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/jobs/"+id, nil)
		rec := httptest.NewRecorder()
		s.Routes().ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), "status-done") {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("job %s never reached status-done in time", id)
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- Jalon 22 : refonte de la bibliothèque (miniatures, vue détail en
// deux volets, texte OCR et analyse LLM affichés) ---

type fakePNGRenderer struct{ png []byte }

func (r fakePNGRenderer) RenderPage(ctx context.Context, path string, page, dpi int) ([]byte, error) {
	return r.png, nil
}

// seedDoneJob enregistre directement un job terminé dans le store, sans
// passer par l'upload — les tests de la vue détail portent sur le rendu
// d'un résultat donné, pas sur le cycle de vie du job.
func seedDoneJob(t *testing.T, store *webapp.FakeStore, job webapp.Job) {
	t.Helper()
	job.Status = webapp.StatusDone
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now()
	}
	if _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func mixedResult() *pipeline.Result {
	return &pipeline.Result{
		DocType:                  "devis",
		ClassificationConfidence: 0.99,
		Triage: triage.Result{Score: 0.5, Pages: []triage.PageResult{
			{Page: 1, Usable: true},
			{Page: 2, Usable: false},
		}},
		Pages: []pipeline.PageContent{
			{Page: 1, Text: "BM Constructions S.A — texte natif page un", Source: pipeline.SourceNative},
			{Page: 2, Text: "<table><tr><td>1.2.7</td><td>Terrassement</td></tr></table>", Source: pipeline.SourceVLM},
		},
		Parsing: []parsing.PageResult{
			{Page: 2, Markdown: "<table><tr><td>1.2.7</td><td>Terrassement</td></tr></table>", Model: vlm.ModelInfo{Name: "olmOCR-2-7B-1025", Version: "Q6_K"}},
		},
		Extraction: []extraction.Result{
			{Page: 1, JSON: json.RawMessage(`{"numero": {"value": "160-2026", "confidence": 0.95, "source_snippet": "Devis 160-2026"}}`), Model: llm.ModelInfo{Name: "qwen3-8b", Version: "Q5_K_M"}, Prompt: "Extrais les champs du devis"},
			{Page: 2, JSON: json.RawMessage(`{"numero": {"value": "160-2026", "confidence": 0.5, "source_snippet": "160"}}`), Model: llm.ModelInfo{Name: "qwen3-8b", Version: "Q5_K_M"}, NeedsReview: true},
		},
		Merged: extraction.MergedResult{JSON: json.RawMessage(`{"numero": {"value": "160-2026", "confidence": 0.95, "source_snippet": "Devis 160-2026"}}`)},
	}
}

func TestHandleDocumentThumbnail_ServesRenderedPNG(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	s.Jobs.Renderer = fakePNGRenderer{png: []byte("\x89PNG-thumb")}
	seedDoneJob(t, store, webapp.Job{ID: "d1", Filename: "a.pdf", Content: []byte("%PDF")})

	rec := get(t, s, "/documents/d1/thumbnail")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if rec.Body.String() != "\x89PNG-thumb" {
		t.Errorf("body = %q, want the rendered PNG", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age") {
		t.Errorf("Cache-Control = %q, want a max-age (thumbnails never change for a given document)", cc)
	}
}

func TestHandleDocumentThumbnail_UnknownID_ReturnsNotFound(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})
	s.Jobs.Renderer = fakePNGRenderer{png: []byte("x")}
	if rec := get(t, s, "/documents/nope/thumbnail"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandleDocuments_ShowsThumbnailPerDocument(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	seedDoneJob(t, store, webapp.Job{ID: "d1", Filename: "a.pdf", DocType: "facture"})

	body := get(t, s, "/documents").Body.String()

	if !strings.Contains(body, `src="/documents/d1/thumbnail"`) {
		t.Errorf("documents page has no thumbnail for d1: %s", body)
	}
	if !strings.Contains(body, `loading="lazy"`) {
		t.Errorf("thumbnails should be lazy-loaded (up to %d documents per page): %s", webapp.DefaultListLimit, body)
	}
}

func TestHandleDocumentDetail_TwoPaneLayoutWithPreviewOnTheLeft(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	seedDoneJob(t, store, webapp.Job{ID: "d1", Filename: "devis.pdf", DocType: "devis", Result: mixedResult()})

	body := get(t, s, "/documents/d1").Body.String()

	preview := strings.Index(body, `class="doc-preview"`)
	panel := strings.Index(body, `id="doc-panel"`)
	if preview < 0 || panel < 0 {
		t.Fatalf("detail page lacks doc-preview (%d) or doc-panel (%d): %s", preview, panel, body)
	}
	if preview > panel {
		t.Errorf("preview must come before (left of) the info panel")
	}
	if !strings.Contains(body, "/documents/d1/pdf") {
		t.Errorf("preview does not load the PDF")
	}
}

func TestHandleDocumentDetail_ShowsOCRTextPerPageWithSource(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	seedDoneJob(t, store, webapp.Job{ID: "d1", Filename: "devis.pdf", DocType: "devis", Result: mixedResult()})

	body := get(t, s, "/documents/d1").Body.String()

	for _, want := range []string{
		"BM Constructions S.A — texte natif page un",
		// Le HTML produit par le VLM est affiché comme du texte, jamais
		// interprété : il vient d'un modèle qui lit un PDF arbitraire.
		"&lt;table&gt;&lt;tr&gt;&lt;td&gt;1.2.7",
		"Texte natif",
		"olmOCR-2-7B-1025",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("OCR tab does not contain %q", want)
		}
	}
	if strings.Contains(body, "<td>1.2.7</td>") {
		t.Errorf("VLM output was injected as raw HTML, want it escaped")
	}
}

func TestHandleDocumentDetail_ShowsLLMAnalysisWithProvenance(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	seedDoneJob(t, store, webapp.Job{ID: "d1", Filename: "devis.pdf", DocType: "devis", Result: mixedResult()})

	body := get(t, s, "/documents/d1").Body.String()

	for _, want := range []string{
		"160-2026",                    // valeur extraite
		"qwen3-8b",                    // modèle LLM
		"Q5_K_M",                      // version du modèle
		"Extrais les champs du devis", // prompt
		"&#34;source_snippet&#34;",    // JSON brut, échappé
	} {
		if !strings.Contains(body, want) {
			t.Errorf("LLM analysis does not contain %q", want)
		}
	}
}

// Un résultat antérieur au jalon 22 n'a pas Result.Pages : le Markdown
// VLM reste affichable (il était déjà dans Result.Parsing), le texte
// natif non — on le dit plutôt que d'afficher un onglet vide.
func TestHandleDocumentDetail_LegacyResultWithoutPages_ExplainsMissingNativeText(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	legacy := mixedResult()
	legacy.Pages = nil
	seedDoneJob(t, store, webapp.Job{ID: "d1", Filename: "devis.pdf", DocType: "devis", Result: legacy})

	body := get(t, s, "/documents/d1").Body.String()

	if !strings.Contains(body, "&lt;table&gt;&lt;tr&gt;&lt;td&gt;1.2.7") {
		t.Errorf("legacy result: VLM markdown from Result.Parsing should still be shown")
	}
	if !strings.Contains(body, "Relancer l&#39;extraction") && !strings.Contains(body, "Relancer l'extraction") {
		t.Errorf("legacy result: missing hint that re-running extraction captures the native text: %s", body)
	}
}

func TestHandleDocumentPanel_RunningJobPollsDoneJobDoesNot(t *testing.T) {
	proceed := make(chan struct{})
	s, store := newTestServer(t, &blockingRunner{result: pipeline.Result{DocType: "facture"}, proceed: proceed})
	seedDoneJob(t, store, webapp.Job{ID: "done", Filename: "a.pdf", DocType: "devis", Result: mixedResult()})

	body, contentType := multipartUpload(t, "running.pdf", []byte("x"))
	req := httptest.NewRequest(http.MethodPost, "/jobs", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	runningID := extractJobID(t, rec.Body.String())
	defer close(proceed)

	running := get(t, s, "/documents/"+runningID+"/panel")
	if running.Code != http.StatusOK || !strings.Contains(running.Body.String(), `hx-get="/documents/`+runningID+`/panel"`) {
		t.Errorf("running panel should poll itself, got %d: %s", running.Code, running.Body.String())
	}
	done := get(t, s, "/documents/done/panel")
	if done.Code != http.StatusOK || strings.Contains(done.Body.String(), "hx-get") {
		t.Errorf("done panel must not poll, got %d: %s", done.Code, done.Body.String())
	}
	if get(t, s, "/documents/nope/panel").Code != http.StatusNotFound {
		t.Errorf("unknown panel should be 404")
	}
}

// --- Jalon 23 : avancement page par page ---

func seedJob(t *testing.T, store *webapp.FakeStore, job webapp.Job, progress *pipeline.Progress) {
	t.Helper()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now()
	}
	if _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if progress != nil {
		if err := store.SetProgress(context.Background(), job.ID, progress); err != nil {
			t.Fatal(err)
		}
	}
}

func halfReadProgress() *pipeline.Progress {
	return &pipeline.Progress{
		Stage: pipeline.StageParsing, PageCount: 5, ParseTotal: 5, ParseDone: 2,
		Pages: []pipeline.PageContent{
			{Page: 1, Text: "BM Constructions — page un lue", Source: pipeline.SourceVLM},
			{Page: 2, Text: "Terrassement — page deux lue", Source: pipeline.SourceVLM},
		},
	}
}

func TestHandleDocumentPanel_Running_ShowsProgressAndTextReadSoFar(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	seedJob(t, store, webapp.Job{ID: "r1", Filename: "devis.pdf", Status: webapp.StatusRunning, StartedAt: time.Now()}, halfReadProgress())

	body := get(t, s, "/documents/r1/panel").Body.String()

	for _, want := range []string{"Lecture OCR", "2/5", "BM Constructions — page un lue", "Terrassement — page deux lue", `hx-get="/documents/r1/panel"`} {
		if !strings.Contains(body, want) {
			t.Errorf("running panel does not contain %q: %s", want, body)
		}
	}
}

func TestHandleDocumentPanel_RunningExtraction_ShowsExtractionStep(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	p := halfReadProgress()
	p.Stage, p.ParseDone, p.ExtractTotal, p.ExtractDone = pipeline.StageExtracting, 5, 5, 3
	seedJob(t, store, webapp.Job{ID: "r1", Filename: "devis.pdf", Status: webapp.StatusRunning, StartedAt: time.Now()}, p)

	body := get(t, s, "/documents/r1/panel").Body.String()
	if !strings.Contains(body, "Extraction") || !strings.Contains(body, "3/5") {
		t.Errorf("panel does not show the extraction step with 3/5: %s", body)
	}
}

// Un job interrompu (redémarrage, erreur) sans résultat garde son texte
// déjà lu consultable dans l'onglet OCR.
func TestHandleDocumentDetail_FailedWithoutResult_ShowsPartialOCRText(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	seedJob(t, store, webapp.Job{ID: "f1", Filename: "devis.pdf", Status: webapp.StatusFailed, Err: "traitement interrompu par un redémarrage du serveur"}, halfReadProgress())

	body := get(t, s, "/documents/f1").Body.String()
	for _, want := range []string{"traitement interrompu", "BM Constructions — page un lue", "Terrassement — page deux lue"} {
		if !strings.Contains(body, want) {
			t.Errorf("failed document page does not contain %q", want)
		}
	}
}

func TestHandleJobStatus_Running_ShowsPageProgress(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	seedJob(t, store, webapp.Job{ID: "r1", Filename: "devis.pdf", Status: webapp.StatusRunning, StartedAt: time.Now()}, halfReadProgress())

	body := get(t, s, "/jobs/r1").Body.String()
	if !strings.Contains(body, "2/5") {
		t.Errorf("upload card does not show 2/5 progress: %s", body)
	}
}

// --- Jalon 24 : Classes en fiches UML + code coloré, Modèle de données
// navigable, code des tests coloré ---

const widgetPkg = "example.com/fixture/widget"

func newCodeTestServer() *Server {
	baseRef := codemap.TypeRef{Package: widgetPkg, Name: "Base"}
	return &Server{
		model: &codemap.Model{ModulePath: "example.com/fixture", Packages: []*codemap.Package{{
			Path: widgetPkg, Name: "widget", ImportNames: []string{"fmt"},
			Types: []*codemap.TypeInfo{
				{
					Name: "Gadget", Package: widgetPkg, Kind: codemap.KindStruct, Exported: true,
					Fields: []codemap.FieldInfo{
						{Name: "Label", Type: "string"},
						{Name: "Parent", Type: "*Base", Refs: []codemap.TypeRef{baseRef}, Optional: true},
					},
					Methods: []codemap.MethodInfo{{Name: "String", Signature: "func() string",
						Source: "func (g Gadget) String() string {\n\treturn fmt.Sprint(g.Label)\n}", File: "widget/gadget.go", Line: 20}},
					Source: "// Gadget est un gadget.\ntype Gadget struct {\n\tLabel  string\n\tParent *Base\n}",
					File:   "widget/gadget.go", Line: 12,
				},
				{Name: "Base", Package: widgetPkg, Kind: codemap.KindStruct, Exported: true,
					Source: "type Base struct{}", File: "widget/base.go", Line: 3},
				{Name: "Lonely", Package: widgetPkg, Kind: codemap.KindStruct, Exported: true},
			},
		}}},
		modulePath: "example.com/fixture",
		categories: []testmap.Category{{Package: widgetPkg, Tests: []testmap.TestFunc{{
			Name: "TestGadget_String", File: "widget/gadget_test.go", Line: 7, Imports: []string{"testing"},
			Source: "func TestGadget_String(t *testing.T) {\n\tif (Gadget{}).String() != \"\" {\n\t\tt.Fatal(\"boom\")\n\t}\n}",
		}}}},
		results: map[string]testrunner.TestResult{},
	}
}

func TestClasses_SidebarShowsPathsRelativeToModule(t *testing.T) {
	body := get(t, newCodeTestServer(), "/classes").Body.String()
	if !strings.Contains(body, ">widget<") {
		t.Errorf("sidebar does not show the short package path 'widget': %s", body)
	}
	if strings.Contains(body, ">example.com/fixture/widget<") {
		t.Errorf("sidebar still shows the full import path as a label")
	}
}

func TestClassDetail_UMLCardWithLinkedFieldTypes(t *testing.T) {
	body := get(t, newCodeTestServer(), "/classes/detail?pkg="+widgetPkg+"&name=Gadget").Body.String()
	for _, want := range []string{`class="uml`, "Label", "Parent", "*Base", "String"} {
		if !strings.Contains(body, want) {
			t.Errorf("UML card does not contain %q", want)
		}
	}
	if !strings.Contains(body, `href="/classes?pkg=example.com/fixture/widget&amp;name=Base"`) {
		t.Errorf("field type *Base is not a link to Base: %s", body)
	}
	if !strings.Contains(body, `href="/model?pkg=example.com/fixture/widget&amp;name=Gadget"`) {
		t.Errorf("no link to view Gadget in the data model diagram")
	}
}

func TestClassDetail_SourceCodeHighlightedWithLocation(t *testing.T) {
	body := get(t, newCodeTestServer(), "/classes/detail?pkg="+widgetPkg+"&name=Gadget").Body.String()
	for _, want := range []string{
		`<span class="tok-kw">type</span>`,
		`<span class="tok-type">Gadget</span>`,
		`<span class="tok-com">// Gadget est un gadget.</span>`,
		`<span class="tok-fn">String</span>`, // méthode déclarée
		`<span class="tok-fn">Sprint</span>`, // fmt.Sprint : fmt connu via ImportNames
		"widget/gadget.go:12",
		"widget/gadget.go:20",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("class source does not contain %q", want)
		}
	}
}

func TestModel_FocusedDiagramWithNavigableNodes(t *testing.T) {
	rec := get(t, newCodeTestServer(), "/model?pkg="+widgetPkg+"&name=Gadget")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<svg",
		`class="dnode focus"`, // Gadget, centre du diagramme
		`href="/model?pkg=example.com/fixture/widget&amp;name=Base"`, // clic = recentrer
		"0..1", // cardinalité de Parent *Base
		`class="active">Modèle`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("model page does not contain %q", want)
		}
	}
	// Lonely reste proposé dans le sélecteur de types, mais n'a aucune
	// relation avec Gadget : pas de boîte pour lui dans le diagramme.
	if strings.Contains(body, "<title>widget.Lonely</title>") {
		t.Errorf("Lonely has no relation with Gadget, should not be drawn")
	}
	if !strings.Contains(body, "<title>widget.Base</title>") {
		t.Errorf("Base should be drawn as a node")
	}
}

func TestModel_DefaultsToAConnectedTypeAndRejectsUnknown(t *testing.T) {
	s := newCodeTestServer()
	if body := get(t, s, "/model").Body.String(); !strings.Contains(body, `class="dnode focus"`) {
		t.Errorf("/model without a type should focus a default type: %s", body)
	}
	if rec := get(t, s, "/model?pkg="+widgetPkg+"&name=Nope"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown type: status = %d, want 404", rec.Code)
	}
}

func TestTests_ShortPathsAndHighlightedTestSource(t *testing.T) {
	body := get(t, newCodeTestServer(), "/tests").Body.String()
	if !strings.Contains(body, ">widget<") {
		t.Errorf("tests page does not show the short package path: %s", body)
	}
	for _, want := range []string{"<details", `<span class="tok-fn">TestGadget_String</span>`, `<span class="tok-ctl">if</span>`, `<span class="tok-str">&#34;boom&#34;</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("tests page does not contain %q", want)
		}
	}
}

// Le type central par défaut est celui qui contient le plus d'autres
// types (le cœur du modèle), pas le plus référencé : sur Jarvis,
// schema.Field est utilisé partout mais n'est qu'une brique.
func TestModel_DefaultFocusIsTheBiggestContainer(t *testing.T) {
	leaf := codemap.TypeRef{Package: widgetPkg, Name: "Leaf"}
	fieldTo := func(name string) codemap.FieldInfo {
		return codemap.FieldInfo{Name: name, Type: "Leaf", Refs: []codemap.TypeRef{leaf}}
	}
	s := &Server{model: &codemap.Model{ModulePath: "example.com/fixture", Packages: []*codemap.Package{{Path: widgetPkg, Name: "widget", Types: []*codemap.TypeInfo{
		{Name: "Leaf", Package: widgetPkg, Kind: codemap.KindStruct},
		{Name: "A", Package: widgetPkg, Kind: codemap.KindStruct, Fields: []codemap.FieldInfo{fieldTo("X")}},
		{Name: "B", Package: widgetPkg, Kind: codemap.KindStruct, Fields: []codemap.FieldInfo{fieldTo("X")}},
		{Name: "C", Package: widgetPkg, Kind: codemap.KindStruct, Fields: []codemap.FieldInfo{fieldTo("X")}},
		{Name: "Root", Package: widgetPkg, Kind: codemap.KindStruct, Fields: []codemap.FieldInfo{
			{Name: "A", Type: "A", Refs: []codemap.TypeRef{{Package: widgetPkg, Name: "A"}}},
			{Name: "B", Type: "B", Refs: []codemap.TypeRef{{Package: widgetPkg, Name: "B"}}},
		}},
	}}}}}

	body := get(t, s, "/model").Body.String()
	if !strings.Contains(body, `value="example.com/fixture/widget#Root" selected`) {
		t.Errorf("default focus should be Root (contains the most types), not Leaf (most referenced)")
	}
}

// Le modèle de données, ce sont les structs : un orchestrateur relié à
// de nombreuses interfaces de service (Pipeline sur Jarvis) ne doit pas
// l'emporter sur le type qui contient le plus de données.
func TestModel_DefaultFocusCountsOnlyDataTypes(t *testing.T) {
	ref := func(n string) codemap.TypeRef { return codemap.TypeRef{Package: widgetPkg, Name: n} }
	f := func(n string) codemap.FieldInfo {
		return codemap.FieldInfo{Name: n, Type: n, Refs: []codemap.TypeRef{ref(n)}}
	}
	s := &Server{model: &codemap.Model{ModulePath: "example.com/fixture", Packages: []*codemap.Package{{Path: widgetPkg, Name: "widget", Types: []*codemap.TypeInfo{
		{Name: "S1", Package: widgetPkg, Kind: codemap.KindInterface},
		{Name: "S2", Package: widgetPkg, Kind: codemap.KindInterface},
		{Name: "S3", Package: widgetPkg, Kind: codemap.KindInterface},
		{Name: "D1", Package: widgetPkg, Kind: codemap.KindStruct},
		{Name: "D2", Package: widgetPkg, Kind: codemap.KindStruct},
		{Name: "Orchestrator", Package: widgetPkg, Kind: codemap.KindStruct, Fields: []codemap.FieldInfo{f("S1"), f("S2"), f("S3")}},
		{Name: "Result", Package: widgetPkg, Kind: codemap.KindStruct, Fields: []codemap.FieldInfo{f("D1"), f("D2")}},
	}}}}}

	if body := get(t, s, "/model").Body.String(); !strings.Contains(body, `value="example.com/fixture/widget#Result" selected`) {
		t.Errorf("default focus should be Result (2 data types) rather than Orchestrator (3 interfaces)")
	}
}

// Ticket "Ajouter commentaire sur document" : la fiche du document porte
// une zone de commentaire, qui s'enregistre sans recharger la page.
func TestHandleDocumentComment_UpdatesAndReturnsCommentForm(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})
	body, contentType := multipartUpload(t, "doc.pdf", []byte("x"))
	req := httptest.NewRequest(http.MethodPost, "/jobs", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	id := extractJobID(t, rec.Body.String())
	// La zone n'apparaît qu'une fois le document traité : pendant le
	// traitement, la fiche se rafraîchit et effacerait la saisie.
	for i := 0; i < 200; i++ {
		if job, _, _ := s.Jobs.Get(context.Background(), id); job.Status == webapp.StatusDone {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	page := httptest.NewRecorder()
	s.Routes().ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/documents/"+id, nil))
	if !strings.Contains(page.Body.String(), `name="comment"`) {
		t.Fatalf("document page has no comment box")
	}

	form := url.Values{"comment": {"Relancer le fournisseur <vite>"}}
	req2 := httptest.NewRequest(http.MethodPost, "/documents/"+id+"/comment", strings.NewReader(form.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "Relancer le fournisseur &lt;vite&gt;") || !strings.Contains(rec2.Body.String(), "Enregistré") {
		t.Errorf("fragment = %s, want the escaped comment and a saved notice", rec2.Body.String())
	}
	job, ok, err := s.Jobs.Get(context.Background(), id)
	if err != nil || !ok || job.Comment != "Relancer le fournisseur <vite>" {
		t.Errorf("job.Comment = %q (%v, %v)", job.Comment, ok, err)
	}
}

func TestHandleDocumentComment_UnknownDocumentIs404(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})
	req := httptest.NewRequest(http.MethodPost, "/documents/inconnu/comment", strings.NewReader("comment=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
