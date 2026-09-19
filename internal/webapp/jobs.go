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
	"sync"
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

// Job est le suivi d'une soumission de document. Result n'est renseigné
// que si Status == StatusDone ; Err seulement si Status == StatusFailed.
type Job struct {
	ID         string
	DocType    string
	Filename   string
	Path       string
	Status     Status
	CreatedAt  time.Time
	FinishedAt time.Time
	Result     *pipeline.Result
	Err        string
}

// Runner exécute le pipeline complet pour un document. pipeline.Pipeline
// satisfait cette interface (typage structurel) ; une fake suffit pour
// les tests, aucun modèle ni GPU requis.
type Runner interface {
	Run(ctx context.Context, reg doctype.Registration, path string) (pipeline.Result, error)
}

// JobManager suit les jobs en mémoire (perdus au redémarrage — acceptable
// pour un usage local ; les résultats, eux, sont persistés sur disque via
// internal/store, cf. OnFinish côté cmd/jarvisweb).
type JobManager struct {
	mu       sync.Mutex
	jobs     map[string]*Job
	runner   Runner
	registry *doctype.Registry

	// OnFinish, si non-nil, est appelé (depuis la goroutine du job) une
	// fois le job terminé (Done ou Failed), avec une copie du job.
	OnFinish func(job Job)

	newID func() (string, error) // injectable pour les tests
}

func NewJobManager(runner Runner, registry *doctype.Registry) *JobManager {
	return &JobManager{
		jobs:     map[string]*Job{},
		runner:   runner,
		registry: registry,
		newID:    randomID,
	}
}

// Submit enregistre un nouveau job pour path (déjà écrit sur disque par
// l'appelant) et lance son traitement en arrière-plan. Retourne
// immédiatement avec le job à l'état StatusPending.
func (m *JobManager) Submit(docType, filename, path string) (Job, error) {
	reg, ok := m.registry.Get(docType)
	if !ok {
		return Job{}, fmt.Errorf("webapp: unknown doc type %q", docType)
	}

	id, err := m.newID()
	if err != nil {
		return Job{}, fmt.Errorf("webapp: generate job id: %w", err)
	}

	job := &Job{
		ID: id, DocType: docType, Filename: filename, Path: path,
		Status: StatusPending, CreatedAt: time.Now(),
	}
	// Copié avant tout partage de job entre goroutines : aucune donnée
	// concurrente à ce stade, donc aucune synchronisation nécessaire pour
	// cette lecture.
	snapshot := *job

	m.mu.Lock()
	m.jobs[id] = job
	m.mu.Unlock()

	go m.run(job, reg)

	return snapshot, nil
}

func (m *JobManager) run(job *Job, reg doctype.Registration) {
	m.mu.Lock()
	job.Status = StatusRunning
	m.mu.Unlock()

	result, err := m.runner.Run(context.Background(), reg, job.Path)

	m.mu.Lock()
	job.FinishedAt = time.Now()
	if err != nil {
		job.Status = StatusFailed
		job.Err = err.Error()
	} else {
		job.Status = StatusDone
		job.Result = &result
	}
	snapshot := *job
	m.mu.Unlock()

	if m.OnFinish != nil {
		m.OnFinish(snapshot)
	}
}

// Get retourne une copie du job id, si connu.
func (m *JobManager) Get(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("webapp: random id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
