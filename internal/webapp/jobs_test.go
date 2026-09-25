package webapp

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/formats"
	"github.com/JulianAndrieux/Jarvis/internal/gate"
	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
)

// fakeRunner permet de contrôler précisément le timing d'exécution dans
// les tests : started se ferme dès que RunAuto() est appelé, et RunAuto()
// bloque jusqu'à ce que proceed soit fermé (si non-nil). Elle enregistre
// aussi le chemin reçu, pour vérifier que JobManager a bien matérialisé
// le contenu du job dans un fichier lisible — protégé par un mutex, un
// même fakeRunner pouvant être appelé depuis plusieurs jobs concurrents.
type fakeRunner struct {
	result pipeline.Result
	err    error
	// progress est émis (dans l'ordre) avant de bloquer sur proceed —
	// simule un pipeline qui avance page par page (jalon 23).
	progress []pipeline.Progress
	started  chan struct{}
	proceed  chan struct{}

	mu         sync.Mutex
	gotPath    string
	gotDocType string
}

func (f *fakeRunner) RunAuto(ctx context.Context, path string, onProgress pipeline.ProgressFunc) (pipeline.Result, error) {
	f.mu.Lock()
	f.gotPath = path
	f.mu.Unlock()
	for _, p := range f.progress {
		onProgress(p)
	}
	if f.started != nil {
		close(f.started)
	}
	if f.proceed != nil {
		<-f.proceed
	}
	return f.result, f.err
}

func (f *fakeRunner) RunWithType(ctx context.Context, docType, path string, onProgress pipeline.ProgressFunc) (pipeline.Result, error) {
	f.mu.Lock()
	f.gotPath = path
	f.gotDocType = docType
	f.mu.Unlock()
	for _, p := range f.progress {
		onProgress(p)
	}
	if f.started != nil {
		close(f.started)
	}
	if f.proceed != nil {
		<-f.proceed
	}
	return f.result, f.err
}

func (f *fakeRunner) Path() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gotPath
}

func (f *fakeRunner) DocType() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gotDocType
}

func waitForStatus(t *testing.T, m *JobManager, id string, want Status) Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, ok, err := m.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("Get(%s) error = %v", id, err)
		}
		if !ok {
			t.Fatalf("Get(%s) ok = false", id)
		}
		if job.Status == want {
			return job
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("job %s did not reach status %q in time", id, want)
	return Job{}
}

func newTestJobManager(runner Runner) *JobManager {
	return NewJobManager(NewFakeStore(), runner)
}

func TestJobManager_Submit_StartsPendingThenRunning(t *testing.T) {
	started := make(chan struct{})
	proceed := make(chan struct{})
	runner := &fakeRunner{started: started, proceed: proceed}
	m := newTestJobManager(runner)
	defer close(proceed)

	job, err := m.Submit(context.Background(), "doc.pdf", []byte("content"))
	if err != nil {
		t.Fatalf("Submit() error = %v, want nil", err)
	}
	if job.Filename != "doc.pdf" {
		t.Errorf("job = %+v, want Filename=doc.pdf", job)
	}
	if job.DocType != "" {
		t.Errorf("job.DocType = %q, want empty (pas encore classifié)", job.DocType)
	}

	<-started // le runner a bien été invoqué de façon asynchrone
	running := waitForStatus(t, m, job.ID, StatusRunning)
	if running.StartedAt.IsZero() {
		t.Error("running.StartedAt is zero, want it set when processing starts")
	}
}

