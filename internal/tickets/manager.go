package tickets

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/gate"
)

// Manager orchestre le cycle des tickets : création, analyse par l'agent
// (en arrière-plan), validation ou révision du plan.
type Manager struct {
	Store   Store
	Analyst Analyst
	// Gate, si non-nil, est la file partagée avec le traitement des
	// documents : l'agent et le pipeline n'appellent jamais les modèles
	// en même temps.
	Gate *gate.Gate
	// DetailChars borne la sortie d'outil conservée dans le fil (0 : 4000).
	DetailChars int

	// Developer, Workspace et Verifier activent le développement (jalon
	// 28) ; si l'un manque, valider le plan s'arrête là.
	Developer Developer
	Workspace Workspace
	Verifier  Verifier
	// MaxAttempts : tentatives de développement avant l'échec (0 : 2) —
	// chaque nouvelle tentative reçoit le rapport de vérification.
	MaxAttempts int

	// Deployer active le déploiement à l'acceptation du diff (jalon 30).
	Deployer Deployer
	// Busy dit pourquoi l'application ne peut pas redémarrer maintenant
	// ("" : elle peut) — documents en traitement, autre ticket actif.
	Busy func(ctx context.Context) string
	// DeployPoll : intervalle de ré-examen de Busy (0 : 15 s) ;
	// DeployMaxWait : attente maximale (0 : 2 h) avant d'abandonner.
	DeployPoll    time.Duration
	DeployMaxWait time.Duration
	// Pusher permet de pousser main vers GitHub depuis un ticket déployé.
	Pusher Pusher
}

// Push pousse main vers GitHub depuis un ticket déployé (action explicite
// de l'utilisateur). Un échec est consigné dans le fil, pas renvoyé : le
// ticket reste déployé et le push peut être retenté.
func (m *Manager) Push(ctx context.Context, id string) error {
	if m.Pusher == nil {
		return fmt.Errorf("tickets: le push vers GitHub n'est pas configuré")
	}
	t, ok, err := m.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("tickets: %s introuvable", id)
	}
	if t.Status != Deployed {
		return fmt.Errorf("tickets: seul un ticket déployé se pousse vers GitHub (statut : %s)", t.Status.Label())
	}
	commit, err := m.Pusher.Push(ctx)
	if err != nil {
		m.event(ctx, id, Event{Kind: EventError, Author: AuthorUser, Text: "Push vers GitHub impossible", Detail: err.Error()})
		return nil
	}
	t.Pushed = commit
	if err := m.Store.Update(ctx, t); err != nil {
		return err
	}
	if len(commit) > 7 {
		commit = commit[:7]
	}
	m.event(ctx, id, Event{Kind: EventStatus, Author: AuthorUser, Text: "Poussé vers GitHub : origin/main à " + commit})
	return nil
}

// Create enregistre un nouveau ticket en brouillon.
func (m *Manager) Create(ctx context.Context, title, need, acceptance string) (Ticket, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Ticket{}, fmt.Errorf("tickets: le titre est obligatoire")
	}
	id, err := newID()
	if err != nil {
		return Ticket{}, err
	}
	now := time.Now()
	t := Ticket{ID: id, Title: title, Need: strings.TrimSpace(need), Acceptance: strings.TrimSpace(acceptance), Status: Draft, CreatedAt: now, UpdatedAt: now}
	if err := m.Store.Create(ctx, t); err != nil {
		return Ticket{}, err
	}
	m.event(ctx, id, Event{Kind: EventStatus, Author: AuthorUser, Text: "Ticket créé"})
	return t, nil
}

func (m *Manager) Get(ctx context.Context, id string) (Ticket, bool, error) {
	return m.Store.Get(ctx, id)
}

func (m *Manager) List(ctx context.Context, status Status) ([]Ticket, error) {
	return m.Store.List(ctx, status)
}

func (m *Manager) Delete(ctx context.Context, id string) error { return m.Store.Delete(ctx, id) }

// Comment ajoute un message de l'utilisateur au fil — pris en compte par
// l'agent à la prochaine révision.
func (m *Manager) Comment(ctx context.Context, id, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return m.Store.AppendEvent(ctx, id, Event{At: time.Now(), Kind: EventComment, Author: AuthorUser, Text: text})
}

