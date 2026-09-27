// Package changes décrit une modification faite par un humain, et en tient
// le journal.
//
// Une Op est une modification au grain du champ, pas du document. Deux
// personnes qui touchent des champs différents de la même note ne se
// marchent jamais dessus : c'est là que « commiter sans conflit » se gagne,
// par la granularité, pas par un algorithme de fusion.
//
// Le même vocabulaire sert deux choses, à deux moments :
//   - le Journal (ce paquet) : l'histoire de ce qui a été commité,
//     append-only, pour répondre à « qui a changé quoi » ;
//   - le changeset (jalon 48) : les mêmes Op, mais en attente, invisibles
//     des autres jusqu'au commit.
//
// Les deux ne se ressemblent pas dans leur exigence : une écriture de
// journal qui échoue ne doit pas défaire une modification déjà faite (elle
// est journalisée en avertissement), alors qu'une opération en attente est
// la donnée elle-même, et son échec doit remonter.
package changes

import (
	"context"
	"encoding/json"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Kind : sur quel genre d'entité porte la modification. Un mot lisible :
// il s'affiche tel quel dans l'activité.
type Kind string

const (
	KindDocument Kind = "document"
	KindNote     Kind = "note"
	KindTask     Kind = "tâche"
	KindTicket   Kind = "ticket"
	KindEmail    Kind = "email"
)

// Action : ce que l'opération fait au champ.
type Action string

const (
	// Set remplace la valeur d'un champ.
	Set Action = "set"
	// Create et Delete portent sur l'entité entière (Field vide).
	Create Action = "create"
	Delete Action = "delete"
)

// Op est une modification d'un champ d'une entité, attribuée.
//
// Before/After portent du JSON (et non any) : c'est déjà le format
// canonique de ce projet pour ce qui est stocké tel quel — mêmes octets
// que result_json ou progress_json — et ça évite qu'un type applicatif
// s'infiltre dans le format du journal.
type Op struct {
	ID      string            `bson:"_id"`
	Env     tenancy.EnvID     `bson:"env_id"`
	User    tenancy.UserID    `bson:"user_id"`
	Session tenancy.SessionID `bson:"session_id"`
	At      time.Time         `bson:"at"`

	Kind   Kind   `bson:"kind"`
	Target string `bson:"target"`
	// Label : de quoi il s'agit, pour l'affichage ("facture_2026.pdf"). Un
	// identifiant ne dit rien à personne, et l'entité peut avoir été
	// supprimée depuis.
	Label  string `bson:"label,omitempty"`
	Field  string `bson:"field,omitempty"`
	Action Action `bson:"action"`

	Before string `bson:"before,omitempty"`
	After  string `bson:"after,omitempty"`
	// BaseVer : la version de l'entité au moment de la saisie. Sert au
	// rebase du changeset (jalon 48) ; le journal la garde pour pouvoir
	// expliquer un conflit après coup.
	BaseVer int `bson:"base_ver,omitempty"`
}

// JSON encode une valeur pour Before/After. Une valeur inencodable rend
// une chaîne vide plutôt qu'une erreur : le journal ne doit jamais faire
// échouer l'opération qu'il décrit.
func JSON(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// Query filtre une lecture du journal.
type Query struct {
	// Env est obligatoire : le journal est cloisonné comme le reste.
	Env tenancy.EnvID
	// User, si renseigné, ne retient que ses modifications (« mon
	// activité »).
	User tenancy.UserID
	// Session, si renseignée, ne retient que celles de cette session.
	Session tenancy.SessionID
	// Kind et Target, si renseignés, ne retiennent que cette entité
	// (l'historique d'un document).
	Kind   Kind
	Target string
	// Limit : 0 -> DefaultLimit.
	Limit int
}

// DefaultLimit borne une lecture du journal.
const DefaultLimit = 200

// Matches dit si une opération satisfait la requête — hors limite et hors
// tri. En Go, comme webapp.ListQuery.Matches : un seul exemplaire du
// prédicat, partagé par la fake et par tout ce qui devra filtrer en
// mémoire.
func (q Query) Matches(o Op) bool {
	if q.Env != "" && o.Env != q.Env {
		return false
	}
	if q.User != "" && o.User != q.User {
		return false
	}
	if q.Session != "" && o.Session != q.Session {
		return false
	}
	if q.Kind != "" && o.Kind != q.Kind {
		return false
	}
	if q.Target != "" && o.Target != q.Target {
		return false
	}
	return true
}

// Journal est l'histoire des modifications commitées. Append-only : rien
// ne se réécrit, rien ne se supprime — c'est une piste d'audit, pas un
// état courant.
type Journal interface {
	Append(ctx context.Context, ops ...Op) error
	// List : du plus récent au plus ancien.
	List(ctx context.Context, q Query) ([]Op, error)
}
