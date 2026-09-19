package main

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/doctype"
	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

// blockingRunner laisse le test contrôler quand Run() se termine, pour
// observer l'état "pending"/"running" avant qu'un job ne se termine.
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

func newTestServer(t *testing.T, runner webapp.Runner) (*Server, string) {
	t.Helper()
	uploadDir := t.TempDir()
	s := &Server{
		Jobs:      webapp.NewJobManager(runner, doctype.NewDefaultRegistry()),
		Registry:  doctype.NewDefaultRegistry(),
		UploadDir: uploadDir,
	}
	return s, uploadDir
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
	s, uploadDir := newTestServer(t, &blockingRunner{proceed: proceed})
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

	entries, err := os.ReadDir(uploadDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("upload dir has %d entries, want 1 (the saved PDF)", len(entries))
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

func TestHandleSubmit_MissingFile_ReturnsBadRequest(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("doc_type", "facture")
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/jobs", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
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
