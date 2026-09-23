package gate

import (
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
