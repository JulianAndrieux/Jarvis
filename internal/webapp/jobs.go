// Package webapp orchestre le pipeline jarvis pour une interface web :
// soumission d'un document, suivi asynchrone d'un job, récupération du
// résultat une fois prêt. Aucune dépendance HTTP ici — c'est cmd/jarvisapp
// qui expose ça sur le réseau.
package webapp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/formats"
	"github.com/JulianAndrieux/Jarvis/internal/gate"
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
	// Comment est le commentaire libre de l'utilisateur sur le document
	// (ticket "Ajouter commentaire sur document"). Ses mots sont trouvés
	// par la barre de recherche, comme les tags.
	Comment string
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

	// Format (famille : "pdf", "word", "sheet"... cf. internal/formats),
	// MIME et Size décrivent le fichier déposé, détectés à la soumission
	// (jalon 25). SourceHash est son SHA-256 (hexadécimal) — la
	// provenance exigée par le brief, calculée une fois pour toutes.
	Format     string
	MIME       string
	Size       int64
	SourceHash string
}

// ErrNoThumbnail : le fichier n'a pas de miniature (fichier seulement
// stocké, sans version PDF) — l'interface affiche alors une icône.
var ErrNoThumbnail = errors.New("webapp: no thumbnail for this file type")

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

	// Converter produit la version PDF des fichiers non-PDF (jalon 25) ;
	// nil : un tel fichier échoue avec une erreur explicite.
	Converter formats.Converter

	// Concurrency borne le nombre de documents traités simultanément
	// (file d'attente globale, jalon 25) ; 0 -> 1. Un dépôt de nombreux
	// fichiers d'un coup ne doit pas envoyer autant d'appels simultanés
	// aux modèles (cf. jalon 21 bis). Les jobs en attente restent
	// StatusPending jusqu'à leur tour.
	Concurrency int
	semOnce     sync.Once
	sem         chan struct{}
	// Gate, si non-nil, remplace la file propre au JobManager par une
	// file partagée avec l'agent des tickets (jalon 27) : documents et
	// tickets n'appellent jamais les modèles en même temps.
	Gate *gate.Gate

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
	return m.SubmitWithTags(ctx, filename, content, nil)
}

// SubmitWithTags : Submit, avec des tags posés dès la création du job —
// jamais juste après (le traitement, déjà lancé, pourrait réécrire le job
// entre-temps ; vu en réel au jalon 39).
func (m *JobManager) SubmitWithTags(ctx context.Context, filename string, content []byte, tags []string) (Job, error) {
	id, err := m.newID()
	if err != nil {
		return Job{}, fmt.Errorf("webapp: generate job id: %w", err)
	}

	head := content
	if len(head) > 512 {
		head = head[:512]
	}
	f := formats.Detect(filename, head)
	job := Job{
		ID: id, Filename: filename, Content: content, Tags: tags,
		Status: StatusPending, CreatedAt: time.Now(),
		Format: string(f.Family), MIME: f.MIME, Size: int64(len(content)),
		SourceHash: fmt.Sprintf("%x", sha256.Sum256(content)),
	}

	created, err := m.store.Create(ctx, job)
	if err != nil {
		return Job{}, fmt.Errorf("webapp: create job: %w", err)
	}

	go m.process(created, m.runner.RunAuto)

	return created, nil
}

// runFunc est l'appel au pipeline d'un traitement : RunAuto pour un
// dépôt, RunWithType pour une ré-extraction avec un type imposé.
type runFunc func(ctx context.Context, path string, onProgress pipeline.ProgressFunc) (pipeline.Result, error)

// process exécute un traitement complet de job, à son tour dans la file
// d'attente : conversion éventuelle en PDF (une seule fois, réutilisée
// ensuite), puis pipeline sur le PDF. Les fichiers seulement stockés
// (zip, dmg...) se terminent sans conversion ni pipeline.
func (m *JobManager) process(job Job, run runFunc) {
	ctx := context.Background()
	release, err := m.acquire(ctx)
	if err != nil {
		// Bascule vers les modèles de documents impossible (jalon 37) :
		// la raison plutôt qu'une connexion refusée plus loin.
		job.StartedAt = time.Now()
		m.finish(ctx, job, pipeline.Result{}, fmt.Errorf("modèles de documents indisponibles : %w", err))
		return
	}
	defer release()

	job.Status = StatusRunning
	job.StartedAt = time.Now()
	if err := m.save(ctx, job); err != nil {
		fmt.Fprintf(os.Stderr, "webapp: update job %s to running: %v\n", job.ID, err)
	}

	fam := familyOf(job)
	if !fam.Pipeline() {
		m.finishStored(ctx, job)
		return
	}

	input, err := m.pipelineInput(ctx, job, fam)
	if err != nil {
		m.finish(ctx, job, pipeline.Result{}, err)
		return
	}
	path, cleanup, err := m.materialize(input, ".pdf")
	if err != nil {
		m.finish(ctx, job, pipeline.Result{}, fmt.Errorf("webapp: write temp file: %w", err))
		return
	}
	result, err := run(ctx, path, m.progressRecorder(ctx, job.ID))
	// Supprimer avant d'enregistrer la fin : qui voit le job terminé ne
	// doit plus trouver son fichier temporaire (trouvé par un test devenu
	// intermittent sous charge, jalon 30).
	cleanup()
	m.finish(ctx, job, result, err)
}

