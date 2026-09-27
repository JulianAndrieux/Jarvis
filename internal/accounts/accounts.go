// Package accounts répond à « qui est là, et qu'a-t-il le droit de voir ».
//
// Quatre choses, un seul paquet parce qu'elles n'ont aucun sens séparées :
// un User (une personne, identifiée par son compte Google), un
// Environment (un espace cloisonné), une Membership (l'appartenance d'un
// user à un environnement, avec son rôle) et une Session (ce qu'un user
// obtient en se connectant).
//
// Le paquet ne connaît ni HTTP ni Google : il reçoit un profil déjà
// vérifié (voir internal/googleauth) et rend une portée
// (tenancy.Scope). C'est ce qui le rend testable sans réseau.
package accounts

import (
	"context"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// User est une personne. L'identité vient de Google : GoogleSub est
// l'identifiant stable du compte (l'adresse, elle, peut changer).
type User struct {
	ID        tenancy.UserID `bson:"_id"`
	Email     string         `bson:"email"`
	GoogleSub string         `bson:"google_sub"`
	Name      string         `bson:"name"`
	CreatedAt time.Time      `bson:"created_at"`
	// Disabled : conservé mais interdit de se connecter.
	Disabled bool `bson:"disabled"`
}

// Environment est un espace cloisonné : ses documents, ses emails, ses
// notes, ses tâches.
type Environment struct {
	ID        tenancy.EnvID `bson:"_id"`
	Name      string        `bson:"name"`
	CreatedAt time.Time     `bson:"created_at"`
}

// Membership : un user dans un environnement, avec son rôle.
type Membership struct {
	Env  tenancy.EnvID  `bson:"env_id"`
	User tenancy.UserID `bson:"user_id"`
	Role tenancy.Role   `bson:"role"`
}

// Session est ce qu'un user obtient en se connectant.
//
// TokenHash, et non le jeton : le cookie porte un jeton aléatoire dont
// seul le SHA-256 est enregistré. Un dump de la base n'est donc pas un
// trousseau de clés vivantes.
type Session struct {
	ID         tenancy.SessionID `bson:"_id"`
	TokenHash  string            `bson:"token_hash"`
	User       tenancy.UserID    `bson:"user_id"`
	Env        tenancy.EnvID     `bson:"env_id"`
	CreatedAt  time.Time         `bson:"created_at"`
	LastSeenAt time.Time         `bson:"last_seen_at"`
	ExpiresAt  time.Time         `bson:"expires_at"`
	UserAgent  string            `bson:"user_agent"`
}

// Profile est ce qu'un fournisseur d'identité nous apprend sur une
// personne, déjà vérifié par l'appelant.
type Profile struct {
	Sub           string
	Email         string
	Name          string
	EmailVerified bool
}

// Store persiste les comptes. Volontairement non cloisonné par
// environnement : c'est lui qui décide qui appartient à quoi, il ne peut
// pas dépendre de la réponse.
type Store interface {
	// UpsertUser rattache un profil à un user : retrouvé par GoogleSub,
	// sinon par adresse (un compte créé par invitation avant toute
	// connexion), sinon créé. L'adresse et le nom sont rafraîchis.
	//
	// Ne touche jamais Disabled : cette méthode est appelée par la
	// connexion, et une connexion ne doit pas pouvoir réactiver un compte
	// désactivé. Désactiver est un acte d'administration à part
	// (SetUserDisabled).
	UpsertUser(ctx context.Context, u User) (User, error)
	// SetUserDisabled active ou désactive un compte.
	SetUserDisabled(ctx context.Context, id tenancy.UserID, disabled bool) error
	UserByID(ctx context.Context, id tenancy.UserID) (User, bool, error)
	UserByEmail(ctx context.Context, email string) (User, bool, error)
	ListUsers(ctx context.Context) ([]User, error)

	CreateEnv(ctx context.Context, e Environment) error
	Env(ctx context.Context, id tenancy.EnvID) (Environment, bool, error)
	ListEnvs(ctx context.Context) ([]Environment, error)

	// SetMembership crée ou remplace l'appartenance (un seul rôle par
	// couple environnement/user).
	SetMembership(ctx context.Context, m Membership) error
	MembershipsOfUser(ctx context.Context, u tenancy.UserID) ([]Membership, error)
	MembershipsOfEnv(ctx context.Context, e tenancy.EnvID) ([]Membership, error)
	RemoveMembership(ctx context.Context, e tenancy.EnvID, u tenancy.UserID) error

	CreateSession(ctx context.Context, s Session) error
	SessionByTokenHash(ctx context.Context, hash string) (Session, bool, error)
	// UpdateSession réécrit l'environnement courant, le dernier accès et
	// l'expiration — jamais le jeton ni le user.
	UpdateSession(ctx context.Context, s Session) error
	DeleteSession(ctx context.Context, id tenancy.SessionID) error
	// DeleteExpiredSessions retire les sessions expirées avant before.
	DeleteExpiredSessions(ctx context.Context, before time.Time) (int64, error)
}
