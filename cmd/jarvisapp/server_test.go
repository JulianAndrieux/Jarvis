package main

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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

// --- Upload / suivi de documents (portés depuis l'ex-cmd/jarvisweb) ---

type blockingRunner struct {
	result  pipeline.Result
	err     error
	proceed chan struct{}
}

func (r *blockingRunner) Run(ctx context.Context, reg doctype.Registration, path string) (pipeline.Result, error) {
	if r.proceed != nil {
		<-r.proceed
	}
	return r.result, r.err
}

func newTestServer(t *testing.T, runner webapp.Runner) (*Server, *webapp.FakeStore) {
	t.Helper()
	fakeStore := webapp.NewFakeStore()
	jobs := webapp.NewJobManager(fakeStore, runner, doctype.NewDefaultRegistry())
	jobs.WorkDir = t.TempDir()
	s := &Server{Jobs: jobs, Registry: doctype.NewDefaultRegistry()}
	return s, fakeStore
}

func multipartUpload(t *testing.T, docType, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("doc_type", docType); err != nil {
		t.Fatal(err)
	}
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

func TestHandleIndex_ListsDocTypes(t *testing.T) {
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
}

func TestHandleSubmit_ValidUpload_ReturnsRunningFragment(t *testing.T) {
	proceed := make(chan struct{})
	s, fakeStore := newTestServer(t, &blockingRunner{proceed: proceed})
	defer close(proceed)

	body, contentType := multipartUpload(t, "facture", "doc.pdf", []byte("%PDF-1.4 fake"))
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

	id := extractJobID(t, rec.Body.String())
	stored, ok, err := fakeStore.Get(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("fakeStore.Get(%s) = %+v, %v, %v", id, stored, ok, err)
	}
	if string(stored.Content) != "%PDF-1.4 fake" {
		t.Errorf("stored content = %q, want the uploaded bytes", stored.Content)
	}
}

func TestHandleSubmit_UnknownDocType_ReturnsBadRequest(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})

	body, contentType := multipartUpload(t, "extraterrestre", "doc.pdf", []byte("x"))
	req := httptest.NewRequest(http.MethodPost, "/jobs", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	s.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
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

func TestHandleJobStatus_DoneJob_RendersResultWithoutPolling(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{result: pipeline.Result{
		Triage: pipeline.Result{}.Triage,
	}})

	body, contentType := multipartUpload(t, "facture", "doc.pdf", []byte("x"))
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
}

func TestHandleJobStatus_FailedJob_ShowsError(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{err: errors.New("pipeline boom")})

	body, contentType := multipartUpload(t, "facture", "doc.pdf", []byte("x"))
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

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