// Des tags donnés à l'envoi font partie du job dès sa création (jalon 39 :
// posés juste après, ils étaient écrasés par un traitement qui échouait
// aussitôt — vu en réel, tags perdus dans MongoDB).
func TestJobManager_SubmitWithTags_TagsFromCreation(t *testing.T) {
	m := newTestJobManager(&fakeRunner{err: errors.New("modèle absent")})
	job, err := m.SubmitWithTags(context.Background(), "facture.pdf", []byte("%PDF"), []string{"email"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(job.Tags, ",") != "email" {
		t.Errorf("created job tags = %v", job.Tags)
	}
	done := waitForStatus(t, m, job.ID, StatusFailed)
	if strings.Join(done.Tags, ",") != "email" {
		t.Errorf("tags after processing = %v, want kept", done.Tags)
	}
}

func TestJobManager_Submit_MaterializesContentForRunner(t *testing.T) {
	started := make(chan struct{})
	proceed := make(chan struct{})
	runner := &fakeRunner{started: started, proceed: proceed}
	m := newTestJobManager(runner)

	job, err := m.Submit(context.Background(), "doc.pdf", []byte("%PDF-1.4 contenu de test"))
	if err != nil {
		t.Fatal(err)
	}
	<-started // RunAuto() a été appelé et a enregistré le chemin, mais attend `proceed`

	path := runner.Path()
	if path == "" {
		t.Fatal("runner never received a path")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected the materialized file to exist while RunAuto() is in progress: %v", err)
	}
	if string(got) != "%PDF-1.4 contenu de test" {
		t.Errorf("materialized file content = %q, want the submitted content", got)
	}

	close(proceed)
	waitForStatus(t, m, job.ID, StatusDone)

	// Le fichier temporaire doit être supprimé une fois le job terminé.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("temp file %s still exists after job completion, want it cleaned up", path)
	}
}

func TestJobManager_RunSucceeds_SetsStatusDoneWithResultAndDocType(t *testing.T) {
	wantResult := pipeline.Result{Path: "somewhere", DocType: "facture"}
	runner := &fakeRunner{result: wantResult}
	m := newTestJobManager(runner)

	job, err := m.Submit(context.Background(), "doc.pdf", []byte("content"))
	if err != nil {
		t.Fatal(err)
	}

	done := waitForStatus(t, m, job.ID, StatusDone)
	if done.Result == nil || done.Result.Path != "somewhere" {
		t.Errorf("done.Result = %+v, want %+v", done.Result, wantResult)
	}
	if done.DocType != "facture" {
		t.Errorf("done.DocType = %q, want %q (repris du résultat de classification)", done.DocType, "facture")
	}
	if done.Err != "" {
		t.Errorf("done.Err = %q, want empty", done.Err)
	}
}

func TestJobManager_RunSucceeds_UnclassifiedDocument_LeavesDocTypeEmpty(t *testing.T) {
	runner := &fakeRunner{result: pipeline.Result{Path: "somewhere", DocType: ""}}
	m := newTestJobManager(runner)

	job, err := m.Submit(context.Background(), "doc.pdf", []byte("content"))
	if err != nil {
		t.Fatal(err)
	}

	done := waitForStatus(t, m, job.ID, StatusDone)
	if done.DocType != "" {
		t.Errorf("done.DocType = %q, want empty for an unclassified document", done.DocType)
	}
}

func TestJobManager_RunFails_SetsStatusFailedWithError(t *testing.T) {
	runner := &fakeRunner{err: errors.New("pipeline boom")}
	m := newTestJobManager(runner)

	job, err := m.Submit(context.Background(), "doc.pdf", []byte("content"))
	if err != nil {
		t.Fatal(err)
	}

	failed := waitForStatus(t, m, job.ID, StatusFailed)
	if failed.Err != "pipeline boom" {
		t.Errorf("failed.Err = %q, want %q", failed.Err, "pipeline boom")
	}
	if failed.Result != nil {
		t.Errorf("failed.Result = %+v, want nil", failed.Result)
	}
}

func TestJobManager_Get_UnknownID_ReturnsFalse(t *testing.T) {
	m := newTestJobManager(&fakeRunner{})

	_, ok, err := m.Get(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if ok {
		t.Error("Get() ok = true, want false for an unknown job id")
	}
}

func TestJobManager_OnFinish_CalledAfterCompletion(t *testing.T) {
	runner := &fakeRunner{result: pipeline.Result{Path: "somewhere"}}
	m := newTestJobManager(runner)

	called := make(chan Job, 1)
	m.OnFinish = func(job Job) { called <- job }

	job, err := m.Submit(context.Background(), "doc.pdf", []byte("content"))
	if err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-called:
		if got.ID != job.ID || got.Status != StatusDone {
			t.Errorf("OnFinish job = %+v, want ID=%s Status=done", got, job.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnFinish was not called in time")
	}
}

func TestJobManager_Submit_GeneratesUniqueIDs(t *testing.T) {
	m := newTestJobManager(&fakeRunner{})

	job1, err := m.Submit(context.Background(), "a.pdf", []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	job2, err := m.Submit(context.Background(), "b.pdf", []byte("b"))
	if err != nil {
		t.Fatal(err)
	}
	if job1.ID == job2.ID {
		t.Errorf("job1.ID == job2.ID (%q), want unique ids", job1.ID)
	}
}

// updateFailingStore laisse Create/Get fonctionner normalement mais fait
// échouer Update — pour vérifier que JobManager continue le traitement
// même si la persistance du statut intermédiaire échoue (best effort, pas
// bloquant).
type updateFailingStore struct {
	*FakeStore
}

func (s *updateFailingStore) Update(ctx context.Context, job Job) error {
	return errors.New("update boom")
}

func TestJobManager_StoreUpdateFailure_DoesNotBlockProcessing(t *testing.T) {
	started := make(chan struct{})
	runner := &fakeRunner{result: pipeline.Result{Path: "somewhere"}, started: started}
	store := &updateFailingStore{FakeStore: NewFakeStore()}
	m := NewJobManager(store, runner)

	if _, err := m.Submit(context.Background(), "doc.pdf", []byte("content")); err != nil {
		t.Fatal(err)
	}

	// Store.Update échoue systématiquement, donc Get (qui lit depuis le
	// même FakeStore sous-jacent) ne verra jamais StatusDone — mais le
	// runner doit tout de même avoir été appelé, sans paniquer ni bloquer
	// indéfiniment. `started` (fermé par fakeRunner.RunAuto avant tout
	// accès à gotPath) synchronise proprement cette lecture avec
	// l'écriture faite dans l'autre goroutine.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("runner was never invoked despite a failing Store.Update")
	}
}

// --- Reprocess (changement manuel de type, bibliothèque de documents,
// jalon 17) ---

func TestJobManager_Reprocess_RunsWithGivenTypeAndUpdatesDocType(t *testing.T) {
	runner := &fakeRunner{result: pipeline.Result{Path: "somewhere", DocType: "facture"}}
	m := newTestJobManager(runner)

	job, err := m.Submit(context.Background(), "doc.pdf", []byte("%PDF-1.4 contenu"))
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, m, job.ID, StatusDone)

	if err := m.Reprocess(context.Background(), job.ID, "facture"); err != nil {
		t.Fatalf("Reprocess() error = %v, want nil", err)
	}

	done := waitForStatus(t, m, job.ID, StatusDone)
	if done.DocType != "facture" {
		t.Errorf("done.DocType = %q, want facture", done.DocType)
	}
	if runner.DocType() != "facture" {
		t.Errorf("runner received docType %q, want facture", runner.DocType())
	}
}

func TestJobManager_Reprocess_SetsFreshStartedAt(t *testing.T) {
	runner := &fakeRunner{result: pipeline.Result{Path: "somewhere", DocType: "facture"}}
	m := newTestJobManager(runner)

	job, err := m.Submit(context.Background(), "doc.pdf", []byte("content"))
	if err != nil {
		t.Fatal(err)
	}
	firstRun := waitForStatus(t, m, job.ID, StatusDone)
	firstStartedAt := firstRun.StartedAt
	if firstStartedAt.IsZero() {
		t.Fatal("firstRun.StartedAt is zero, want it set")
	}

	time.Sleep(2 * time.Millisecond) // garantit un StartedAt strictement postérieur

	proceed := make(chan struct{})
	runner.proceed = proceed
	runner.started = make(chan struct{})
	defer close(proceed)

	if err := m.Reprocess(context.Background(), job.ID, "facture"); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	running := waitForStatus(t, m, job.ID, StatusRunning)
	if !running.StartedAt.After(firstStartedAt) {
		t.Errorf("reprocess StartedAt = %v, want it after the first run's StartedAt (%v)", running.StartedAt, firstStartedAt)
	}
}

func TestJobManager_Reprocess_UnknownJobID_ReturnsError(t *testing.T) {
	m := newTestJobManager(&fakeRunner{})

	err := m.Reprocess(context.Background(), "does-not-exist", "facture")
	if err == nil {
		t.Fatal("Reprocess() error = nil, want non-nil for an unknown job id")
	}
}

func TestJobManager_Reprocess_RunnerError_SetsStatusFailed(t *testing.T) {
	runner := &fakeRunner{result: pipeline.Result{Path: "somewhere", DocType: "facture"}}
	m := newTestJobManager(runner)
	job, err := m.Submit(context.Background(), "doc.pdf", []byte("content"))
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, m, job.ID, StatusDone)

	runner.err = errors.New("reprocess boom")
	if err := m.Reprocess(context.Background(), job.ID, "facture"); err != nil {
		t.Fatal(err)
	}

	failed := waitForStatus(t, m, job.ID, StatusFailed)
	if failed.Err != "reprocess boom" {
		t.Errorf("failed.Err = %q, want %q", failed.Err, "reprocess boom")
	}
}

// --- SetTags / List (bibliothèque de documents, jalon 17) ---

func TestJobManager_SetTags_UpdatesStoredTags(t *testing.T) {
	m := newTestJobManager(&fakeRunner{})
	job, err := m.Submit(context.Background(), "doc.pdf", []byte("content"))
	if err != nil {
		t.Fatal(err)
	}

	if err := m.SetTags(context.Background(), job.ID, []string{"urgent", "client-x"}); err != nil {
		t.Fatalf("SetTags() error = %v, want nil", err)
	}

	got, ok, err := m.Get(context.Background(), job.ID)
	if err != nil || !ok {
		t.Fatalf("Get() = %+v, %v, %v", got, ok, err)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "urgent" || got.Tags[1] != "client-x" {
		t.Errorf("got.Tags = %v, want [urgent client-x]", got.Tags)
	}
}

func TestJobManager_SetTags_UnknownJobID_ReturnsError(t *testing.T) {
	m := newTestJobManager(&fakeRunner{})

	err := m.SetTags(context.Background(), "does-not-exist", []string{"x"})
	if err == nil {
		t.Fatal("SetTags() error = nil, want non-nil for an unknown job id")
	}
}

func TestJobManager_List_DelegatesToStore(t *testing.T) {
	m := newTestJobManager(&fakeRunner{})
	if _, err := m.Submit(context.Background(), "a.pdf", []byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Submit(context.Background(), "b.pdf", []byte("b")); err != nil {
		t.Fatal(err)
	}

	got, err := m.List(context.Background(), ListQuery{})
	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Errorf("len(List()) = %d, want 2", len(got))
	}
}

// --- Delete (jalon 18) ---

func TestJobManager_Delete_RemovesJob(t *testing.T) {
	m := newTestJobManager(&fakeRunner{})
	job, err := m.Submit(context.Background(), "doc.pdf", []byte("content"))
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Delete(context.Background(), job.ID); err != nil {
		t.Fatalf("Delete() error = %v, want nil", err)
	}

	_, ok, err := m.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("Get() ok = true after Delete(), want false")
	}
}

func TestJobManager_Delete_UnknownID_ReturnsError(t *testing.T) {
	m := newTestJobManager(&fakeRunner{})

	err := m.Delete(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("Delete() error = nil, want non-nil for an unknown job id")
	}
}

// --- RecoverOrphaned (jalon 20) ---
//
// Un job encore "pending"/"running" au démarrage d'un nouveau process ne
// peut par construction jamais aboutir : la goroutine qui l'aurait
// terminé appartenait à l'ancien process, disparue avec lui (Submit ne
// persiste aucun état de reprise). RecoverOrphaned est appelé une fois
// au démarrage pour les faire échouer explicitement plutôt que de les
// laisser bloqués indéfiniment à sonder dans le vide.

func TestJobManager_RecoverOrphaned_MarksPendingAndRunningJobsAsFailed(t *testing.T) {
	store := NewFakeStore()
	ctx := context.Background()
	_, _ = store.Create(ctx, Job{ID: "running-job", Status: StatusRunning, CreatedAt: time.Now()})
	_, _ = store.Create(ctx, Job{ID: "pending-job", Status: StatusPending, CreatedAt: time.Now()})
	_, _ = store.Create(ctx, Job{ID: "done-job", Status: StatusDone, CreatedAt: time.Now()})

	m := NewJobManager(store, &fakeRunner{})
	n, err := m.RecoverOrphaned(ctx)
	if err != nil {
		t.Fatalf("RecoverOrphaned() error = %v, want nil", err)
	}
	if n != 2 {
		t.Errorf("RecoverOrphaned() = %d, want 2", n)
	}

	for _, id := range []string{"running-job", "pending-job"} {
		job, ok, err := store.Get(ctx, id)
		if err != nil || !ok {
			t.Fatalf("Get(%s) = %+v, %v, %v", id, job, ok, err)
		}
		if job.Status != StatusFailed {
			t.Errorf("job %s Status = %q, want failed", id, job.Status)
		}
		if job.Err == "" {
			t.Errorf("job %s Err is empty, want an explanatory message", id)
		}
		if job.FinishedAt.IsZero() {
			t.Errorf("job %s FinishedAt is zero, want it set", id)
		}
	}

	done, ok, err := store.Get(ctx, "done-job")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if done.Status != StatusDone {
		t.Errorf("done-job Status = %q, want it untouched (done)", done.Status)
	}
}

func TestJobManager_RecoverOrphaned_NoOrphans_ReturnsZero(t *testing.T) {
	store := NewFakeStore()
	ctx := context.Background()
	_, _ = store.Create(ctx, Job{ID: "done-job", Status: StatusDone, CreatedAt: time.Now()})

	m := NewJobManager(store, &fakeRunner{})
	n, err := m.RecoverOrphaned(ctx)
	if err != nil {
		t.Fatalf("RecoverOrphaned() error = %v, want nil", err)
	}
	if n != 0 {
		t.Errorf("RecoverOrphaned() = %d, want 0", n)
	}
}

// fakeThumbRenderer implémente parsing.Renderer pour les tests de
// miniature : compte ses appels et vérifie qu'on lui passe bien un
// fichier contenant le PDF du job.
type fakeThumbRenderer struct {
	mu      sync.Mutex
	calls   int
	gotPage int
	gotDPI  int
	gotPDF  []byte
	png     []byte
	err     error
}

func (r *fakeThumbRenderer) RenderPage(ctx context.Context, path string, page, dpi int) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.gotPage, r.gotDPI = page, dpi
	r.gotPDF, _ = os.ReadFile(path)
	return r.png, r.err
}

