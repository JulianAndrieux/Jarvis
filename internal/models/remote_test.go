package models

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHealthURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://100.64.0.2:8080/v1":  "http://100.64.0.2:8080/health",
		"http://mac:8081/v1/":        "http://mac:8081/health",
		"http://127.0.0.1:8080":      "http://127.0.0.1:8080/health",
		"https://modeles.exemple/v1": "https://modeles.exemple/health",
	} {
		if got := HealthURL(in); got != want {
			t.Errorf("HealthURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRemote_Wait_ReturnsOnceEveryServerAnswers(t *testing.T) {
	var calls atomic.Int32
	loading := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path = %s, want /health", r.URL.Path)
		}
		// llama-server répond 503 tant que le modèle se charge.
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer loading.Close()
	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ready.Close()

	var logged []string
	r := Remote{URLs: []string{ready.URL + "/v1", loading.URL + "/v1"}, Poll: 5 * time.Millisecond,
		Log: func(f string, a ...any) { logged = append(logged, f) }}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if calls.Load() < 3 {
		t.Errorf("returned after %d call(s), before the server was ready", calls.Load())
	}
	// Dit une fois qu'on attend, une fois que c'est revenu — pas à chaque sonde.
	if len(logged) != 2 {
		t.Errorf("logged %d line(s), want 2 (waiting, back): %v", len(logged), logged)
	}
}

func TestRemote_Wait_StopsWithTheContextAndNamesTheServer(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := down.URL
	down.Close() // plus personne n'écoute : le Mac est éteint

	r := Remote{URLs: []string{url + "/v1"}, Poll: 5 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	err := r.Wait(ctx)
	if err == nil {
		t.Fatal("Wait on a dead server: error = nil")
	}
	if !strings.Contains(err.Error(), url) {
		t.Errorf("error = %q, want it to name %s", err, url)
	}
}

func TestRemote_Reachable(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	if !(Remote{URLs: []string{up.URL + "/v1"}}).Reachable(context.Background()) {
		t.Error("Reachable = false on a server that answers")
	}
	if (Remote{URLs: []string{up.URL + "/v1", "http://127.0.0.1:1/v1"}}).Reachable(context.Background()) {
		t.Error("Reachable = true although one server is down")
	}
}
