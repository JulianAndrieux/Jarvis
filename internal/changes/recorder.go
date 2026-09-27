package changes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Recorder est ce que les services appellent pour journaliser une
// modification. Il complète chaque opération avec le « qui/où » de la
// portée du contexte, pour qu'aucun appelant n'ait à le répéter — ni à
// pouvoir se tromper.
//
// Une écriture de journal qui échoue n'est jamais remontée : la
// modification, elle, a déjà eu lieu ; la défaire pour une trace manquante
// serait pire. L'échec est signalé par Logf.
type Recorder struct {
	Journal Journal
	// Logf, s'il est donné, reçoit les échecs d'écriture.
	Logf func(format string, args ...any)
	// Now : horloge (nil : time.Now).
	Now func() time.Time
	// newID : injectable pour les tests.
	newID func() (string, error)
}

func (r *Recorder) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Recorder) id() (string, error) {
	if r.newID != nil {
		return r.newID()
	}
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Record journalise des opérations.
//
// Sans portée dans le contexte, ou sans journal, il ne se passe rien : un
// traitement de fond (lecture d'un document, relève des emails) n'est pas
// une modification d'un humain, et n'a rien à faire dans « qui a changé
// quoi ».
func (r *Recorder) Record(ctx context.Context, ops ...Op) {
	if r == nil || r.Journal == nil || len(ops) == 0 {
		return
	}
	scope, ok := tenancy.FromContext(ctx)
	if !ok || scope.Background() {
		return
	}
	now := r.now()
	filled := make([]Op, 0, len(ops))
	for _, o := range ops {
		id, err := r.id()
		if err != nil {
			r.logf("changes: identifiant: %v", err)
			return
		}
		o.ID = id
		o.Env, o.User, o.Session = scope.Env, scope.User, scope.Session
		if o.At.IsZero() {
			o.At = now
		}
		filled = append(filled, o)
	}
	if err := r.Journal.Append(ctx, filled...); err != nil {
		r.logf("changes: journal: %v", err)
	}
}

func (r *Recorder) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}
