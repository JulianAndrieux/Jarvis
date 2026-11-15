// Package assistant : l'assistant de Jarvis (jalon 55) — une conversation
// où Claude cherche dans les documents, les emails, les notes et les
// tâches de l'utilisateur, et agit par les fonctions de Jarvis (créer une
// tâche, une note, taguer un document, archiver un email…).
//
// Exception explicite à « aucune donnée ne sort de la machine », décidée
// par l'utilisateur (cf. CLAUDE.md, exception 9) : ce que l'assistant lit
// pour répondre part à l'API d'Anthropic.
//
// Le paquet ne connaît ni HTTP ni le CLI de Claude : il reçoit un Runner
// (le modèle) et lui offre des outils (internal/mcp) qui agissent avec la
// portée de celui qui pose la question.
package assistant

import (
	"context"
	"errors"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Role : l'auteur d'un message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Step : une chose que l'assistant a faite pendant une réponse (un outil
// appelé). Action : il a modifié quelque chose ; Link : où le voir.
type Step struct {
	Tool    string    `bson:"tool"`
	Summary string    `bson:"summary"`
	Link    string    `bson:"link"`
	Action  bool      `bson:"action"`
	Failed  bool      `bson:"failed"`
	Error   string    `bson:"error"`
	At      time.Time `bson:"at"`
}

// Message : un tour de la conversation.
type Message struct {
	Role Role   `bson:"role"`
	Text string `bson:"text"`
	// Steps : ce que l'assistant a fait pour cette réponse.
	Steps []Step `bson:"steps"`
	// Error : la réponse n'a pas abouti (Text est alors vide).
	Error string    `bson:"error"`
	At    time.Time `bson:"at"`
	// Provenance de la réponse : consignes données au modèle, coût et
	// nombre de tours rapportés par le CLI.
	System  string  `bson:"system"`
	CostUSD float64 `bson:"cost_usd"`
	Turns   int     `bson:"turns"`
}

// Conversation : un fil avec l'assistant, privé à son auteur.
type Conversation struct {
	ID string `bson:"_id"`
	// Env et User : renseignés par le Store, jamais par l'appelant.
	Env      tenancy.EnvID  `bson:"env_id"`
	User     tenancy.UserID `bson:"user_id"`
	Title    string         `bson:"title"`
	Messages []Message      `bson:"messages"`
	// Running : une réponse est en préparation depuis RunStartedAt ; Live :
	// ses étapes au fil de l'eau (vidées quand la réponse est enregistrée).
	Running      bool      `bson:"running"`
	RunStartedAt time.Time `bson:"run_started_at"`
	Live         []Step    `bson:"live"`
	CreatedAt    time.Time `bson:"created_at"`
	UpdatedAt    time.Time `bson:"updated_at"`
}

// Active : une réponse est réellement en préparation. Au-delà de limit,
// le processus qui la préparait a disparu (redémarrage) : la conversation
// n'est plus tenue pour occupée — aucune passe de reprise au démarrage
// n'est nécessaire.
func (c Conversation) Active(now time.Time, limit time.Duration) bool {
	return c.Running && now.Sub(c.RunStartedAt) < limit
}

var (
	// ErrNotFound : conversation inconnue (ou celle d'un autre).
	ErrNotFound = errors.New("assistant: conversation introuvable")
	// ErrBusy : une réponse est déjà en préparation dans cette conversation.
	ErrBusy = errors.New("assistant: une réponse est déjà en préparation")
)

// MaxList : conversations ramenées par List.
const MaxList = 100

// Store persiste les conversations (MongoStore en production, FakeStore en
// test). Une conversation appartient à un user dans un environnement :
// celle d'un autre est introuvable.
type Store interface {
	// For rend la même persistance vue depuis une portée. Sans
	// environnement, chaque méthode refuse (tenancy.ErrNoEnv).
	For(scope tenancy.Scope) Store
	Create(ctx context.Context, c Conversation) error
	Get(ctx context.Context, id string) (Conversation, bool, error)
	// List : la plus récemment modifiée d'abord, sans les messages.
	List(ctx context.Context) ([]Conversation, error)
	// Begin ajoute le message de l'utilisateur et marque la conversation
	// occupée, d'un seul geste : refusé (ErrBusy) si une réponse est en
	// préparation depuis staleBefore ou après.
	Begin(ctx context.Context, id string, user Message, staleBefore time.Time) error
	// AddStep ajoute une étape à la réponse en préparation.
	AddStep(ctx context.Context, id string, step Step) error
	// Finish enregistre la réponse et libère la conversation.
	Finish(ctx context.Context, id string, reply Message) error
	Delete(ctx context.Context, id string) error
}
