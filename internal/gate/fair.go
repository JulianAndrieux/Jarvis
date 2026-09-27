package gate

import (
	"context"
	"fmt"
	"sync"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// AcquireFair attend son tour dans une file **équitable entre
// environnements**, puis charge le profil de modèles demandé.
//
// Pourquoi pas la file unique d'Acquire : les modèles locaux ne servent
// qu'un travail à la fois (les serveurs llama.cpp échouent sous charge
// concurrente sur ce matériel, cf. jalon 21 bis) et un document de cinq
// pages scannées les occupe une vingtaine de minutes. En FIFO, un
// environnement qui dépose dix documents fait attendre tous les autres
// derrière ses dix traitements. Le tourniquet donne son tour à chaque
// environnement à tour de rôle ; à l'intérieur d'un environnement, l'ordre
// d'arrivée est conservé.
//
// L'inférence reste sérialisée pour toujours sur cette machine : ce
// mécanisme ne la rend pas plus rapide, il la rend équitable.
func (g *Gate) AcquireFair(ctx context.Context, profile string, env tenancy.EnvID) (func(), error) {
	g.fairOnce.Do(func() {
		g.waiting = map[tenancy.EnvID][]chan struct{}{}
		// -1 : personne n'a encore été servi, donc la première libération
		// part du premier environnement qui s'est mis en file (sans quoi le
		// tourniquet sauterait son tour).
		g.lastServed = -1
	})

	g.mu.Lock()
	if g.held < g.slotCount() && g.pending == 0 {
		// De la place, et personne devant : on prend tout de suite.
		g.held++
		g.mu.Unlock()
		return g.enter(ctx, profile)
	}
	// Sinon on prend un rang dans la file de son environnement.
	turn := make(chan struct{})
	g.waiting[env] = append(g.waiting[env], turn)
	g.pending++
	if _, known := g.envIndex(env); !known {
		g.order = append(g.order, env)
	}
	g.mu.Unlock()

	select {
	case <-turn:
		return g.enter(ctx, profile)
	case <-ctx.Done():
		// Retirer son rang : sans ça, la place lui serait donnée plus tard et
		// personne ne la libérerait.
		g.withdraw(env, turn)
		return nil, fmt.Errorf("gate: attente annulée: %w", ctx.Err())
	}
}

// enter charge le profil demandé et rend la fonction de libération. Si la
// bascule échoue, la place est rendue : sinon la porte resterait occupée
// par un travail qui ne peut pas commencer.
//
// Un profil vide ne déclenche aucune bascule : certains appelants veulent
// seulement attendre le calme, pas changer les modèles chargés.
func (g *Gate) enter(ctx context.Context, profile string) (func(), error) {
	if g.Switch != nil && profile != "" {
		if err := g.Switch(ctx, profile); err != nil {
			g.releaseFair()
			return nil, err
		}
	}
	var once sync.Once
	return func() { once.Do(g.releaseFair) }, nil
}

// releaseFair passe la main au prochain environnement du tourniquet.
func (g *Gate) releaseFair() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held > 0 {
		g.held--
	}
	if g.pending == 0 {
		return
	}
	// Tourniquet : on repart de l'environnement qui suit le dernier servi.
	n := len(g.order)
	for i := 1; i <= n; i++ {
		env := g.order[(g.lastServed+i)%n]
		queue := g.waiting[env]
		if len(queue) == 0 {
			continue
		}
		turn := queue[0]
		g.waiting[env] = queue[1:]
		g.pending--
		g.lastServed = (g.lastServed + i) % n
		g.held++
		close(turn)
		return
	}
	// Des rangs comptés mais aucune file : plus personne n'attend.
	g.pending = 0
}

// slotCount : le nombre de travaux servis en parallèle (appelé sous verrou).
func (g *Gate) slotCount() int {
	if g.slots <= 0 {
		return 1
	}
	return g.slots
}

// withdraw retire un rang d'attente abandonné (contexte annulé).
func (g *Gate) withdraw(env tenancy.EnvID, turn chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	queue := g.waiting[env]
	for i, c := range queue {
		if c != turn {
			continue
		}
		g.waiting[env] = append(queue[:i:i], queue[i+1:]...)
		g.pending--
		return
	}
	// Déjà servi entre-temps : on tient la porte sans le savoir, il faut la
	// rendre, sinon elle ne se libérerait jamais.
	g.mu.Unlock()
	g.releaseFair()
	g.mu.Lock()
}

func (g *Gate) envIndex(env tenancy.EnvID) (int, bool) {
	for i, e := range g.order {
		if e == env {
			return i, true
		}
	}
	return 0, false
}
