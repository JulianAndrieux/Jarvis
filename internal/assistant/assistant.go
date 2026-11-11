// Package assistant est Jarvis en tant qu'assistant personnel : une
// conversation avec Claude, qui orchestre, et des outils qui sont les
// services de Jarvis (notes, tâches, documents, emails, mémoire).
//
// Claude décide, Go exécute : la boucle d'outils est ici, chaque appel
// passe par du code testé qui porte la portée de l'utilisateur. Claude n'a
// jamais d'accès direct à la base. Plan et décisions :
// docs/plan-assistant.md.
package assistant

import (
	"context"
	"errors"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/claudeapi"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Status : l'état d'une conversation.
type Status string

const (
	// StatusIdle : l'assistant a répondu, il attend l'utilisateur.
	StatusIdle Status = "idle"
	// StatusRunning : l'assistant travaille (un seul tour à la fois).
	StatusRunning Status = "running"
	// StatusWaiting : l'assistant attend que l'utilisateur confirme ou
	// refuse une action (jalon 58). Aucun processus ne tourne : l'attente
	// survit à un redémarrage.
	StatusWaiting Status = "waiting"
	// StatusFailed : le dernier tour a échoué (Error dit pourquoi) ; on
	// peut réécrire.
	StatusFailed Status = "failed"
)

// Role : qui a écrit une entrée du fil.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	// RoleTools : les résultats des outils demandés par l'entrée
	// précédente.
	RoleTools Role = "tools"
)

// Entry : une entrée du fil d'une conversation. Le fil est en ajout seul :
// une entrée écrite n'est jamais modifiée (l'API de Claude exige que ses
// messages soient rejoués tels quels).
type Entry struct {
	Role Role      `bson:"role"`
	At   time.Time `bson:"at"`
	// Text : le texte de l'utilisateur, ou le texte visible de l'assistant.
	Text string `bson:"text,omitempty"`
	// Raw : le message de Claude tel qu'il l'a rendu (RoleAssistant).
	Raw []byte `bson:"raw,omitempty"`
	// Calls : les outils demandés par l'assistant.
	Calls []claudeapi.Call `bson:"calls,omitempty"`
	// Results : les réponses des outils (RoleTools).
	Results []claudeapi.Result `bson:"results,omitempty"`
	Usage   claudeapi.Usage    `bson:"usage"`
	// Model : le modèle qui a répondu (RoleAssistant).
	Model string `bson:"model,omitempty"`
}

// Conversation : un fil de discussion, privé à son user dans son
// environnement.
type Conversation struct {
	ID string `bson:"_id"`
	// Env et User : renseignés par le Store, jamais par l'appelant.
	Env       tenancy.EnvID  `bson:"env_id"`
	User      tenancy.UserID `bson:"user_id"`
	Title     string         `bson:"title"`
	Status    Status         `bson:"status"`
	Error     string         `bson:"error,omitempty"`
	CreatedAt time.Time      `bson:"created_at"`
	UpdatedAt time.Time      `bson:"updated_at"`
	// Entries : absent des listes (Store.List).
	Entries []Entry `bson:"entries"`
}

// Fact : une chose que l'assistant retient d'une conversation à l'autre
// (« mon propriétaire s'appelle… »). Relue au début de chaque tour.
type Fact struct {
	ID   string    `bson:"_id"`
	Text string    `bson:"text"`
	At   time.Time `bson:"at"`
}

var (
	ErrNotFound = errors.New("assistant: introuvable")
	// ErrBusy : l'assistant travaille déjà sur cette conversation.
	ErrBusy = errors.New("assistant: une réponse est déjà en cours")
	// ErrWaiting : une action attend une confirmation ; il faut y répondre
	// avant d'écrire autre chose.
	ErrWaiting = errors.New("assistant: une action attend ta confirmation")
	// ErrNotPending : cette action n'attend pas (ou plus) de confirmation.
	ErrNotPending = errors.New("assistant: aucune confirmation attendue pour cette action")
)

// DefaultListLimit : taille de la liste des conversations.
const DefaultListLimit = 100

// Store persiste les conversations, la mémoire et la consommation
// (MongoStore en production, FakeStore en test).
//
// Conversations et mémoire sont privées : la vue est celle d'un user dans
// un environnement. La consommation, elle, se compte par environnement.
type Store interface {
	// For : la même persistance vue depuis une portée. Sans environnement,
	// toute opération est refusée (tenancy.ErrNoEnv).
	For(scope tenancy.Scope) Store
	Create(ctx context.Context, c Conversation) error
	Get(ctx context.Context, id string) (Conversation, bool, error)
	// List : la plus récemment modifiée d'abord, sans les entrées.
	List(ctx context.Context, limit int) ([]Conversation, error)
	// Append ajoute des entrées à la fin du fil (jamais de réécriture).
	Append(ctx context.Context, id string, at time.Time, entries ...Entry) error
	// Start fait passer la conversation de « idle » ou « failed » à
	// « running », de façon atomique : deux tours simultanés ne démarrent
	// jamais tous les deux. started=false : un tour est en cours, ou une
	// confirmation est attendue.
	Start(ctx context.Context, id string, at time.Time) (started bool, err error)
	// Pause met le tour en attente d'une confirmation (« waiting »).
	Pause(ctx context.Context, id string, at time.Time) error
	// Resume fait repartir un tour en attente (« waiting » → « running »),
	// de façon atomique : deux réponses simultanées à la même demande
	// n'exécutent jamais l'action deux fois. resumed=false : rien n'attend.
	Resume(ctx context.Context, id string, at time.Time) (resumed bool, err error)
	// Finish clôt le tour : idle (errMsg vide) ou failed.
	Finish(ctx context.Context, id string, errMsg string, at time.Time) error
	Delete(ctx context.Context, id string) error

	AddFact(ctx context.Context, f Fact) error
	// Facts : le plus ancien d'abord.
	Facts(ctx context.Context) ([]Fact, error)
	DeleteFact(ctx context.Context, id string) error

	// AddUsage ajoute u à la consommation du jour ("AAAA-MM-JJ").
	AddUsage(ctx context.Context, day string, u claudeapi.Usage) error
	UsageOn(ctx context.Context, day string) (claudeapi.Usage, error)
}

// Recoverer : au démarrage, les tours restés « running » appartenaient à
// un processus disparu ; ils ne finiront jamais. Tous environnements
// confondus (c'est le processus qui a disparu, pas un environnement).
type Recoverer interface {
	FailRunning(ctx context.Context, errMsg string, at time.Time) (int, error)
}
