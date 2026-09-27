// Package gate borne l'accès aux modèles locaux (jalon 27) : le
// traitement des documents et l'agent des tickets partagent les mêmes
// serveurs llama.cpp, qui échouent sous charge concurrente sur ce
// matériel (cf. jalon 21 bis, "Context size has been exceeded").
//
// Depuis le jalon 50, la file est **équitable entre environnements** : les
// modèles ne servent qu'un travail à la fois et un document de cinq pages
// scannées les occupe une vingtaine de minutes, donc en FIFO un
// environnement qui dépose dix documents ferait attendre tous les autres
// derrière ses dix traitements. Un tourniquet donne son tour à chaque
// environnement ; à l'intérieur d'un environnement, l'ordre d'arrivée est
// conservé.
//
// Un seul mécanisme, délibérément : deux files sur la même porte (un
// sémaphore par canal et un tourniquet tenant son propre état) ne se
// verraient pas l'une l'autre et laisseraient passer deux travaux en même
// temps — exactement ce que cette porte existe pour empêcher.
package gate

import (
	"context"
	"sync"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Profils de modèles (jalon 37, internal/models) : les documents et le
// code ne tiennent pas ensemble en mémoire.
const (
	Documents = "documents"
	Code      = "code"
)

// Gate est la porte partagée.
type Gate struct {
	// Switch, s'il est donné, charge un profil de modèles (jalon 37) —
	// appelé pendant qu'on détient la porte, donc jamais pendant un autre
	// traitement.
	Switch func(ctx context.Context, profile string) error

	slots      int
	fairOnce   sync.Once
	mu         sync.Mutex
	held       int
	pending    int
	waiting    map[tenancy.EnvID][]chan struct{}
	order      []tenancy.EnvID
	lastServed int
}

// New crée une porte laissant passer n travaux à la fois (n <= 0 : 1).
func New(n int) *Gate {
	if n <= 0 {
		n = 1
	}
	return &Gate{slots: n}
}

// Acquire attend son tour et retourne la fonction qui libère la place,
// sans charger de profil de modèles. Utilisé là où il n'y a pas
// d'environnement à équilibrer — attendre le calme avant un déploiement.
func (g *Gate) Acquire() func() {
	release, _ := g.AcquireFair(context.Background(), "", "")
	return release
}

// AcquireFor attend son tour puis charge le profil demandé, sans
// distinction d'environnement.
func (g *Gate) AcquireFor(ctx context.Context, profile string) (func(), error) {
	return g.AcquireFair(ctx, profile, "")
}
