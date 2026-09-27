package changes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Pending est une opération en attente : la même Op, mais pas encore
// commitée — donc invisible de tous sauf de son auteur.
//
// Un changeset appartient au couple (environnement, user), pas à la
// session : un user qui passe de son portable à son téléphone doit
// retrouver ses modifications en attente. Chaque opération garde tout de
// même la session qui l'a produite, pour la traçabilité.
type Pending struct {
	Op
	// Applied : l'opération a été commitée. Gardée un instant pour que le
	// commit soit rejouable en cas d'interruption, puis nettoyée.
	Applied bool `bson:"applied,omitempty"`
}

// ChangesetStore persiste les opérations en attente.
//
// Contrairement au Journal, ses écritures sont la donnée elle-même : un
// échec doit remonter à l'appelant, pas être seulement journalisé.
type ChangesetStore interface {
	// Stage ajoute des opérations en attente. Append-only, comme le fil
	// d'un ticket : une écriture concurrente ne peut pas en perdre une.
	Stage(ctx context.Context, ops ...Op) error
	// Pending rend les opérations en attente d'un user, dans l'ordre où
	// elles ont été produites — l'ordre compte, une modification peut en
	// suivre une autre sur le même champ.
	Pending(ctx context.Context, env tenancy.EnvID, user tenancy.UserID) ([]Op, error)
	// Drop retire une opération en attente (abandon d'une modification).
	Drop(ctx context.Context, env tenancy.EnvID, user tenancy.UserID, opID string) error
	// DropAll vide le changeset d'un user (tout abandonner, ou nettoyage
	// après un commit réussi).
	DropAll(ctx context.Context, env tenancy.EnvID, user tenancy.UserID) error
}

// FakeChangesetStore est un ChangesetStore en mémoire, pour les tests.
type FakeChangesetStore struct {
	mu  sync.Mutex
	ops []Op
}

func NewFakeChangesetStore() *FakeChangesetStore { return &FakeChangesetStore{} }

func (s *FakeChangesetStore) Stage(ctx context.Context, ops ...Op) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ops = append(s.ops, ops...)
	return nil
}

func (s *FakeChangesetStore) Pending(ctx context.Context, env tenancy.EnvID, user tenancy.UserID) ([]Op, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Op
	for _, o := range s.ops {
		if o.Env == env && o.User == user {
			out = append(out, o)
		}
	}
	sort.SliceStable(out, func(i, k int) bool { return out[i].At.Before(out[k].At) })
	return out, nil
}

func (s *FakeChangesetStore) Drop(ctx context.Context, env tenancy.EnvID, user tenancy.UserID, opID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.ops[:0]
	for _, o := range s.ops {
		if o.Env == env && o.User == user && o.ID == opID {
			continue
		}
		kept = append(kept, o)
	}
	s.ops = kept
	return nil
}

func (s *FakeChangesetStore) DropAll(ctx context.Context, env tenancy.EnvID, user tenancy.UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.ops[:0]
	for _, o := range s.ops {
		if o.Env == env && o.User == user {
			continue
		}
		kept = append(kept, o)
	}
	s.ops = kept
	return nil
}

// Stager attribue et enregistre les opérations en attente. Même rôle que
// Recorder pour le journal — compléter le « qui/où » depuis la portée —
// mais ses échecs remontent : une opération non enregistrée est une
// modification perdue, pas une trace manquante.
type Stager struct {
	Store ChangesetStore
	Now   func() time.Time
	newID func() (string, error)
}

// Stage enregistre des opérations en attente pour la portée du contexte.
// Sans session (travail de fond), il n'y a rien à mettre en attente :
// l'appelant doit écrire directement.
func (s *Stager) Stage(ctx context.Context, ops ...Op) error {
	if len(ops) == 0 {
		return nil
	}
	scope, ok := tenancy.FromContext(ctx)
	if !ok || scope.Background() {
		return fmt.Errorf("changes: mise en attente sans session")
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	filled := make([]Op, 0, len(ops))
	for i, o := range ops {
		id, err := s.id()
		if err != nil {
			return err
		}
		o.ID = id
		o.Env, o.User, o.Session = scope.Env, scope.User, scope.Session
		if o.At.IsZero() {
			// Un rang distinct par opération du même appel : l'ordre dans
			// lequel elles ont été produites doit survivre.
			o.At = now.Add(time.Duration(i) * time.Microsecond)
		}
		filled = append(filled, o)
	}
	return s.Store.Stage(ctx, filled...)
}

func (s *Stager) id() (string, error) {
	if s.newID != nil {
		return s.newID()
	}
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("changes: identifiant: %w", err)
	}
	return hex.EncodeToString(b), nil
}
