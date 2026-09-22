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

	"github.com/JulianAndrieux/Jarvis/internal/parsing"
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
	ID        string
	DocType   string
	Filename  string
	Content   []byte
	Status    Status
	CreatedAt time.Time
	// StartedAt est l'instant où le traitement EN COURS a commencé —
	// distinct de CreatedAt (l'upload initial) : un job relancé
	// manuellement (Reprocess, jalon 17) garde son CreatedAt d'origine,
	// mais StartedAt est réinitialisé à chaque nouvelle tentative.
	// Répond à la demande "un timestamp de début" (jalon 20) — sans lui,
	// rien ne dit depuis quand le traitement affiché est réellement en
	// cours, en particulier après une ré-extraction.
	StartedAt  time.Time
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
	// Thumbnail est la miniature PNG de la première page (jalon 22,
	// grille de la bibliothèque de documents), générée à la demande par
	// JobManager.Thumbnail puis persistée via Store.SetThumbnail. nil tant
	// qu'elle n'a jamais été demandée.
	Thumbnail []byte
	// Progress est l'avancement du dernier traitement (jalon 23) : étape,
	// compteurs de pages, texte déjà lu. Écrit au fil de l'eau via
	// Store.SetProgress, effacé au début de chaque nouvelle tentative, et
	// conservé ensuite — un job interrompu garde ainsi les pages déjà
	// lues. Sans objet une fois Result disponible (Result.Pages fait foi).
	Progress *pipeline.Progress
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
	RunAuto(ctx context.Context, path string, onProgress pipeline.ProgressFunc) (pipeline.Result, error)
	RunWithType(ctx context.Context, docType, path string, onProgress pipeline.ProgressFunc) (pipeline.Result, error)
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

	// Renderer rend la première page en PNG pour les miniatures (jalon
	// 22) — le même port que l'étage Parsing (PdftoppmRenderer en
	// production). nil : Thumbnail retourne une erreur explicite.
	Renderer parsing.Renderer

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
	job.StartedAt = time.Now()
	if err := m.store.Update(ctx, job); err != nil {
		fmt.Fprintf(os.Stderr, "webapp: update job %s to running: %v\n", job.ID, err)
	}

	path, cleanup, err := m.materialize(job.Content)
	if err != nil {
		m.finish(ctx, job, pipeline.Result{}, fmt.Errorf("webapp: write temp file: %w", err))
		return
	}
	defer cleanup()

	result, err := m.runner.RunAuto(ctx, path, m.progressRecorder(ctx, job.ID))
	m.finish(ctx, job, result, err)
}

// progressRecorder enregistre chaque étape d'avancement du job id. Un
// échec d'écriture est journalisé sans interrompre le traitement : le
// suivi est un confort, le résultat final reste enregistré par finish.
func (m *JobManager) progressRecorder(ctx context.Context, id string) pipeline.ProgressFunc {
	return func(p pipeline.Progress) {
		if err := m.store.SetProgress(ctx, id, &p); err != nil {
			fmt.Fprintf(os.Stderr, "webapp: store progress %s: %v\n", id, err)
		}
	}
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

// ThumbnailDPI est la résolution de rendu des miniatures : ~330x470 px
// pour une page A4, assez net pour une carte de la grille (affichée
// ~160 px de large, x2 pour les écrans Retina) et quelques dizaines de Ko.
const ThumbnailDPI = 40

// Thumbnail retourne la miniature PNG de la première page du job id,
// en la générant (puis la persistant) au premier appel — les documents
// existants avant le jalon 22 n'en ont pas, et la générer à la demande
// évite toute migration. ok=false (err=nil) si le job n'existe pas.
// Un échec de rendu n'est jamais mémorisé : l'appel suivant réessaie.
func (m *JobManager) Thumbnail(ctx context.Context, id string) ([]byte, bool, error) {
	job, ok, err := m.store.Get(ctx, id)
	if err != nil || !ok {
		return nil, ok, err
	}
	if len(job.Thumbnail) > 0 {
		return job.Thumbnail, true, nil
	}
	if m.Renderer == nil {
		return nil, true, fmt.Errorf("webapp: thumbnail %s: no renderer configured", id)
	}

	path, cleanup, err := m.materialize(job.Content)
	if err != nil {
		return nil, true, fmt.Errorf("webapp: thumbnail %s: write temp file: %w", id, err)
	}
	defer cleanup()

	png, err := m.Renderer.RenderPage(ctx, path, 1, ThumbnailDPI)
	if err != nil {
		return nil, true, fmt.Errorf("webapp: thumbnail %s: %w", id, err)
	}
	if err := m.store.SetThumbnail(ctx, id, png); err != nil {
		// La miniature est valide, seule sa mise en cache a échoué : on
		// la sert quand même, elle sera regénérée au prochain appel.
		fmt.Fprintf(os.Stderr, "webapp: store thumbnail %s: %v\n", id, err)
	}
	return png, true, nil
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

// RecoverOrphaned marque en échec tout job resté StatusPending ou
// StatusRunning — trouvé au redémarrage (jalon 20), après un incident
// réel où un job restait bloqué "running" pour toujours : la goroutine
// qui l'aurait terminé appartenait à un process précédent, disparue
// avec lui. Aucun état de reprise n'est persisté, donc ces jobs ne
// peuvent par construction jamais aboutir — les laisser tels quels
// affiche un fragment qui sonde indéfiniment dans le vide plutôt qu'un
// message actionnable. Retourne le nombre de jobs récupérés, pour le
// journaliser au démarrage.
func (m *JobManager) RecoverOrphaned(ctx context.Context) (int, error) {
	n := 0
	for _, status := range []Status{StatusRunning, StatusPending} {
		jobs, err := m.store.List(ctx, ListQuery{Status: status, Limit: DefaultListLimit})
		if err != nil {
			return n, fmt.Errorf("webapp: recover orphaned (%s): %w", status, err)
		}
		for _, job := range jobs {
			job.Status = StatusFailed
			job.Err = "traitement interrompu par un redémarrage du serveur — relance-le (changer le type relance l'extraction)"
			job.FinishedAt = time.Now()
			if err := m.store.Update(ctx, job); err != nil {
				fmt.Fprintf(os.Stderr, "webapp: recover orphaned job %s: %v\n", job.ID, err)
				continue
			}
			n++
		}
	}
	return n, nil
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
	job.StartedAt = time.Now()
	if err := m.store.Update(ctx, job); err != nil {
		fmt.Fprintf(os.Stderr, "webapp: update job %s to running: %v\n", job.ID, err)
	}
	if err := m.store.SetProgress(ctx, job.ID, nil); err != nil {
		fmt.Fprintf(os.Stderr, "webapp: clear progress %s: %v\n", job.ID, err)
	}

	go func() {
		ctx := context.Background()
		path, cleanup, err := m.materialize(job.Content)
		if err != nil {
			m.finish(ctx, job, pipeline.Result{}, fmt.Errorf("webapp: write temp file: %w", err))
			return
		}
		defer cleanup()

		result, err := m.runner.RunWithType(ctx, docType, path, m.progressRecorder(ctx, job.ID))
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
