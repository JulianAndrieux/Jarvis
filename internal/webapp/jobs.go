// Package webapp orchestre le pipeline jarvis pour une interface web :
// soumission d'un document, suivi asynchrone d'un job, récupération du
// résultat une fois prêt. Aucune dépendance HTTP ici — c'est cmd/jarvisapp
// qui expose ça sur le réseau.
package webapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"time"

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
// DocType n'est connu qu'une fois le job terminé (StatusDone) —
// déterminé par classification automatique (internal/classify), pas
// choisi par l'utilisateur à l'upload : "" tant que le job n'est pas
// terminé, et peut rester "" même terminé si aucun type ne correspond.
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
	// Tags est librement éditable par l'utilisateur (bibliothèque de
	// documents, jalon 17) — n'a aucune incidence sur le traitement.
	Tags []string
	// SearchText est le texte du document (natif ou Markdown VLM, cf.
	// pipeline.Result.SearchText) — recopié ici uniquement pour que
	// Store.List puisse chercher dedans sans désérialiser tout Result
	// (jalon 18, "chercher dans les documents").
	SearchText string
}

// Runner exécute le pipeline complet pour un document, à partir d'un
// chemin de fichier local (les outils sous-jacents — pdftotext, pdftoppm —
// opèrent sur des fichiers). RunAuto détermine lui-même le type de
// document par classification automatique ; RunWithType l'impose
// explicitement — utilisé quand l'utilisateur réattribue manuellement le
// type d'un document (bibliothèque de documents, jalon 17). Les deux sont
// satisfaites par pipeline.Pipeline par typage structurel — une fake
// suffit pour les tests, aucun modèle ni GPU requis.
type Runner interface {
	RunAuto(ctx context.Context, path string) (pipeline.Result, error)
	RunWithType(ctx context.Context, docType, path string) (pipeline.Result, error)
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
	store  Store
	runner Runner

	// WorkDir est le répertoire des fichiers temporaires de traitement ;
	// "" laisse os.CreateTemp choisir (répertoire temporaire du système).
	WorkDir string

	// OnFinish, si non-nil, est appelé (depuis la goroutine du job) une
	// fois le job terminé (Done ou Failed), avec l'état final du job.
	OnFinish func(job Job)

	newID func() (string, error) // injectable pour les tests
}

func NewJobManager(store Store, runner Runner) *JobManager {
	return &JobManager{
		store:  store,
		runner: runner,
		newID:  randomID,
	}
}

// Submit enregistre un nouveau job pour content et lance son traitement en
// arrière-plan. Le type de document n'est pas demandé : il est déterminé
// automatiquement pendant le traitement (classification). Retourne
// immédiatement avec le job à l'état StatusPending.
func (m *JobManager) Submit(ctx context.Context, filename string, content []byte) (Job, error) {
	id, err := m.newID()
	if err != nil {
		return Job{}, fmt.Errorf("webapp: generate job id: %w", err)
	}

	job := Job{
		ID: id, Filename: filename, Content: content,
		Status: StatusPending, CreatedAt: time.Now(),
	}

	created, err := m.store.Create(ctx, job)
	if err != nil {
		return Job{}, fmt.Errorf("webapp: create job: %w", err)
	}

	go m.run(created)

	return created, nil
}

func (m *JobManager) run(job Job) {
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

	result, err := m.runner.RunAuto(ctx, path)
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
		job.DocType = result.DocType
		job.SearchText = result.SearchText
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

// Delete supprime définitivement le job id (bibliothèque de documents,
// jalon 18) — délègue directement à Store.Delete. Ne touche pas à la
// copie locale additionnelle éventuelle (--out-dir) : celle-ci reste un
// filet de secours indépendant, jamais purgé automatiquement.
func (m *JobManager) Delete(ctx context.Context, id string) error {
	return m.store.Delete(ctx, id)
}

// List retourne les jobs correspondant à q (bibliothèque de documents,
// jalon 17) — délègue directement à Store.List.
func (m *JobManager) List(ctx context.Context, q ListQuery) ([]Job, error) {
	return m.store.List(ctx, q)
}

// SetTags remplace les tags du job id — n'a aucune incidence sur le
// traitement, purement de l'organisation côté utilisateur.
func (m *JobManager) SetTags(ctx context.Context, id string, tags []string) error {
	job, ok, err := m.store.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("webapp: set tags %s: get: %w", id, err)
	}
	if !ok {
		return fmt.Errorf("webapp: set tags %s: not found", id)
	}
	job.Tags = tags
	if err := m.store.Update(ctx, job); err != nil {
		return fmt.Errorf("webapp: set tags %s: %w", id, err)
	}
	return nil
}

// Reprocess relance le traitement du job id avec un type de document
// choisi explicitement (docType), sans repasser par la classification —
// répond à "changer le type sur la base des types existants". Le PDF
// source (déjà en base, dans job.Content) est rematérialisé ; jamais
// redemandé à l'utilisateur. Asynchrone, comme Submit : retourne dès que
// le job passe à StatusRunning, le résultat s'obtient via Get comme pour
// un job normal.
func (m *JobManager) Reprocess(ctx context.Context, id, docType string) error {
	job, ok, err := m.store.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("webapp: reprocess %s: get: %w", id, err)
	}
	if !ok {
		return fmt.Errorf("webapp: reprocess %s: not found", id)
	}

	job.Status = StatusRunning
	if err := m.store.Update(ctx, job); err != nil {
		fmt.Fprintf(os.Stderr, "webapp: update job %s to running: %v\n", job.ID, err)
	}

	go func() {
		ctx := context.Background()
		path, cleanup, err := m.materialize(job.Content)
		if err != nil {
			m.finish(ctx, job, pipeline.Result{}, fmt.Errorf("webapp: write temp file: %w", err))
			return
		}
		defer cleanup()

		result, err := m.runner.RunWithType(ctx, docType, path)
		m.finish(ctx, job, result, err)
	}()

	return nil
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("webapp: random id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