// StartAnalysis lance l'analyse d'un brouillon ou d'un ticket en échec.
func (m *Manager) StartAnalysis(ctx context.Context, id string) error {
	t, err := m.transition(ctx, id, Analyzing, "Analyse demandée")
	if err != nil {
		return err
	}
	go m.analyze(t, AnalysisRequest{Title: t.Title, Need: t.Need, Acceptance: t.Acceptance})
	return nil
}

// RequestRevision renvoie le plan à l'agent avec les commentaires de
// l'utilisateur (feedback, plus ceux écrits depuis le plan).
func (m *Manager) RequestRevision(ctx context.Context, id, feedback string) error {
	if err := m.Comment(ctx, id, feedback); err != nil {
		return err
	}
	t, err := m.transition(ctx, id, Analyzing, "Révision du plan demandée")
	if err != nil {
		return err
	}
	req := AnalysisRequest{Title: t.Title, Need: t.Need, Acceptance: t.Acceptance, PreviousPlan: t.Plan, Feedback: feedbackSinceLastPlan(t.Events)}
	go m.analyze(t, req)
	return nil
}

// ApprovePlan valide le plan proposé (première validation humaine) et,
// si le développement est configuré, le lance aussitôt.
func (m *Manager) ApprovePlan(ctx context.Context, id string) error {
	if _, err := m.transition(ctx, id, PlanApproved, "Plan validé"); err != nil {
		return err
	}
	if !m.canDevelop() {
		return nil
	}
	return m.StartDevelopment(ctx, id, "")
}

func (m *Manager) canDevelop() bool {
	return m.Developer != nil && m.Workspace != nil && m.Verifier != nil
}

// StartDevelopment lance (ou relance) le développement d'un ticket au
// plan validé ; feedback (facultatif) est transmis à l'agent.
func (m *Manager) StartDevelopment(ctx context.Context, id, feedback string) error {
	if !m.canDevelop() {
		return fmt.Errorf("tickets: le développement automatique n'est pas configuré")
	}
	t, err := m.transition(ctx, id, Developing, "Développement lancé")
	if err != nil {
		return err
	}
	go m.develop(t, feedback)
	return nil
}

// RequestChanges renvoie le diff à l'agent avec le retour de
// l'utilisateur (seconde validation refusée).
func (m *Manager) RequestChanges(ctx context.Context, id, feedback string) error {
	if err := m.Comment(ctx, id, feedback); err != nil {
		return err
	}
	return m.StartDevelopment(ctx, id, "Changements demandés par l'utilisateur :\n"+strings.TrimSpace(feedback))
}

// AcceptChanges valide le diff (seconde validation humaine) et, si le
// déploiement est configuré, le lance.
func (m *Manager) AcceptChanges(ctx context.Context, id string) error {
	if m.Deployer == nil {
		_, err := m.transition(ctx, id, Accepted, "Diff accepté")
		return err
	}
	return m.startDeployment(ctx, id, "Diff accepté — déploiement lancé")
}

// StartDeployment déploie un ticket déjà accepté (sans déploiement à
// l'époque).
func (m *Manager) StartDeployment(ctx context.Context, id string) error {
	if m.Deployer == nil {
		return fmt.Errorf("tickets: le déploiement n'est pas configuré")
	}
	return m.startDeployment(ctx, id, "Déploiement lancé")
}

func (m *Manager) startDeployment(ctx context.Context, id, label string) error {
	t, err := m.transition(ctx, id, Deploying, label)
	if err != nil {
		return err
	}
	go m.deploy(t)
	return nil
}

// deploy attend que l'application puisse redémarrer, prend la file des
// modèles (plus aucun document ni agent ne démarre), puis déploie.
func (m *Manager) deploy(t Ticket) {
	ctx := context.Background()
	poll, maxWait := m.DeployPoll, m.DeployMaxWait
	if poll <= 0 {
		poll = 15 * time.Second
	}
	if maxWait <= 0 {
		maxWait = 2 * time.Hour
	}
	var release func()
	deadline := time.Now().Add(maxWait)
	lastReason := ""
	for {
		reason := m.busy(ctx)
		if reason == "" && m.Gate != nil {
			release = m.Gate.Acquire()
			if reason = m.busy(ctx); reason != "" {
				release()
				release = nil
			}
		}
		if reason == "" {
			break
		}
		if reason != lastReason {
			m.event(ctx, t.ID, Event{Kind: EventStatus, Author: AuthorAgent, Text: "Déploiement en attente : " + reason})
			lastReason = reason
		}
		if time.Now().After(deadline) {
			m.backToReview(ctx, t.ID, "Déploiement abandonné : l'application n'a pas été disponible pour redémarrer ("+reason+").", "")
			return
		}
		time.Sleep(poll)
	}
	if release != nil {
		defer release()
	}
	err := m.Deployer.Deploy(ctx, t.ID, func(text, detail string) {
		m.event(ctx, t.ID, Event{Kind: EventStep, Author: AuthorAgent, Text: text, Detail: clip(detail, m.detailChars())})
	})
	if err != nil {
		m.backToReview(ctx, t.ID, "Déploiement impossible — l'application et main restent comme avant.", err.Error())
	}
}