func TestJobManager_Thumbnail_RendersFirstPageOnceThenServesStored(t *testing.T) {
	store := NewFakeStore()
	store.Create(context.Background(), Job{ID: "a", Filename: "a.pdf", Content: []byte("%PDF-a")})
	renderer := &fakeThumbRenderer{png: []byte("thumb-png")}
	m := NewJobManager(store, &fakeRunner{})
	m.WorkDir = t.TempDir()
	m.Renderer = renderer

	for i := 0; i < 2; i++ {
		png, ok, err := m.Thumbnail(context.Background(), "a")
		if err != nil || !ok {
			t.Fatalf("Thumbnail() call %d = ok %v, err %v; want ok, nil", i+1, ok, err)
		}
		if string(png) != "thumb-png" {
			t.Errorf("Thumbnail() call %d = %q, want thumb-png", i+1, png)
		}
	}

	if renderer.calls != 1 {
		t.Errorf("renderer calls = %d, want 1 (second call must be served from the store)", renderer.calls)
	}
	if renderer.gotPage != 1 || renderer.gotDPI != ThumbnailDPI {
		t.Errorf("rendered page %d at %d DPI, want page 1 at %d DPI", renderer.gotPage, renderer.gotDPI, ThumbnailDPI)
	}
	if string(renderer.gotPDF) != "%PDF-a" {
		t.Errorf("renderer read %q, want the job's PDF content", renderer.gotPDF)
	}
	stored, _, _ := store.Get(context.Background(), "a")
	if string(stored.Thumbnail) != "thumb-png" {
		t.Errorf("stored Thumbnail = %q, want it persisted", stored.Thumbnail)
	}
}

