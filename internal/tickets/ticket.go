// Package tickets est l'outil de tickets de Jarvis (jalon 26) : on y
// décrit un changement voulu dans l'application, qu'un agent LLM local
// analyse (jalon 27), puis développe, teste (jalon 28) et déploie (jalon 30),
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
	Developing   Status = "developpement"  // jalon 28
	Review       Status = "diff_a_valider" // jalon 28 : seconde validation
	Accepted     Status = "accepte"        // accepté sans déploiement automatique
	Deploying    Status = "deploiement"    // jalon 30
	Deployed     Status = "deploye"
	Failed       Status = "echec"
	Cancelled    Status = "annule"
)

// transitions : seules ces étapes s'enchaînent. Pas de plan validé sans
// analyse, un ticket annulé est clos ; un échec se relance.
var transitions = map[Status][]Status{
	Draft:        {Analyzing, Cancelled},
	Analyzing:    {PlanReady, Failed},
	PlanReady:    {PlanApproved, Analyzing, Cancelled},
	PlanApproved: {Developing, Cancelled},
	Developing:   {Review, Failed},
	Review:       {Accepted, Deploying, Developing, Cancelled},
	Accepted:     {Deploying, Cancelled},
	// Un déploiement se confirme (nouvelle version en service) ou revient
	// en revue (échec, retour arrière) — jamais annulé en plein vol.
	Deploying: {Deployed, Review},
	Failed:    {Analyzing, Developing, Cancelled},
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
	case Developing:
		return "Développement en cours"
	case Review:
		return "Diff à valider"
	case Accepted:
		return "Accepté"
	case Deploying:
		return "Déploiement en cours"
	case Deployed:
		return "Déployé"
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
func (s Status) Active() bool { return s == Analyzing || s == Developing || s == Deploying }

// AgentKind : qui travaille sur le ticket (jalon 41) — Claude Code, ou
// le modèle local (Devstral). Vide (tickets d'avant) : le modèle local.
type AgentKind string

const (
	AgentLocal  AgentKind = "local"
	AgentClaude AgentKind = "claude"
)

// Label est le libellé affiché d'un agent.
func (a AgentKind) Label() string {
	if a == AgentClaude {
		return "Claude Code"
	}
	return "Modèle local"
}

// AgentSet : les agents d'un même moteur. Reviewer nil : pas de relecture.
type AgentSet struct {
	Analyst   Analyst
	Developer Developer
	Reviewer  Reviewer
}

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
	Acceptance string `bson:"acceptance"`
	Status     Status `bson:"status"`
	// Agent : qui analyse, développe et relit ce ticket.
	Agent AgentKind `bson:"agent,omitempty"`
	Plan  string    `bson:"plan,omitempty"`
	// Branch, Diff et Report : la branche git du développement (jalon
	// 28), le diff par rapport à main soumis à la revue, et le dernier
	// rapport de vérification (gofmt, vet, tests).
	Branch string `bson:"branch,omitempty"`
	Diff   string `bson:"diff,omitempty"`
	Report string `bson:"report,omitempty"`
	// Pushed : commit de main poussé vers GitHub depuis ce ticket.
	Pushed string `bson:"pushed,omitempty"`
	// Review : verdict du relecteur sur le diff soumis à la revue (jalon
	// 36) ; nil sans relecteur.
	Review    *ReviewResult `bson:"review,omitempty"`
	CreatedAt time.Time     `bson:"created_at"`
	UpdatedAt time.Time     `bson:"updated_at"`
	Events    []Event       `bson:"events,omitempty"`
}

// MaxList borne ce que List ramène : un appelant qui reçoit autant de
// tickets sait que la liste est tronquée, et qu'un compte tiré de sa
// longueur est un minimum, pas un total.
const MaxList = 500

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

// DevRequest est ce que l'agent reçoit pour développer un ticket (jalon
// 28) : le besoin, le plan validé, et — pour une nouvelle tentative — le
// retour (rapport de vérification en échec, demande de changements).
type DevRequest struct {
	Title      string
	Need       string
	Acceptance string
	Plan       string
	Feedback   string
	// Dir est la copie de travail isolée du ticket.
	Dir string
	// Attempt : numéro de la tentative sur toute la vie du ticket
	// (relances comprises) — l'agent varie son approche au-delà de 1.
	Attempt int
}

// Developer développe un ticket dans sa copie de travail et retourne un
// résumé — agent.Developer en production.
type Developer interface {
	Develop(ctx context.Context, req DevRequest, onStep func(AgentStep)) (summary string, err error)
}

// Workspace gère la copie de travail isolée d'un ticket —
// workspace.Manager en production (branche git ticket/<id>).
type Workspace interface {
	Prepare(ctx context.Context, id string) (dir string, err error)
	Diff(ctx context.Context, dir string) (string, error)
	Commit(ctx context.Context, dir, message string) error
	Discard(ctx context.Context, id string) error
}

// Deployer déploie la branche d'un ticket accepté (jalon 30) —
// deploy.Deployer en production. En cas de succès, le processus est
// remplacé par la nouvelle version : c'est elle qui confirme
// (ConfirmDeployment). Une erreur signifie que main et l'application sont
// restés (ou revenus) comme avant.
type Deployer interface {
	Deploy(ctx context.Context, id string, onStep func(text, detail string)) error
}

// Pusher pousse main vers GitHub — workspace.Manager en production.
// Jamais de push forcé.
type Pusher interface {
	Push(ctx context.Context) (string, error)
}

// Verifier refait la vérification finale, indépendamment de ce que
// l'agent affirme — workspace.Checker en production.
type Verifier interface {
	Checks(ctx context.Context, dir string) (string, bool)
	Tests(ctx context.Context, dir, pkg string) (string, bool)
}

// ReviewRequest est ce que le relecteur reçoit (jalon 36) : le ticket, le
// plan validé, le diff vérifié et la copie de travail (pour lire autour).
type ReviewRequest struct {
	Title, Need, Acceptance, Plan string
	Diff                          string
	Dir                           string
}

// ReviewIssue : une remarque du relecteur. Severity : "bloquant",
// "important" ou "mineur".
type ReviewIssue struct {
	File     string `bson:"file"`
	Line     int    `bson:"line"`
	Severity string `bson:"severity"`
	Message  string `bson:"message"`
}

// ReviewResult : le verdict du relecteur. Rounds : relectures menées
// avant ce verdict.
type ReviewResult struct {
	Approved bool          `bson:"approved"`
	Summary  string        `bson:"summary"`
	Issues   []ReviewIssue `bson:"issues,omitempty"`
	Rounds   int           `bson:"rounds"`
	// Captures : pages capturées par la relecture visuelle (jalon 38),
	// avant et après, montrées aussi à l'humain.
	Captures []ReviewCapture `bson:"captures,omitempty"`
}

// ReviewCapture : une page, avant (version en service) et après (version
// du ticket), en PNG.
type ReviewCapture struct {
	Page   string `bson:"page"`
	Before []byte `bson:"before"`
	After  []byte `bson:"after"`
}

// Reviewer relit un diff vérifié selon les standards du projet (jalon
// 36) : ce que les tests ne voient pas.
type Reviewer interface {
	Review(ctx context.Context, req ReviewRequest, onStep func(AgentStep)) (ReviewResult, error)
}