func (m *Manager) busy(ctx context.Context) string {
	if m.Busy == nil {
		return ""
	}
	return m.Busy(ctx)
}

// backToReview : le déploiement n'a pas eu lieu (ou a été défait) ; le
// diff reste à la revue — relancer, demander des changements ou abandonner.
func (m *Manager) backToReview(ctx context.Context, id, msg, detail string) {
	t, ok, err := m.Store.Get(ctx, id)
	if err != nil || !ok {
		fmt.Fprintf(os.Stderr, "tickets: %s introuvable après déploiement : %v\n", id, err)
		return
	}
	t.Status = Review
	if err := m.Store.Update(ctx, t); err != nil {
		fmt.Fprintf(os.Stderr, "tickets: update %s: %v\n", id, err)
	}
	m.event(ctx, id, Event{Kind: EventError, Author: AuthorAgent, Text: msg, Detail: clip(detail, m.detailChars())})
}

// ConfirmDeployment : appelé par la nouvelle version une fois en service.
// La branche est dans main : la copie de travail est supprimée.
func (m *Manager) ConfirmDeployment(ctx context.Context, id, commit string) error {
	if len(commit) > 7 {
		commit = commit[:7]
	}
	t, err := m.transition(ctx, id, Deployed, "Déployé : main à "+commit+", application redémarrée sur la nouvelle version (main n'est pas poussé vers GitHub)")
	if err != nil {
		return err
	}
	if m.Workspace != nil && t.Branch != "" {
		if err := m.Workspace.Discard(ctx, id); err != nil {
			m.event(ctx, id, Event{Kind: EventError, Author: AuthorAgent, Text: "Copie de travail non supprimée : " + err.Error()})
		}
	}
	return nil
}

// DeploymentRolledBack : la nouvelle version s'est arrêtée avant d'être
// confirmée ; l'ancienne a été rétablie (binaire et code).
func (m *Manager) DeploymentRolledBack(ctx context.Context, id, reason string) error {
	t, ok, err := m.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("tickets: %s introuvable", id)
	}
	if !CanTransition(t.Status, Review) {
		return fmt.Errorf("tickets: impossible de passer de « %s » à « %s »", t.Status.Label(), Review.Label())
	}
	m.backToReview(ctx, id, "Déploiement annulé : la nouvelle version s'est arrêtée au démarrage — ancienne version rétablie, main ramené à son état d'avant.", reason)
	return nil
}

// Cancel clôt le ticket ; sa copie de travail éventuelle est supprimée.
func (m *Manager) Cancel(ctx context.Context, id string) error {
	t, err := m.transition(ctx, id, Cancelled, "Ticket annulé")
	if err != nil {
		return err
	}
	if t.Branch != "" && m.Workspace != nil {
		if err := m.Workspace.Discard(ctx, id); err != nil {
			m.event(ctx, id, Event{Kind: EventError, Author: AuthorAgent, Text: "Copie de travail non supprimée : " + err.Error()})
		}
	}
	return nil
}

