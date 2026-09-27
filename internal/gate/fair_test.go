package gate

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Le problème à résoudre : la porte des modèles est une file d'attente
// unique, et un document de 5 pages scannées occupe les modèles ~18
// minutes (mesuré, cf. CLAUDE.md jalon 21 bis). En FIFO, un environnement
// qui dépose dix documents fait attendre tous les autres derrière ses dix
// traitements. Un tourniquet entre environnements donne son tour à chacun.
func TestFair_TourniquetEntreEnvironnements(t *testing.T) {
	g := New(1)
	ctx := context.Background()

	// A détient la porte.
	relA, err := g.AcquireFair(ctx, Documents, "A")
	if err != nil {
		t.Fatal(err)
	}

	// A met trois travaux en attente, B un seul, dans cet ordre.
	var mu sync.Mutex
	var served []string
	var wg sync.WaitGroup
	start := make(chan struct{})

	enqueue := func(env tenancy.EnvID, label string, delay time.Duration) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			time.Sleep(delay) // pour fixer l'ordre d'arrivée
			rel, err := g.AcquireFair(ctx, Documents, env)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			served = append(served, label)
			mu.Unlock()
			rel()
		}()
	}
	enqueue("A", "A1", 0)
	enqueue("A", "A2", 20*time.Millisecond)
	enqueue("A", "A3", 40*time.Millisecond)
	enqueue("B", "B1", 60*time.Millisecond)
	close(start)
	time.Sleep(150 * time.Millisecond) // tous en attente
	relA()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(served) != 4 {
		t.Fatalf("%d travaux servis, veut 4 : %v", len(served), served)
	}
	// B ne doit pas être dernier : il est arrivé après trois travaux de A,
	// mais c'est son premier.
	if served[3] == "B1" {
		t.Errorf("ordre = %v : B a attendu derrière tous les travaux de A, le tourniquet n'a pas joué", served)
	}
	if served[0] != "A1" {
		t.Errorf("ordre = %v : le premier arrivé de A doit passer d'abord", served)
	}
}

// Dans un seul environnement, l'ordre d'arrivée est respecté.
func TestFair_FIFODansUnEnvironnement(t *testing.T) {
	g := New(1)
	ctx := context.Background()
	rel, err := g.AcquireFair(ctx, Documents, "A")
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var served []int
	var wg sync.WaitGroup
	for i := 1; i <= 3; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			time.Sleep(time.Duration(n) * 20 * time.Millisecond)
			r, err := g.AcquireFair(ctx, Documents, "A")
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			served = append(served, n)
			mu.Unlock()
			r()
		}(i)
	}
	time.Sleep(120 * time.Millisecond)
	rel()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	for i, n := range served {
		if n != i+1 {
			t.Errorf("ordre = %v, veut 1,2,3 : dans un environnement, c'est l'ordre d'arrivée", served)
			break
		}
	}
}

// Un seul détenteur à la fois : c'est toute la raison d'être de la porte
// (les serveurs llama.cpp échouent sous charge concurrente sur ce
// matériel).
func TestFair_UnSeulALaFois(t *testing.T) {
	g := New(1)
	ctx := context.Background()
	var mu sync.Mutex
	inside, maxInside := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			env := tenancy.EnvID("env-" + string(rune('A'+i%4)))
			rel, err := g.AcquireFair(ctx, Documents, env)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			inside++
			if inside > maxInside {
				maxInside = inside
			}
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			inside--
			mu.Unlock()
			rel()
		}(i)
	}
	wg.Wait()
	if maxInside != 1 {
		t.Errorf("%d détenteurs simultanés, veut 1", maxInside)
	}
}

// Un contexte annulé pendant l'attente rend la main, sans laisser la place
// occupée pour toujours.
func TestFair_ContexteAnnule(t *testing.T) {
	g := New(1)
	held, err := g.AcquireFair(context.Background(), Documents, "A")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := g.AcquireFair(ctx, Documents, "B")
		done <- err
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("une attente annulée doit rendre une erreur")
		}
	case <-time.After(time.Second):
		t.Fatal("l'attente annulée n'a pas rendu la main")
	}
	held()
	// La porte est de nouveau prenable.
	rel, err := g.AcquireFair(context.Background(), Documents, "C")
	if err != nil {
		t.Fatalf("la porte est restée bloquée : %v", err)
	}
	rel()
}

// Une bascule de profil impossible rend la place : sinon la porte resterait
// occupée par un travail qui ne peut pas commencer.
func TestFair_BasculeImpossibleRendLaPlace(t *testing.T) {
	g := New(1)
	g.Switch = func(ctx context.Context, profile string) error { return context.DeadlineExceeded }
	if _, err := g.AcquireFair(context.Background(), Code, "A"); err == nil {
		t.Fatal("une bascule impossible doit échouer")
	}
	g.Switch = nil
	rel, err := g.AcquireFair(context.Background(), Documents, "B")
	if err != nil {
		t.Fatalf("la porte est restée occupée : %v", err)
	}
	rel()
}
