package launcher

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPortOpen_ListeningPort_ReturnsTrue(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	if !PortOpen(ln.Addr().String(), 200*time.Millisecond) {
		t.Errorf("PortOpen(%s) = false, want true", ln.Addr().String())
	}
}

func TestPortOpen_ClosedPort_ReturnsFalse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	if PortOpen(addr, 200*time.Millisecond) {
		t.Errorf("PortOpen(%s) = true, want false (port was closed)", addr)
	}
}

func TestWaitHealthy_BecomesHealthyAfterRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := WaitHealthy(ctx, srv.URL+"/health", 20*time.Millisecond); err != nil {
		t.Fatalf("WaitHealthy() error = %v, want nil once the server reports healthy", err)
	}
	if atomic.LoadInt32(&calls) < 3 {
		t.Errorf("calls = %d, want at least 3 (proves it actually retried)", calls)
	}
}

func TestWaitHealthy_NeverHealthy_ReturnsErrorOnContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := WaitHealthy(ctx, srv.URL+"/health", 20*time.Millisecond)
	if err == nil {
		t.Fatal("WaitHealthy() error = nil, want a timeout error")
	}
}

func TestWaitHealthy_ServerNeverUp_ReturnsErrorOnContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := WaitHealthy(ctx, "http://127.0.0.1:1/health", 20*time.Millisecond)
	if err == nil {
		t.Fatal("WaitHealthy() error = nil, want a timeout error for an unreachable server")
	}
}