// RecoverOrphaned marque en échec les analyses et développements
// interrompus par un redémarrage (la goroutine qui les menait a disparu
// avec le process), et renvoie en revue un déploiement interrompu. À
// appeler après la confirmation d'un déploiement (marqueur).
func (m *Manager) RecoverOrphaned(ctx context.Context) (int, error) {
	deploying, err := m.Store.List(ctx, Deploying)
	if err != nil {
		return 0, err
	}
	for _, t := range deploying {
		m.backToReview(ctx, t.ID, "Déploiement interrompu par un redémarrage de l'application — vérifie main (git log) avant de relancer.", "")
	}
	var list []Ticket
	for _, st := range []Status{Analyzing, Developing} {
		l, err := m.Store.List(ctx, st)
		if err != nil {
			return 0, err
		}
		list = append(list, l...)
	}
	for _, t := range list {
		t, ok, err := m.Store.Get(ctx, t.ID) // la liste n'a pas diff ni rapport
		if err != nil || !ok {
			continue
		}
		t.Status = Failed
		if err := m.Store.Update(ctx, t); err != nil {
			return 0, err
		}
		m.event(ctx, t.ID, Event{Kind: EventError, Author: AuthorAgent, Text: "Travail de l'agent interrompu par un redémarrage de l'application — relance-le."})
	}
	return len(deploying) + len(list), nil
}

func (m *Manager) transition(ctx context.Context, id string, to Status, label string) (Ticket, error) {
	t, ok, err := m.Store.Get(ctx, id)
	if err != nil {
		return Ticket{}, err
	}
	if !ok {
		return Ticket{}, fmt.Errorf("tickets: %s introuvable", id)
	}
	if !CanTransition(t.Status, to) {
		return Ticket{}, fmt.Errorf("tickets: impossible de passer de « %s » à « %s »", t.Status.Label(), to.Label())
	}
	t.Status = to
	if err := m.Store.Update(ctx, t); err != nil {
		return Ticket{}, err
	}
	m.event(ctx, id, Event{Kind: EventStatus, Author: AuthorUser, Text: label})
	return t, nil
}

// analyze mène l'analyse à son tour dans la file partagée, et consigne
// chaque étape de l'agent dans le fil.
func (m *Manager) analyze(t Ticket, req AnalysisRequest) {
	ctx := context.Background()
	if m.Gate != nil {
		release := m.Gate.Acquire()
		defer release()
	}
	plan, err := m.Analyst.Analyze(ctx, req, func(s AgentStep) {
		m.event(ctx, t.ID, Event{Kind: EventStep, Author: AuthorAgent, Text: s.Summary, Detail: clip(s.Detail, m.detailChars())})
	})
	if err != nil {
		t.Status = Failed
		if uerr := m.Store.Update(ctx, t); uerr != nil {
			fmt.Fprintf(os.Stderr, "tickets: update %s: %v\n", t.ID, uerr)
		}
		m.event(ctx, t.ID, Event{Kind: EventError, Author: AuthorAgent, Text: "L'analyse a échoué : " + err.Error()})
		return
	}
	t.Status, t.Plan = PlanReady, plan
	if err := m.Store.Update(ctx, t); err != nil {
		fmt.Fprintf(os.Stderr, "tickets: update %s: %v\n", t.ID, err)
		return
	}
	m.event(ctx, t.ID, Event{Kind: EventPlan, Author: AuthorAgent, Text: "Plan proposé — à valider"})
}