func TestJobManager_Thumbnail_UnknownJob_ReturnsNotFound(t *testing.T) {
	m := NewJobManager(NewFakeStore(), &fakeRunner{})
	m.Renderer = &fakeThumbRenderer{png: []byte("x")}

	_, ok, err := m.Thumbnail(context.Background(), "nope")
	if err != nil || ok {
		t.Errorf("Thumbnail() = ok %v, err %v; want ok=false, err=nil", ok, err)
	}
}

func TestJobManager_Thumbnail_RenderError_ReturnsErrorAndStoresNothing(t *testing.T) {
	store := NewFakeStore()
	store.Create(context.Background(), Job{ID: "a", Content: []byte("%PDF")})
	m := NewJobManager(store, &fakeRunner{})
	m.WorkDir = t.TempDir()
	m.Renderer = &fakeThumbRenderer{err: errors.New("pdftoppm boom")}

	if _, _, err := m.Thumbnail(context.Background(), "a"); err == nil {
		t.Error("Thumbnail() error = nil, want the render error")
	}
	stored, _, _ := store.Get(context.Background(), "a")
	if stored.Thumbnail != nil {
		t.Errorf("stored Thumbnail = %q, want nothing stored after a failed render", stored.Thumbnail)
	}
}

func TestJobManager_Thumbnail_NoRenderer_ReturnsError(t *testing.T) {
	store := NewFakeStore()
	store.Create(context.Background(), Job{ID: "a", Content: []byte("%PDF")})
	m := NewJobManager(store, &fakeRunner{})

	if _, _, err := m.Thumbnail(context.Background(), "a"); err == nil {
		t.Error("Thumbnail() error = nil, want an explicit error when no Renderer is configured")
	}
}