// acquire réserve une place dans la file globale (et y charge les
// modèles de documents, jalon 37) et retourne la fonction qui la libère.
func (m *JobManager) acquire(ctx context.Context) (func(), error) {
	if m.Gate != nil {
		return m.Gate.AcquireFor(ctx, gate.Documents)
	}
	m.semOnce.Do(func() {
		n := m.Concurrency
		if n <= 0 {
			n = 1
		}
		m.sem = make(chan struct{}, n)
	})
	m.sem <- struct{}{}
	return func() { <-m.sem }, nil
}

// familyOf retourne la famille du fichier d'un job ; un job antérieur au
// jalon 25 (Format vide) était forcément un PDF.
func familyOf(job Job) formats.Family {
	if job.Format == "" {
		return formats.PDF
	}
	return formats.Family(job.Format)
}

// pipelineInput retourne le PDF à traiter : l'original pour un PDF, sinon
// la version PDF — produite et enregistrée au premier passage (avec
// l'aperçu natif éventuel), réutilisée ensuite (ré-extraction).
func (m *JobManager) pipelineInput(ctx context.Context, job Job, fam formats.Family) ([]byte, error) {
	if !fam.NeedsRendition() {
		return m.readRequired(ctx, job.ID, FileOriginal)
	}
	if pdf, ok, err := m.store.ReadFile(ctx, job.ID, FileRendition); err == nil && ok {
		return pdf, nil
	}
	if m.Converter == nil {
		return nil, fmt.Errorf("webapp: %s : aucun convertisseur configuré pour les fichiers %s (no converter)", job.Filename, fam)
	}

	original, err := m.readRequired(ctx, job.ID, FileOriginal)
	if err != nil {
		return nil, err
	}
	if err := m.store.SetProgress(ctx, job.ID, &pipeline.Progress{Stage: pipeline.StageConverting}); err != nil {
		fmt.Fprintf(os.Stderr, "webapp: store progress %s: %v\n", job.ID, err)
	}
	src, cleanup, err := m.materialize(original, sourceExt(job, fam))
	if err != nil {
		return nil, fmt.Errorf("webapp: write temp file: %w", err)
	}
	defer cleanup()

	r, err := m.Converter.Convert(ctx, formats.Format{Family: fam, MIME: job.MIME, Ext: strings.TrimPrefix(sourceExt(job, fam), ".")}, src)
	if err != nil {
		return nil, conversionError(job.Filename, err)
	}
	if err := m.store.WriteFile(ctx, job.ID, FileRendition, r.PDF); err != nil {
		return nil, fmt.Errorf("webapp: store rendition %s: %w", job.ID, err)
	}
	if r.Preview != nil {
		if err := m.store.WriteFile(ctx, job.ID, FilePreview, r.Preview); err != nil {
			fmt.Fprintf(os.Stderr, "webapp: store preview %s: %v\n", job.ID, err)
		}
	}
	return r.PDF, nil
}

// sourceExt est l'extension du fichier temporaire donné au convertisseur
// (LibreOffice choisit son filtre d'après elle) : celle du nom déposé,
// sinon une extension déduite de la famille (fichier sans extension
// reconnu par son contenu).
func sourceExt(job Job, fam formats.Family) string {
	if ext := strings.ToLower(filepath.Ext(job.Filename)); ext != "" {
		return ext
	}
	switch fam {
	case formats.Image:
		switch job.MIME {
		case "image/png":
			return ".png"
		case "image/gif":
			return ".gif"
		case "image/webp":
			return ".webp"
		}
		return ".jpg"
	case formats.Email:
		return ".eml"
	case formats.HTML:
		return ".html"
	case formats.CSV:
		return ".csv"
	}
	return ".txt"
}

func (m *JobManager) readRequired(ctx context.Context, id string, name FileName) ([]byte, error) {
	data, ok, err := m.store.ReadFile(ctx, id, name)
	if err != nil {
		return nil, fmt.Errorf("webapp: read %s of %s: %w", name, id, err)
	}
	if !ok {
		return nil, fmt.Errorf("webapp: %s of %s not found", name, id)
	}
	return data, nil
}

