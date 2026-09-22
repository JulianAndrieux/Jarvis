package webapp

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

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