// Jalon 23 : l'avancement remonté par le pipeline est enregistré pendant
// le traitement — le texte déjà lu est consultable avant la fin.
func TestJobManager_Run_PersistsProgressWhileRunning(t *testing.T) {
	store := NewFakeStore()
	runner := &fakeRunner{
		started: make(chan struct{}),
		proceed: make(chan struct{}),
		progress: []pipeline.Progress{
			{Stage: pipeline.StageParsing, ParseTotal: 3, ParseDone: 0},
			{Stage: pipeline.StageParsing, ParseTotal: 3, ParseDone: 1, Pages: []pipeline.PageContent{{Page: 1, Text: "page un", Source: pipeline.SourceVLM}}},
		},
	}
	m := NewJobManager(store, runner)
	m.WorkDir = t.TempDir()

	job, err := m.Submit(context.Background(), "a.pdf", []byte("%PDF"))
	if err != nil {
		t.Fatal(err)
	}
	<-runner.started

	got, _, _ := store.Get(context.Background(), job.ID)
	if got.Progress == nil || got.Progress.ParseDone != 1 || len(got.Progress.Pages) != 1 || got.Progress.Pages[0].Text != "page un" {
		t.Errorf("stored Progress while running = %+v, want the latest event (page 1 read)", got.Progress)
	}
	close(runner.proceed)
}

// Une nouvelle tentative (ré-extraction) repart d'un avancement vide :
// l'affichage ne doit pas montrer "page 5/5" d'une tentative précédente.
func TestJobManager_Reprocess_ClearsPreviousProgressBeforeRunning(t *testing.T) {
	store := NewFakeStore()
	ctx := context.Background()
	store.Create(ctx, Job{ID: "a", Content: []byte("%PDF"), Status: StatusFailed})
	store.SetProgress(ctx, "a", &pipeline.Progress{ParseDone: 5, ParseTotal: 5})

	runner := &fakeRunner{started: make(chan struct{}), proceed: make(chan struct{})}
	m := NewJobManager(store, runner)
	m.WorkDir = t.TempDir()

	if err := m.Reprocess(ctx, "a", "facture"); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	got, _, _ := store.Get(ctx, "a")
	if got.Progress != nil {
		t.Errorf("Progress after reprocess started = %+v, want cleared", got.Progress)
	}
	close(runner.proceed)
}

// Un redémarrage garde le texte déjà lu : RecoverOrphaned marque le job
// en échec sans effacer son avancement.
func TestJobManager_RecoverOrphaned_KeepsPartialProgress(t *testing.T) {
	store := NewFakeStore()
	ctx := context.Background()
	store.Create(ctx, Job{ID: "a", Status: StatusRunning})
	store.SetProgress(ctx, "a", &pipeline.Progress{ParseDone: 2, Pages: []pipeline.PageContent{{Page: 1, Text: "déjà lu"}}})

	if _, err := NewJobManager(store, &fakeRunner{}).RecoverOrphaned(ctx); err != nil {
		t.Fatal(err)
	}
	got, _, _ := store.Get(ctx, "a")
	if got.Status != StatusFailed || got.Progress == nil || got.Progress.Pages[0].Text != "déjà lu" {
		t.Errorf("after recovery: status=%s progress=%+v, want failed with progress kept", got.Status, got.Progress)
	}
}

// --- Jalon 25 : tous les types de fichiers, file d'attente globale ---

// fakeConverter simule formats.Converter : enregistre les appels (format,
// contenu du fichier source, extension du chemin) et l'étape
// d'avancement visible en base au moment de l'appel.
type fakeConverter struct {
	store     *FakeStore
	rendition formats.Rendition
	err       error

	mu        sync.Mutex
	calls     int
	gotFamily formats.Family
	gotSrc    []byte
	gotExt    string
	gotStage  pipeline.Stage
}

func (c *fakeConverter) Convert(ctx context.Context, f formats.Format, srcPath string) (formats.Rendition, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.gotFamily = f.Family
	c.gotSrc, _ = os.ReadFile(srcPath)
	c.gotExt = filepath.Ext(srcPath)
	if c.store != nil {
		for _, j := range c.store.jobs {
			if j.Progress != nil {
				c.gotStage = j.Progress.Stage
			}
		}
	}
	return c.rendition, c.err
}

func (c *fakeConverter) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func waitStatus(t *testing.T, store *FakeStore, id string, want Status) Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		j, _, _ := store.Get(context.Background(), id)
		if j.Status == want {
			return j
		}
		time.Sleep(2 * time.Millisecond)
	}
	j, _, _ := store.Get(context.Background(), id)
	t.Fatalf("job %s status = %s, want %s (err %q)", id, j.Status, want, j.Err)
	return j
}