// develop mène le développement à son tour dans la file partagée : copie
// de travail, agent, puis vérification finale refaite par Jarvis (le
// modèle ne se juge pas lui-même). Une vérification en échec relance
// l'agent avec le rapport, dans la limite de MaxAttempts.
func (m *Manager) develop(t Ticket, feedback string) {
	ctx := context.Background()
	if m.Gate != nil {
		release := m.Gate.Acquire()
		defer release()
	}
	dir, err := m.Workspace.Prepare(ctx, t.ID)
	if err != nil {
		m.fail(ctx, t, "Copie de travail impossible : "+err.Error(), "")
		return
	}
	t.Branch = "ticket/" + t.ID
	if err := m.Store.Update(ctx, t); err != nil {
		fmt.Fprintf(os.Stderr, "tickets: update %s: %v\n", t.ID, err)
	}

	attempts := m.MaxAttempts
	if attempts <= 0 {
		attempts = 2
	}
	var report string
	previous := previousAttempts(t.Events)
	for attempt := 1; attempt <= attempts; attempt++ {
		n := previous + attempt
		m.event(ctx, t.ID, Event{Kind: EventStatus, Author: AuthorAgent, Text: fmt.Sprintf("Tentative %d", n)})
		summary, err := m.Developer.Develop(ctx, DevRequest{
			Title: t.Title, Need: t.Need, Acceptance: t.Acceptance, Plan: t.Plan, Feedback: feedback, Dir: dir, Attempt: n,
		}, func(s AgentStep) {
			m.event(ctx, t.ID, Event{Kind: EventStep, Author: AuthorAgent, Text: s.Summary, Detail: clip(s.Detail, m.detailChars())})
		})
		if err != nil {
			m.fail(ctx, t, "Le développement a échoué : "+err.Error(), "")
			return
		}

		// Aucun fichier modifié : inutile de lancer la vérification complète
		// (vu en réel : une minute perdue avant de le constater).
		if changes, err := m.Workspace.Diff(ctx, dir); err == nil && strings.TrimSpace(changes) == "" {
			m.fail(ctx, t, "L'agent n'a modifié aucun fichier : aucune modification à vérifier.", "")
			return
		}

		var ok bool
		report, ok = m.verify(ctx, dir)
		if !ok {
			m.event(ctx, t.ID, Event{Kind: EventError, Author: AuthorAgent, Text: "Vérification finale en échec", Detail: clip(report, m.detailChars())})
			feedback = "La vérification finale (gofmt, go vet, go build, go test ./...) a échoué :\n" + report
			continue
		}
		m.event(ctx, t.ID, Event{Kind: EventStatus, Author: AuthorAgent, Text: "Vérification finale réussie", Detail: clip(report, m.detailChars())})

		diff, err := m.Workspace.Diff(ctx, dir)
		if err != nil {
			m.fail(ctx, t, "Diff impossible : "+err.Error(), report)
			return
		}
		if strings.TrimSpace(diff) == "" {
			m.fail(ctx, t, "L'agent n'a apporté aucune modification.", report)
			return
		}
		if err := m.Workspace.Commit(ctx, dir, fmt.Sprintf("Ticket %s : %s\n\n%s", t.ID, t.Title, summary)); err != nil {
			m.fail(ctx, t, "Commit impossible : "+err.Error(), report)
			return
		}
		t.Status, t.Diff, t.Report = Review, clip(diff, 200_000), report
		if err := m.Store.Update(ctx, t); err != nil {
			fmt.Fprintf(os.Stderr, "tickets: update %s: %v\n", t.ID, err)
			return
		}
		m.event(ctx, t.ID, Event{Kind: EventPlan, Author: AuthorAgent, Text: "Diff prêt à relire — " + summary})
		return
	}
	m.fail(ctx, t, fmt.Sprintf("La vérification finale échoue encore après %d tentatives.", attempts), report)
}

// verify : vérifications (templ, gofmt, vet, build) puis toute la suite
// de tests unitaires.
// previousAttempts compte les tentatives de développement déjà menées
// sur ce ticket (relances comprises).
func previousAttempts(events []Event) int {
	n := 0
	for _, e := range events {
		if e.Kind == EventStatus && strings.HasPrefix(e.Text, "Tentative ") {
			n++
		}
	}
	return n
}

func (m *Manager) verify(ctx context.Context, dir string) (string, bool) {
	checks, ok := m.Verifier.Checks(ctx, dir)
	if !ok {
		return checks, false
	}
	tests, ok := m.Verifier.Tests(ctx, dir, "./...")
	return checks + "\n" + tests, ok
}

func (m *Manager) fail(ctx context.Context, t Ticket, msg, report string) {
	t.Status = Failed
	if report != "" {
		t.Report = report
	}
	if err := m.Store.Update(ctx, t); err != nil {
		fmt.Fprintf(os.Stderr, "tickets: update %s: %v\n", t.ID, err)
	}
	m.event(ctx, t.ID, Event{Kind: EventError, Author: AuthorAgent, Text: msg, Detail: clip(report, m.detailChars())})
}

func (m *Manager) event(ctx context.Context, id string, e Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if err := m.Store.AppendEvent(ctx, id, e); err != nil {
		fmt.Fprintf(os.Stderr, "tickets: append event %s: %v\n", id, err)
	}
}

func (m *Manager) detailChars() int {
	if m.DetailChars > 0 {
		return m.DetailChars
	}
	return 4000
}

// feedbackSinceLastPlan : les commentaires de l'utilisateur écrits depuis
// le dernier plan proposé.
func feedbackSinceLastPlan(events []Event) string {
	start := 0
	for i, e := range events {
		if e.Kind == EventPlan {
			start = i + 1
		}
	}
	var parts []string
	for _, e := range events[start:] {
		if e.Kind == EventComment {
			parts = append(parts, "- "+e.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "\n…"
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("tickets: generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
