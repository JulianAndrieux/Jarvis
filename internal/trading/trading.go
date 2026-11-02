// Package trading porte ce que l'utilisateur fait des marchés Polymarket :
// aujourd'hui la liste de suivi (jalon 53) ; ensuite les analyses, le
// modèle de risque et les ordres (jalons 54-55).
//
// Stocké dans Atlas et cloisonné par environnement, comme les notes
// (décision de l'utilisateur). La lecture des marchés eux-mêmes est
// l'affaire d'internal/polymarket : ce paquet n'appelle jamais le réseau.
package trading

import (
	"context"
	"errors"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// DefaultAlertMove : variation sur 24 h (en fraction : 0.05 = 5 points de
// probabilité) à partir de laquelle un marché suivi est signalé dans la
// barre latérale.
const DefaultAlertMove = 0.05

// Watch : un marché suivi. Slug est la clé (un marché suivi une seule fois
// par environnement).
//
// Question et EventTitle sont recopiés au moment du suivi : la liste reste
// lisible si Polymarket est injoignable. Les prix, eux, ne sont jamais
// stockés ici — ils sont relus à l'affichage.
type Watch struct {
	Env        tenancy.EnvID  `bson:"env_id"`
	Slug       string         `bson:"slug"`
	Question   string         `bson:"question"`
	EventTitle string         `bson:"event_title,omitempty"`
	AddedAt    time.Time      `bson:"added_at"`
	AddedBy    tenancy.UserID `bson:"added_by,omitempty"`
	// AlertMove : 0 = DefaultAlertMove.
	AlertMove float64 `bson:"alert_move,omitempty"`
	Note      string  `bson:"note,omitempty"`
}

// Threshold : le seuil d'alerte effectif.
func (w Watch) Threshold() float64 {
	if w.AlertMove <= 0 {
		return DefaultAlertMove
	}
	return w.AlertMove
}

var (
	ErrAlreadyWatched = errors.New("trading: marché déjà suivi")
	ErrNotFound       = errors.New("trading: introuvable")
)

// Store persiste la liste de suivi (MongoStore en production, FakeStore en
// test).
//
// Hors changeset (jalons 48-49), comme l'archivage d'un email ou la marque
// « payé » : suivre un marché est une préférence d'affichage, effective
// tout de suite, pas une donnée partagée qu'on relit avant de la publier.
type Store interface {
	// For : la même persistance vue depuis un environnement. Sans portée,
	// toute opération est refusée (tenancy.ErrNoEnv).
	For(scope tenancy.Scope) Store
	AddWatch(ctx context.Context, w Watch) error
	GetWatch(ctx context.Context, slug string) (Watch, bool, error)
	// ListWatches : le plus récemment suivi d'abord.
	ListWatches(ctx context.Context) ([]Watch, error)
	SetWatchNote(ctx context.Context, slug, note string) error
	RemoveWatch(ctx context.Context, slug string) error
}