func TestJobManager_Submit_DetectsFormatSizeAndHash(t *testing.T) {
	store := NewFakeStore()
	m := NewJobManager(store, &fakeRunner{})
	m.WorkDir = t.TempDir()
	m.Converter = &fakeConverter{rendition: formats.Rendition{PDF: []byte("%PDF")}}

	job, err := m.Submit(context.Background(), "Budget 2026.xlsx", []byte("xlsx-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ := store.Get(context.Background(), job.ID)
	if got.Format != "sheet" || got.MIME != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" || got.Size != 10 {
		t.Errorf("stored job format=%q mime=%q size=%d", got.Format, got.MIME, got.Size)
	}
	if want := fmt.Sprintf("%x", sha256.Sum256([]byte("xlsx-bytes"))); got.SourceHash != want {
		t.Errorf("SourceHash = %q, want sha256 %q", got.SourceHash, want)
	}
	waitStatus(t, store, job.ID, StatusDone)
}

// Un Word est converti en PDF (extension d'origine conservée pour que
// LibreOffice choisisse le bon filtre), la version PDF et l'aperçu sont
// enregistrés, et c'est le PDF qui part dans le pipeline.
func TestJobManager_ConvertsThenRunsPipelineOnRendition(t *testing.T) {
	store := NewFakeStore()
	runner := &fakeRunner{result: pipeline.Result{DocType: "facture"}}
	conv := &fakeConverter{store: store, rendition: formats.Rendition{PDF: []byte("%PDF-rendition"), Preview: []byte("<table>"), PreviewMIME: "text/html"}}
	m := NewJobManager(store, runner)
	m.WorkDir = t.TempDir()
	m.Converter = conv

	job, _ := m.Submit(context.Background(), "lettre.docx", []byte("docx-bytes"))
	waitStatus(t, store, job.ID, StatusDone)

	if conv.gotFamily != formats.Word || string(conv.gotSrc) != "docx-bytes" || conv.gotExt != ".docx" {
		t.Errorf("converter got family=%s src=%q ext=%q", conv.gotFamily, conv.gotSrc, conv.gotExt)
	}
	if conv.gotStage != pipeline.StageConverting {
		t.Errorf("progress during conversion = %q, want %q", conv.gotStage, pipeline.StageConverting)
	}
	if pdf, ok, _ := store.ReadFile(context.Background(), job.ID, FileRendition); !ok || string(pdf) != "%PDF-rendition" {
		t.Errorf("stored rendition = %q ok=%v", pdf, ok)
	}
	if prev, ok, _ := store.ReadFile(context.Background(), job.ID, FilePreview); !ok || string(prev) != "<table>" {
		t.Errorf("stored preview = %q ok=%v", prev, ok)
	}
	if got, _ := os.ReadFile(runner.Path()); got != nil {
		t.Errorf("runner temp file should be cleaned up after the run")
	}
	if filepath.Ext(runner.Path()) != ".pdf" {
		t.Errorf("runner path = %q, want a .pdf (the rendition)", runner.Path())
	}
}

func TestJobManager_ConversionErrorFailsJobWithoutRunningPipeline(t *testing.T) {
	store := NewFakeStore()
	runner := &fakeRunner{}
	m := NewJobManager(store, runner)
	m.WorkDir = t.TempDir()
	m.Converter = &fakeConverter{err: errors.New("soffice: boom")}

	job, _ := m.Submit(context.Background(), "deck.pptx", []byte("pptx"))
	got := waitStatus(t, store, job.ID, StatusFailed)
	if !strings.Contains(got.Err, "conversion") || !strings.Contains(got.Err, "soffice: boom") {
		t.Errorf("Err = %q, want a conversion error with the cause", got.Err)
	}
	if runner.Path() != "" {
		t.Error("pipeline must not run when conversion failed")
	}
}

func TestJobManager_NoConverterForADocument_FailsExplicitly(t *testing.T) {
	store := NewFakeStore()
	m := NewJobManager(store, &fakeRunner{})
	m.WorkDir = t.TempDir()

	job, _ := m.Submit(context.Background(), "deck.pptx", []byte("pptx"))
	got := waitStatus(t, store, job.ID, StatusFailed)
	if !strings.Contains(got.Err, "convert") {
		t.Errorf("Err = %q, want an explicit 'no converter' error", got.Err)
	}
}

// Un zip, un dmg... sont stockés et téléchargeables, sans conversion ni
// pipeline : le job se termine tout de suite, sans résultat.
func TestJobManager_OtherFilesAreStoredOnly(t *testing.T) {
	store := NewFakeStore()
	runner := &fakeRunner{}
	conv := &fakeConverter{}
	m := NewJobManager(store, runner)
	m.WorkDir = t.TempDir()
	m.Converter = conv

	job, _ := m.Submit(context.Background(), "photos.zip", []byte("PK\x03\x04"))
	got := waitStatus(t, store, job.ID, StatusDone)
	if got.Result != nil || got.Err != "" {
		t.Errorf("stored-only job = result %v err %q, want neither", got.Result, got.Err)
	}
	if conv.Calls() != 0 || runner.Path() != "" {
		t.Error("neither conversion nor pipeline should run for a stored-only file")
	}
	if data, ok, _ := store.ReadFile(context.Background(), job.ID, FileOriginal); !ok || string(data) != "PK\x03\x04" {
		t.Errorf("original not kept: %q", data)
	}
}

// Changer le type relance l'extraction, pas la conversion : la version
// PDF déjà produite est réutilisée.
func TestJobManager_ReprocessReusesRendition(t *testing.T) {
	store := NewFakeStore()
	conv := &fakeConverter{rendition: formats.Rendition{PDF: []byte("%PDF-r")}}
	m := NewJobManager(store, &fakeRunner{result: pipeline.Result{DocType: "devis"}})
	m.WorkDir = t.TempDir()
	m.Converter = conv

	job, _ := m.Submit(context.Background(), "devis.docx", []byte("docx"))
	waitStatus(t, store, job.ID, StatusDone)
	if err := m.Reprocess(context.Background(), job.ID, "devis"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, store, job.ID, StatusDone)
	time.Sleep(20 * time.Millisecond)
	if conv.Calls() != 1 {
		t.Errorf("converter called %d times, want 1 (reprocess must reuse the rendition)", conv.Calls())
	}
}

// File d'attente globale : un seul document traité à la fois par
// défaut — déposer 20 fichiers d'un coup ne doit pas envoyer 20 appels
// simultanés aux modèles (cf. jalon 21 bis).
func TestJobManager_QueueProcessesOneJobAtATimeByDefault(t *testing.T) {
	store := NewFakeStore()
	runner := &countingRunner{proceed: make(chan struct{})}
	m := NewJobManager(store, runner)
	m.WorkDir = t.TempDir()

	a, _ := m.Submit(context.Background(), "a.pdf", []byte("%PDF-a"))
	b, _ := m.Submit(context.Background(), "b.pdf", []byte("%PDF-b"))
	time.Sleep(50 * time.Millisecond)

	if runner.Max() != 1 {
		t.Errorf("max concurrent runs = %d, want 1", runner.Max())
	}
	ja, _, _ := store.Get(context.Background(), a.ID)
	jb, _, _ := store.Get(context.Background(), b.ID)
	running, pending := ja, jb
	if ja.Status == StatusPending {
		running, pending = jb, ja
	}
	if running.Status != StatusRunning || pending.Status != StatusPending || !pending.StartedAt.IsZero() {
		t.Errorf("statuses = %s/%s (pending StartedAt %v), want one running and one pending not started", ja.Status, jb.Status, pending.StartedAt)
	}
	close(runner.proceed)
	waitStatus(t, store, a.ID, StatusDone)
	waitStatus(t, store, b.ID, StatusDone)
	if runner.Max() != 1 {
		t.Errorf("max concurrent runs = %d, want 1", runner.Max())
	}
}

type countingRunner struct {
	proceed chan struct{}
	mu      sync.Mutex
	cur     int
	max     int
}

func (r *countingRunner) enter() {
	r.mu.Lock()
	r.cur++
	if r.cur > r.max {
		r.max = r.cur
	}
	r.mu.Unlock()
}

func (r *countingRunner) leave() {
	r.mu.Lock()
	r.cur--
	r.mu.Unlock()
}

func (r *countingRunner) Max() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.max
}

