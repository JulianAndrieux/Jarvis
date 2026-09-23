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

// ApprovePlan valide le plan proposé (première validation humaine).
func (m *Manager) ApprovePlan(ctx context.Context, id string) error {
	_, err := m.transition(ctx, id, PlanApproved, "Plan validé")
	return err
}

func (m *Manager) Cancel(ctx context.Context, id string) error {
	_, err := m.transition(ctx, id, Cancelled, "Ticket annulé")
	return err
}

// RecoverOrphaned marque en échec les analyses interrompues par un
// redémarrage (la goroutine qui les menait a disparu avec le process).
func (m *Manager) RecoverOrphaned(ctx context.Context) (int, error) {
	list, err := m.Store.List(ctx, Analyzing)
	if err != nil {
		return 0, err
	}
	for _, t := range list {
		t.Status = Failed
		if err := m.Store.Update(ctx, t); err != nil {
			return 0, err
		}
		m.event(ctx, t.ID, Event{Kind: EventError, Author: AuthorAgent, Text: "Analyse interrompue par un redémarrage de l'application — relance-la."})
	}
	return len(list), nil
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