// ReadFile lit un fichier rattaché au job id (original, version PDF,
// aperçu) — pour les routes de téléchargement et d'aperçu.
func (m *JobManager) ReadFile(ctx context.Context, id string, name FileName) ([]byte, bool, error) {
	return m.store.ReadFile(ctx, id, name)
}

// finishStored termine un job de fichier seulement stocké : terminé, sans
// résultat ni erreur.
func (m *JobManager) finishStored(ctx context.Context, job Job) {
	job.FinishedAt = time.Now()
	job.Status = StatusDone
	job.Result = nil
	if err := m.save(ctx, job); err != nil {
		fmt.Fprintf(os.Stderr, "webapp: update job %s: %v\n", job.ID, err)
	}
	if m.OnFinish != nil {
		m.OnFinish(job)
	}
}

// save écrit l'état du traitement (statut, dates, résultat, erreur) sur
// la version à jour du job : ce que l'utilisateur modifie pendant le
// traitement (tags, commentaire) n'est jamais écrasé par la copie prise au démarrage
// — bug réel, trouvé par un test devenu intermittent au jalon 27.
func (m *JobManager) save(ctx context.Context, job Job) error {
	if current, ok, err := m.store.Get(ctx, job.ID); err == nil && ok {
		job.Tags, job.Comment = current.Tags, current.Comment
	}
	return m.store.Update(ctx, job)
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

	if err := m.save(ctx, job); err != nil {
		fmt.Fprintf(os.Stderr, "webapp: update job %s: %v\n", job.ID, err)
	}

	if m.OnFinish != nil {
		m.OnFinish(job)
	}
}

// materialize écrit content dans un fichier temporaire sous m.WorkDir et
// retourne son chemin ainsi qu'une fonction pour le supprimer.
func (m *JobManager) materialize(content []byte, ext string) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp(m.WorkDir, "jarvis-job-*"+ext)
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

	// Page 1 du PDF : l'original, ou la version PDF d'un fichier converti
	// (pas encore produite tant que le job n'a pas été traité).
	fam := familyOf(job)
	if !fam.Pipeline() {
		return nil, true, ErrNoThumbnail
	}
	source := FileOriginal
	if fam.NeedsRendition() {
		source = FileRendition
	}
	pdf, found, err := m.store.ReadFile(ctx, id, source)
	if err != nil {
		return nil, true, fmt.Errorf("webapp: thumbnail %s: %w", id, err)
	}
	if !found {
		return nil, true, ErrNoThumbnail
	}

	path, cleanup, err := m.materialize(pdf, ".pdf")
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

// SetComment remplace le commentaire du job id (espaces de début et de
// fin retirés). Comme les tags, sans incidence sur le traitement.
func (m *JobManager) SetComment(ctx context.Context, id, comment string) error {
	job, ok, err := m.store.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("webapp: set comment %s: get: %w", id, err)
	}
	if !ok {
		return fmt.Errorf("webapp: set comment %s: not found", id)
	}
	job.Comment = strings.TrimSpace(comment)
	if err := m.store.Update(ctx, job); err != nil {
		return fmt.Errorf("webapp: set comment %s: %w", id, err)
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

	// En attente de son tour dans la file (jalon 25) ; StartedAt est
	// renseigné quand le traitement démarre réellement.
	job.Status = StatusPending
	job.StartedAt = time.Time{}
	job.Err = ""
	if err := m.store.Update(ctx, job); err != nil {
		fmt.Fprintf(os.Stderr, "webapp: update job %s to pending: %v\n", job.ID, err)
	}
	if err := m.store.SetProgress(ctx, job.ID, nil); err != nil {
		fmt.Fprintf(os.Stderr, "webapp: clear progress %s: %v\n", job.ID, err)
	}

	go m.process(job, func(ctx context.Context, path string, onProgress pipeline.ProgressFunc) (pipeline.Result, error) {
		return m.runner.RunWithType(ctx, docType, path, onProgress)
	})

	return nil
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("webapp: random id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// conversionErrPrefix : début du message d'un échec de conversion en PDF.
// Défini ici seulement : ConversionFailed s'y fie.
const conversionErrPrefix = "webapp: conversion de "

func conversionError(name string, err error) error {
	return fmt.Errorf(conversionErrPrefix+"%s : %w", name, err)
}

// ConversionFailed : le job a échoué à la conversion en PDF — il n'a donc
// pas de version PDF à montrer (l'aperçu de sa fiche le dit, au lieu d'une
// iframe vers un fichier absent).
func ConversionFailed(j Job) bool {
	return j.Status == StatusFailed && strings.HasPrefix(j.Err, conversionErrPrefix)
}
