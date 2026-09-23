// Package tickets est l'outil de tickets de Jarvis (jalon 26) : on y
// décrit un changement voulu dans l'application, qu'un agent LLM local
// analyse (jalon 27), puis développe, teste et déploie (jalons 28-29),
// avec deux validations humaines — le plan, puis le diff (décision de
// l'utilisateur).
package tickets

import (
	"context"
	"time"
)

// Status est l'étape d'un ticket.
type Status string

const (
	Draft        Status = "brouillon"
	Analyzing    Status = "analyse"
	PlanReady    Status = "plan_a_valider"
	PlanApproved Status = "plan_valide"
	Failed       Status = "echec"
	Cancelled    Status = "annule"
)

// transitions : seules ces étapes s'enchaînent. Pas de plan validé sans
// analyse, un ticket annulé est clos ; un échec se relance.
var transitions = map[Status][]Status{
	Draft:        {Analyzing, Cancelled},
	Analyzing:    {PlanReady, Failed},
	PlanReady:    {PlanApproved, Analyzing, Cancelled},
	Failed:       {Analyzing, Cancelled},
	PlanApproved: {Cancelled},
}

// CanTransition indique si un ticket peut passer de from à to.
func CanTransition(from, to Status) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Label est le libellé affiché d'un statut.
func (s Status) Label() string {
	switch s {
	case Draft:
		return "Brouillon"
	case Analyzing:
		return "Analyse en cours"
	case PlanReady:
		return "Plan à valider"
	case PlanApproved:
		return "Plan validé"
	case Failed:
		return "Échec"
	case Cancelled:
		return "Annulé"
	default:
		return string(s)
	}
}

// Active : l'agent travaille sur le ticket (l'interface se met à jour
// d'elle-même).
func (s Status) Active() bool { return s == Analyzing }

// EventKind est la nature d'une entrée du fil d'un ticket.
type EventKind string

const (
	EventComment EventKind = "comment" // message de l'utilisateur
	EventStatus  EventKind = "status"  // changement d'étape
	EventStep    EventKind = "step"    // action de l'agent (outil appelé)
	EventPlan    EventKind = "plan"    // plan proposé par l'agent
	EventError   EventKind = "error"
)

// Author distingue l'utilisateur de l'agent dans le fil.
type Author string

const (
	AuthorUser  Author = "utilisateur"
	AuthorAgent Author = "agent"
)

// Event est une entrée du fil d'un ticket — historique en ajout seul :
// tout ce que l'agent a lu, cherché ou conclu reste consultable.
type Event struct {
	At     time.Time `bson:"at"`
	Kind   EventKind `bson:"kind"`
	Author Author    `bson:"author"`
	Text   string    `bson:"text"`
	// Detail : sortie d'un outil, tronquée (affichée repliée).
	Detail string `bson:"detail,omitempty"`
}

// Ticket est une demande de changement de l'application.
type Ticket struct {
	ID    string `bson:"_id"`
	Title string `bson:"title"`
	// Need : ce que l'utilisateur veut, en langage libre.
	Need string `bson:"need"`
	// Acceptance : critères d'acceptation, un par ligne — ce qui dira que
	// c'est fait (et que les tests de l'agent devront vérifier).
	Acceptance string    `bson:"acceptance"`
	Status     Status    `bson:"status"`
	Plan       string    `bson:"plan,omitempty"`
	CreatedAt  time.Time `bson:"created_at"`
	UpdatedAt  time.Time `bson:"updated_at"`
	Events     []Event   `bson:"events,omitempty"`
}

// Store est le port de persistance des tickets.
type Store interface {
	Create(ctx context.Context, t Ticket) error
	Get(ctx context.Context, id string) (Ticket, bool, error)
	// Update réécrit titre, besoin, critères, statut et plan. Jamais le fil
	// (AppendEvent), pour qu'une mise à jour ne perde pas un événement
	// ajouté entre-temps par l'agent.
	Update(ctx context.Context, t Ticket) error
	// List retourne les tickets (du plus récent au plus ancien), filtrés
	// par statut si status n'est pas vide.
	List(ctx context.Context, status Status) ([]Ticket, error)
	AppendEvent(ctx context.Context, id string, e Event) error
	Delete(ctx context.Context, id string) error
}

// AnalysisRequest est ce que l'agent reçoit pour analyser un ticket. Pour
// une révision, PreviousPlan et Feedback (commentaires de l'utilisateur
// depuis ce plan) lui disent quoi changer.
type AnalysisRequest struct {
	Title        string
	Need         string
	Acceptance   string
	PreviousPlan string
	Feedback     string
}

// AgentStep est une action de l'agent, ajoutée au fil du ticket.
type AgentStep struct {
	Summary string // ex. "read_file internal/webapp/store.go"
	Detail  string // sortie de l'outil (tronquée)
}

// Analyst analyse un ticket en lecture seule et propose un plan —
// agent.Analyzer en production, une fake dans les tests.
type Analyst interface {
	Analyze(ctx context.Context, req AnalysisRequest, onStep func(AgentStep)) (plan string, err error)
}
