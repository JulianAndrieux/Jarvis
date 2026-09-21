package main

import (
	"bytes"
	"context"
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
	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
	"github.com/JulianAndrieux/Jarvis/internal/testmap"
	"github.com/JulianAndrieux/Jarvis/internal/testrunner"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

// --- Upload / suivi de documents (portés depuis l'ex-cmd/jarvisweb,
// adaptés à la classification automatique du jalon 15) ---

type blockingRunner struct {
	result  pipeline.Result
	err     error
	proceed chan struct{}
}

func (r *blockingRunner) RunAuto(ctx context.Context, path string) (pipeline.Result, error) {
	if r.proceed != nil {
		<-r.proceed
	}
	return r.result, r.err
}

func (r *blockingRunner) RunWithType(ctx context.Context, docType, path string) (pipeline.Result, error) {
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
	stored, ok, err := fakeStore.Get(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("fakeStore.Get(%s) = %+v, %v, %v", id, stored, ok, err)
	}
	if string(stored.Content) != "%PDF-1.4 fake" {
		t.Errorf("stored content = %q, want the uploaded bytes", stored.Content)
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
