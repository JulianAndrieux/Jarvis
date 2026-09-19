package webapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/doctype"
	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
)

// fakeRunner permet de contrôler précisément le timing d'exécution dans
// les tests : started se ferme dès que Run() est appelé, et Run() bloque
// jusqu'à ce que proceed soit fermé (si non-nil).
type fakeRunner struct {
	result  pipeline.Result
	err     error
	started chan struct{}
	proceed chan struct{}
}

func (f *fakeRunner) Run(ctx context.Context, reg doctype.Registration, path string) (pipeline.Result, error) {
	if f.started != nil {
		close(f.started)
	}
	if f.proceed != nil {
		<-f.proceed
	}
	return f.result, f.err
}

func waitForStatus(t *testing.T, m *JobManager, id string, want Status) Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, ok := m.Get(id)
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

func testRegistry(t *testing.T) *doctype.Registry {
	t.Helper()
	return doctype.NewDefaultRegistry()
}

func TestJobManager_Submit_UnknownDocType_ReturnsError(t *testing.T) {
	m := NewJobManager(&fakeRunner{}, testRegistry(t))

	_, err := m.Submit("extraterrestre", "doc.pdf", "/tmp/doc.pdf")
	if err == nil {
		t.Fatal("Submit() error = nil, want non-nil for an unregistered doc type")
	}
}

func TestJobManager_Submit_StartsPendingThenRunning(t *testing.T) {
	started := make(chan struct{})
	proceed := make(chan struct{})
	runner := &fakeRunner{started: started, proceed: proceed}
	m := NewJobManager(runner, testRegistry(t))
	defer close(proceed)

	job, err := m.Submit("facture", "doc.pdf", "/tmp/doc.pdf")
	if err != nil {
		t.Fatalf("Submit() error = %v, want nil", err)
	}
	if job.DocType != "facture" || job.Filename != "doc.pdf" {
		t.Errorf("job = %+v, want DocType=facture Filename=doc.pdf", job)
	}

	<-started // le runner a bien été invoqué de façon asynchrone
	waitForStatus(t, m, job.ID, StatusRunning)
}

func TestJobManager_RunSucceeds_SetsStatusDoneWithResult(t *testing.T) {
	wantResult := pipeline.Result{Path: "/tmp/doc.pdf"}
	runner := &fakeRunner{result: wantResult}
	m := NewJobManager(runner, testRegistry(t))

	job, err := m.Submit("facture", "doc.pdf", "/tmp/doc.pdf")
	if err != nil {
		t.Fatal(err)
	}

	done := waitForStatus(t, m, job.ID, StatusDone)
	if done.Result == nil || done.Result.Path != "/tmp/doc.pdf" {
		t.Errorf("done.Result = %+v, want %+v", done.Result, wantResult)
	}
	if done.Err != "" {
		t.Errorf("done.Err = %q, want empty", done.Err)
	}
}

func TestJobManager_RunFails_SetsStatusFailedWithError(t *testing.T) {
	runner := &fakeRunner{err: errors.New("pipeline boom")}
	m := NewJobManager(runner, testRegistry(t))

	job, err := m.Submit("facture", "doc.pdf", "/tmp/doc.pdf")
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
	m := NewJobManager(&fakeRunner{}, testRegistry(t))

	_, ok := m.Get("does-not-exist")
	if ok {
		t.Error("Get() ok = true, want false for an unknown job id")
	}
}

func TestJobManager_OnFinish_CalledAfterCompletion(t *testing.T) {
	runner := &fakeRunner{result: pipeline.Result{Path: "/tmp/doc.pdf"}}
	m := NewJobManager(runner, testRegistry(t))

	called := make(chan Job, 1)
	m.OnFinish = func(job Job) { called <- job }

	job, err := m.Submit("facture", "doc.pdf", "/tmp/doc.pdf")
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
	m := NewJobManager(&fakeRunner{}, testRegistry(t))

	job1, err := m.Submit("facture", "a.pdf", "/tmp/a.pdf")
	if err != nil {
		t.Fatal(err)
	}
	job2, err := m.Submit("facture", "b.pdf", "/tmp/b.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if job1.ID == job2.ID {
		t.Errorf("job1.ID == job2.ID (%q), want unique ids", job1.ID)
	}
}
