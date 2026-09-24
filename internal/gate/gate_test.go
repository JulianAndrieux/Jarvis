package gate

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGate_BoundsConcurrentHolders(t *testing.T) {
	g := New(1)
	var cur, max int32
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := g.Acquire()
			defer release()
			n := atomic.AddInt32(&cur, 1)
			for {
				m := atomic.LoadInt32(&max)
				if n <= m || atomic.CompareAndSwapInt32(&max, m, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt32(&cur, -1)
		}()
	}
	wg.Wait()
	if max != 1 {
		t.Errorf("max concurrent holders = %d, want 1", max)
	}
}

func TestGate_ZeroMeansOne(t *testing.T) {
	g := New(0)
	release := g.Acquire()
	done := make(chan struct{})
	go func() { g.Acquire()(); close(done) }()
	select {
	case <-done:
		t.Fatal("second Acquire went through while the first was held")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	<-done
}

// Jalon 37 : AcquireFor charge le profil de modèles demandé pendant qu'on
// détient la porte (jamais pendant un autre traitement).
func TestGate_AcquireForSwitchesWhileHeld(t *testing.T) {
	g := New(1)
	var got []string
	g.Switch = func(ctx context.Context, profile string) error {
		got = append(got, profile)
		return nil
	}
	release, err := g.AcquireFor(context.Background(), Code)
	if err != nil || len(got) != 1 || got[0] != Code {
		t.Fatalf("err = %v, switched = %v", err, got)
	}
	release()
	// Sans bascule configurée : simple acquisition.
	plain := New(1)
	r, err := plain.AcquireFor(context.Background(), Documents)
	if err != nil {
		t.Fatal(err)
	}
	r()
}

// Bascule impossible : la porte est rendue et l'erreur remonte.
func TestGate_AcquireForReleasesOnSwitchError(t *testing.T) {
	g := New(1)
	g.Switch = func(ctx context.Context, profile string) error { return errors.New("modèle introuvable") }
	if _, err := g.AcquireFor(context.Background(), Documents); err == nil {
		t.Fatal("error swallowed")
	}
	done := make(chan struct{})
	go func() { g.Acquire()(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("gate still held after a failed switch")
	}
}