func (r *countingRunner) RunAuto(ctx context.Context, path string, onProgress pipeline.ProgressFunc) (pipeline.Result, error) {
	r.enter()
	defer r.leave()
	<-r.proceed
	return pipeline.Result{}, nil
}

func (r *countingRunner) RunWithType(ctx context.Context, docType, path string, onProgress pipeline.ProgressFunc) (pipeline.Result, error) {
	return r.RunAuto(ctx, path, onProgress)
}

func TestJobManager_Thumbnail_UsesRenditionForConvertedFormats(t *testing.T) {
	store := NewFakeStore()
	renderer := &fakeThumbRenderer{png: []byte("thumb")}
	m := NewJobManager(store, &fakeRunner{})
	m.WorkDir = t.TempDir()
	m.Renderer = renderer
	m.Converter = &fakeConverter{rendition: formats.Rendition{PDF: []byte("%PDF-rendition")}}

	job, _ := m.Submit(context.Background(), "deck.pptx", []byte("pptx"))
	waitStatus(t, store, job.ID, StatusDone)
	if _, ok, err := m.Thumbnail(context.Background(), job.ID); !ok || err != nil {
		t.Fatalf("Thumbnail() ok=%v err=%v", ok, err)
	}
	if string(renderer.gotPDF) != "%PDF-rendition" {
		t.Errorf("thumbnail rendered from %q, want the PDF rendition", renderer.gotPDF)
	}
}

func TestJobManager_Thumbnail_StoredOnlyFileHasNone(t *testing.T) {
	store := NewFakeStore()
	m := NewJobManager(store, &fakeRunner{})
	m.WorkDir = t.TempDir()
	m.Renderer = &fakeThumbRenderer{png: []byte("thumb")}

	job, _ := m.Submit(context.Background(), "photos.zip", []byte("PK"))
	waitStatus(t, store, job.ID, StatusDone)
	if _, _, err := m.Thumbnail(context.Background(), job.ID); !errors.Is(err, ErrNoThumbnail) {
		t.Errorf("Thumbnail(zip) err = %v, want ErrNoThumbnail", err)
	}
}

// Jalon 27 : la file des documents peut être partagée avec l'agent des
// tickets — tant qu'un autre détenteur tient la porte, un document
// attend (StatusPending) au lieu d'appeler les modèles en même temps.
func TestJobManager_SharedGate_WaitsForOtherHolder(t *testing.T) {
	store := NewFakeStore()
	g := gate.New(1)
	release := g.Acquire() // l'agent analyse un ticket
	m := NewJobManager(store, &fakeRunner{})
	m.WorkDir = t.TempDir()
	m.Gate = g

	job, _ := m.Submit(context.Background(), "a.pdf", []byte("%PDF"))
	time.Sleep(50 * time.Millisecond)
	if got, _, _ := store.Get(context.Background(), job.ID); got.Status != StatusPending {
		t.Errorf("status while the gate is held = %s, want pending", got.Status)
	}
	release()
	waitStatus(t, store, job.ID, StatusDone)
}

// Bug trouvé par un test devenu intermittent (jalon 27) : process()
// réécrivait le job à partir de sa copie prise au démarrage — des tags
// posés pendant le traitement (plusieurs minutes pour un scan) étaient
// effacés à la fin, sans le moindre signal.
func TestJobManager_TagsSetDuringProcessingSurvive(t *testing.T) {
	store := NewFakeStore()
	runner := &fakeRunner{started: make(chan struct{}), proceed: make(chan struct{}), result: pipeline.Result{DocType: "facture"}}
	m := NewJobManager(store, runner)
	m.WorkDir = t.TempDir()

	job, _ := m.Submit(context.Background(), "a.pdf", []byte("%PDF"))
	<-runner.started
	if err := m.SetTags(context.Background(), job.ID, []string{"urgent"}); err != nil {
		t.Fatal(err)
	}
	close(runner.proceed)
	done := waitStatus(t, store, job.ID, StatusDone)
	if len(done.Tags) != 1 || done.Tags[0] != "urgent" {
		t.Errorf("tags after processing = %v, want [urgent] (set while running)", done.Tags)
	}
	if done.DocType != "facture" {
		t.Errorf("DocType = %q, the processing result must still be written", done.DocType)
	}
}

