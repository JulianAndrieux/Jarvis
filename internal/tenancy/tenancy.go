// Package tenancy porte l'identité d'une opération : dans quel
// environnement elle a lieu, pour quel user, dans quelle session.
//
// Un environnement est un espace cloisonné (ses documents, ses emails,
// ses notes, ses tâches, ses tickets) ; l'installation existante est
// l'environnement Local. Un user appartient à un ou plusieurs
// environnements ; une session est ce qu'il obtient en se connectant.
//
// Ce paquet ne dépend de rien d'autre du projet, pour que tous les
// stores puissent l'importer sans cycle. Il ne contient aucune règle
// métier : seulement le « qui/où », et de quoi le transporter.
package tenancy

import (
	"context"
	"errors"
)

// EnvID, UserID, SessionID sont des types distincts et non des string,
// pour qu'aucun appel ne puisse les échanger par erreur.
type EnvID string
type UserID string
type SessionID string

// Local est l'environnement de l'installation existante — celui de
// l'utilisateur qui a construit Jarvis. Les données antérieures au
// cloisonnement lui sont attribuées par la migration.
const Local EnvID = "local"

// LocalUser et LocalSession sont l'identité de l'installation
// mono-utilisateur, quand aucune authentification n'est configurée.
//
// Une session nommée plutôt qu'une portée sans session : « pas de session »
// signifie « travail de fond », et le travail de fond n'a ni journal ni
// changeset. L'utilisateur d'une installation locale, lui, est bien un
// humain qui modifie des choses — ses modifications doivent être tracées et
// pouvoir être mises en attente comme celles de n'importe qui.
const (
	LocalUser    UserID    = "local"
	LocalSession SessionID = "local"
)

// LocalScope est la portée de cette installation mono-utilisateur.
func LocalScope() Scope {
	return Scope{Env: Local, User: LocalUser, Session: LocalSession, Role: RoleOwner}
}

// Role : ce qu'un user peut faire. RoleOwner est le propriétaire de
// l'instance (le seul à disposer de l'espace Admin, des tickets et du
// déploiement — un déploiement redémarre l'application de tous les
// environnements) ; RoleAdmin administre son propre environnement ;
// RoleMember l'utilise.
type Role string

const (
	RoleMember Role = "member"
	RoleAdmin  Role = "admin"
	RoleOwner  Role = "owner"
)

// ErrNoEnv : portée sans environnement. Les stores la reconnaissent
// (errors.Is) pour distinguer une erreur de programmation d'un échec de
// persistance.
var ErrNoEnv = errors.New("tenancy: portée sans environnement")

// Scope est le « qui/où » d'une opération.
//
// Il ne se reconstruit jamais depuis une URL ni depuis un formulaire : il
// vient du cookie de session, résolu une seule fois par le middleware —
// sinon changer d'environnement se ferait en éditant une adresse.
//
// Session vide signifie « travail de fond » (traitement d'un document,
// relève des emails, agent d'un ticket) : une écriture qui n'appartient à
// aucune session, donc qui ne passe par aucun changeset.
type Scope struct {
	Env     EnvID
	User    UserID
	Session SessionID
	Role    Role
}

// System est la portée du travail de fond dans un environnement.
func System(env EnvID) Scope { return Scope{Env: env, Role: RoleOwner} }

// Valid : un environnement est obligatoire, c'est lui qui cloisonne.
func (s Scope) Valid() error {
	if s.Env == "" {
		return ErrNoEnv
	}
	return nil
}

// Background : écriture de la machine, hors de toute session.
func (s Scope) Background() bool { return s.Session == "" }

// CanAdmin : administrateur de son environnement (ou propriétaire).
func (s Scope) CanAdmin() bool { return s.Role == RoleAdmin || s.Role == RoleOwner }

// IsOwner : propriétaire de l'instance.
func (s Scope) IsOwner() bool { return s.Role == RoleOwner }

type ctxKey struct{}

// WithScope attache une portée au contexte. Une portée invalide n'est pas
// attachée : l'erreur doit se voir à sa cause, pas au premier accès au
// store, très loin en aval.
func WithScope(ctx context.Context, s Scope) context.Context {
	if s.Valid() != nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, s)
}

// FromContext retourne la portée attachée au contexte, si elle y est.
func FromContext(ctx context.Context) (Scope, bool) {
	s, ok := ctx.Value(ctxKey{}).(Scope)
	return s, ok
}
