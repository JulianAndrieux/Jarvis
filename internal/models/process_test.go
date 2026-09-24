package models

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

// Processus auxiliaire : quand MODELS_FAKE_SERVER_PORT est défini, le
// binaire de test joue un llama-server (répond /health).
func TestMain(m *testing.M) {
	if port := os.Getenv("MODELS_FAKE_SERVER_PORT"); port != "" {
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"status":"ok"}`) })
		http.ListenAndServe("127.0.0.1:"+port, nil)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// Vrai processus : démarré, prêt, arrêté par son port.
func TestProcessRunner_StartHealthyStop(t *testing.T) {
	port := freePort(t)
	t.Setenv("MODELS_FAKE_SERVER_PORT", strconv.Itoa(port))
	r := ProcessRunner{Binary: os.Args[0], LogDir: t.TempDir()}
	ctx := context.Background()
	if r.Healthy(ctx, port) {
		t.Fatal("healthy before start")
	}
	if err := r.Start(Server{Name: "faux", Port: port, Args: []string{"-test.run=^$"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !r.Healthy(ctx, port) {
		if time.Now().After(deadline) {
			t.Fatal("never healthy")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := r.Stop(port); err != nil {
		t.Fatal(err)
	}
	if r.Healthy(ctx, port) {
		t.Error("still healthy after stop")
	}
	if _, err := os.Stat(r.LogDir + "/faux.log"); err != nil {
		t.Errorf("no log file: %v", err)
	}
	// Rien n'écoute : pas d'erreur.
	if err := r.Stop(port); err != nil {
		t.Errorf("stop on a free port: %v", err)
	}
}