// --- Commentaire (ticket "Ajouter commentaire sur document") ---

func TestJobManager_SetComment_UpdatesStoredComment(t *testing.T) {
	m := newTestJobManager(&fakeRunner{})
	job, err := m.Submit(context.Background(), "doc.pdf", []byte("content"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetComment(context.Background(), job.ID, "  Payée le 12/09  \n"); err != nil {
		t.Fatalf("SetComment() error = %v, want nil", err)
	}
	got, ok, err := m.Get(context.Background(), job.ID)
	if err != nil || !ok {
		t.Fatalf("Get() = %+v, %v, %v", got, ok, err)
	}
	if got.Comment != "Payée le 12/09" {
		t.Errorf("got.Comment = %q, want the trimmed comment", got.Comment)
	}
}

func TestJobManager_SetComment_UnknownJobID_ReturnsError(t *testing.T) {
	m := newTestJobManager(&fakeRunner{})
	if err := m.SetComment(context.Background(), "does-not-exist", "x"); err == nil {
		t.Fatal("SetComment() error = nil, want non-nil for an unknown job id")
	}
}

// Même piège que les tags (jalon 27) : un commentaire écrit pendant le
// traitement ne doit pas être effacé quand le traitement se termine.
func TestJobManager_CommentSetDuringProcessingSurvives(t *testing.T) {
	store := NewFakeStore()
	runner := &fakeRunner{started: make(chan struct{}), proceed: make(chan struct{}), result: pipeline.Result{DocType: "facture"}}
	m := NewJobManager(store, runner)
	m.WorkDir = t.TempDir()

	job, _ := m.Submit(context.Background(), "a.pdf", []byte("%PDF"))
	<-runner.started
	if err := m.SetComment(context.Background(), job.ID, "à vérifier"); err != nil {
		t.Fatal(err)
	}
	close(runner.proceed)
	done := waitStatus(t, store, job.ID, StatusDone)
	if done.Comment != "à vérifier" {
		t.Errorf("comment after processing = %q, want it kept (set while running)", done.Comment)
	}
}

// Une erreur de conversion est reconnaissable (la fiche n'a alors pas de
// version PDF à montrer) ; une autre erreur ne l'est pas.
func TestConversionFailed(t *testing.T) {
	conv := Job{Status: StatusFailed, Err: conversionError("contrat.docx", errors.New("source file could not be loaded")).Error()}
	if !ConversionFailed(conv) {
		t.Error("conversion error not recognised")
	}
	for _, j := range []Job{
		{Status: StatusFailed, Err: "vlm: délai dépassé"},
		{Status: StatusDone, Err: conv.Err},
	} {
		if ConversionFailed(j) {
			t.Errorf("ConversionFailed(%+v) = true", j)
		}
	}
}

// Jalon 37 : un document charge le profil « documents » avant son
// traitement ; si la bascule échoue, le document échoue avec la raison
// (plutôt qu'une connexion refusée plus loin).
func TestJobManager_SwitchesToDocumentModels(t *testing.T) {
	store := NewFakeStore()
	g := gate.New(1)
	var profiles []string
	g.Switch = func(ctx context.Context, p string) error { profiles = append(profiles, p); return nil }
	m := NewJobManager(store, &fakeRunner{result: pipeline.Result{DocType: "facture"}})
	m.WorkDir, m.Gate = t.TempDir(), g
	job, _ := m.Submit(context.Background(), "a.pdf", []byte("%PDF"))
	waitStatus(t, store, job.ID, StatusDone)
	if len(profiles) != 1 || profiles[0] != gate.Documents {
		t.Errorf("profiles = %v", profiles)
	}

	g.Switch = func(ctx context.Context, p string) error { return errors.New("VLM jamais prêt") }
	job, _ = m.Submit(context.Background(), "b.pdf", []byte("%PDF"))
	failed := waitStatus(t, store, job.ID, StatusFailed)
	if !strings.Contains(failed.Err, "VLM jamais prêt") {
		t.Errorf("err = %q", failed.Err)
	}
}

// Constat du jalon 39 : SetTags/SetComment relisaient puis réécrivaient
// tout le job — une fin de traitement dans la même fenêtre était annulée
// (statut remis à "running"), ou l'inverse. Ils passent désormais par
// l'écriture ciblée du store, jamais par Update.
func TestJobManager_SetTagsAndComment_NeverRewriteTheWholeJob(t *testing.T) {
	store := &updateFailingStore{FakeStore: NewFakeStore()}
	ctx := context.Background()
	store.Create(ctx, Job{ID: "a", Status: StatusDone})
	m := NewJobManager(store, &fakeRunner{})

	if err := m.SetTags(ctx, "a", []string{"urgent"}); err != nil {
		t.Errorf("SetTags() error = %v, want a targeted write (no Update)", err)
	}
	if err := m.SetComment(ctx, "a", " à vérifier "); err != nil {
		t.Errorf("SetComment() error = %v, want a targeted write (no Update)", err)
	}
	got, _, _ := store.Get(ctx, "a")
	if len(got.Tags) != 1 || got.Tags[0] != "urgent" || got.Comment != "à vérifier" {
		t.Errorf("tags=%v comment=%q, want [urgent] and the trimmed comment", got.Tags, got.Comment)
	}
}
