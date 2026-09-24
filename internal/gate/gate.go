// Package gate borne l'accès aux modèles locaux (jalon 27) : le
// traitement des documents et l'agent des tickets partagent les mêmes
// serveurs llama.cpp, qui échouent sous charge concurrente sur ce
// matériel (cf. jalon 21 bis, "Context size has been exceeded").
package gate

import "context"

// Profils de modèles (jalon 37, internal/models) : les documents et le
// code ne tiennent pas ensemble en mémoire.
const (
	Documents = "documents"
	Code      = "code"
)

// Gate est un sémaphore partagé.
type Gate struct {
	ch chan struct{}
	// Switch, s'il est donné, charge un profil de modèles (jalon 37) —
	// appelé par AcquireFor pendant qu'on détient la porte, donc jamais
	// pendant un autre traitement.
	Switch func(ctx context.Context, profile string) error
}

// New crée une porte laissant passer n détenteurs à la fois (n <= 0 : 1).
func New(n int) *Gate {
	if n <= 0 {
		n = 1
	}
	return &Gate{ch: make(chan struct{}, n)}
}

// Acquire attend son tour et retourne la fonction qui libère la place.
func (g *Gate) Acquire() func() {
	g.ch <- struct{}{}
	return func() { <-g.ch }
}

// AcquireFor attend son tour puis charge le profil de modèles demandé. Si
// la bascule échoue, la porte est rendue et l'erreur retournée.
func (g *Gate) AcquireFor(ctx context.Context, profile string) (func(), error) {
	release := g.Acquire()
	if g.Switch != nil {
		if err := g.Switch(ctx, profile); err != nil {
			release()
			return nil, err
		}
	}
	return release, nil
}
