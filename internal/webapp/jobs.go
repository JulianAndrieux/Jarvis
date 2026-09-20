// Package webapp orchestre le pipeline jarvis pour une interface web :
// soumission d'un document, suivi asynchrone d'un job, récupération du
// résultat une fois prêt. Aucune dépendance HTTP ici — c'est cmd/jarvisweb
// qui expose ça sur le réseau.
package webapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/doctype"
	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
)

// Status est l'état d'avancement d'un job.
type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
)

// Job est le suivi d'une soumission de document. Content est le PDF
// source lui-même (persisté via Store, ex. MongoDB — voir CLAUDE.md pour
// la portée de l'exception à "aucune donnée ne sort de la machine").
// Result n'est renseigné que si Status == StatusDone ; Err seulement si
// Status == StatusFailed.
type Job struct {
	ID         string
	DocType    string
	Filename   string
	Content    []byte
	Status     Status
	CreatedAt  time.Time
	FinishedAt time.Time
	Result     *pipeline.Result
	Err        string
}

// Runner exécute le pipeline complet pour un document, à partir d'un
// chemin de fichier local (les outils sous-jacents — pdftotext, pdftoppm —
// opèrent sur des fichiers). pipeline.Pipeline satisfait cette interface
// (typage structurel) ; une fake suffit pour les tests, aucun modèle ni
// GPU requis.
type Runner interface {
	Run(ctx context.Context, reg doctype.Registration, path string) (pipeline.Result, error)
}

// JobManager orchestre la soumission et le traitement asynchrone des
// jobs. La persistance (Store) est injectée : FakeStore en test, une
// implémentation réelle (ex. MongoStore) en production — JobManager ne
// sait pas laquelle.
//
// Le pipeline étant basé sur des chemins de fichiers, JobManager
// matérialise Content dans un fichier temporaire (sous WorkDir) le temps
// du traitement, puis le supprime — que le traitement réussisse ou non.
type JobManager struct {
	store    Store
	runner   Runner
	registry *doctype.Registry

	// WorkDir est le répertoire des fichiers temporaires de traitement ;
	// "" laisse os.CreateTemp choisir (répertoire temporaire du système).
	WorkDir string

	// OnFinish, si non-nil, est appelé (depuis la goroutine du job) une
	// fois le job terminé (Done ou Failed), avec l'état final du job.
	OnFinish func(job Job)

	newID func() (string, error) // injectable pour les tests
}

func NewJobManager(store Store, runner Runner, registry *doctype.Registry) *JobManager {
	return &JobManager{
		store:    store,
		runner:   runner,
		registry: registry,
		newID:    randomID,
	}
}

// Submit enregistre un nouveau job pour content et lance son traitement en
// arrière-plan. Retourne immédiatement avec le job à l'état StatusPending.
func (m *JobManager) Submit(ctx context.Context, docType, filename string, content []byte) (Job, error) {
	reg, ok := m.registry.Get(docType)
	if !ok {
		return Job{}, fmt.Errorf("webapp: unknown doc type %q", docType)
	}

	id, err := m.newID()
	if err != nil {
		return Job{}, fmt.Errorf("webapp: generate job id: %w", err)
	}

	job := Job{
		ID: id, DocType: docType, Filename: filename, Content: content,
		Status: StatusPending, CreatedAt: time.Now(),
	}

	created, err := m.store.Create(ctx, job)
	if err != nil {
		return Job{}, fmt.Errorf("webapp: create job: %w", err)
	}

	go m.run(created, reg)

	return created, nil
}

func (m *JobManager) run(job Job, reg doctype.Registration) {
	ctx := context.Background()

	job.Status = StatusRunning
	if err := m.store.Update(ctx, job); err != nil {
		fmt.Fprintf(os.Stderr, "webapp: update job %s to running: %v\n", job.ID, err)
	}

	path, cleanup, err := m.materialize(job.Content)
	if err != nil {
		m.finish(ctx, job, pipeline.Result{}, fmt.Errorf("webapp: write temp file: %w", err))
		return
	}
	defer cleanup()

	result, err := m.runner.Run(ctx, reg, path)
	m.finish(ctx, job, result, err)
}

func (m *JobManager) finish(ctx context.Context, job Job, result pipeline.Result, runErr error) {
	job.FinishedAt = time.Now()
	if runErr != nil {
		job.Status = StatusFailed
		job.Err = runErr.Error()
	} else {
		job.Status = StatusDone
		job.Result = &result
	}

	if err := m.store.Update(ctx, job); err != nil {
		fmt.Fprintf(os.Stderr, "webapp: update job %s: %v\n", job.ID, err)
	}

	if m.OnFinish != nil {
		m.OnFinish(job)
	}
}

// materialize écrit content dans un fichier temporaire sous m.WorkDir et
// retourne son chemin ainsi qu'une fonction pour le supprimer.
func (m *JobManager) materialize(content []byte) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp(m.WorkDir, "jarvisweb-job-*.pdf")
	if err != nil {
		return "", func() {}, err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", func() {}, err
	}
	path = f.Name()
	return path, func() { os.Remove(path) }, nil
}

// Get retourne le job id, s'il existe.
func (m *JobManager) Get(ctx context.Context, id string) (Job, bool, error) {
	return m.store.Get(ctx, id)
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("webapp: random id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
