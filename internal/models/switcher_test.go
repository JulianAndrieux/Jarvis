package models

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRunner : des serveurs simulés, par port.
type fakeRunner struct {
	mu        sync.Mutex
	listening map[int]bool
	calls     []string
	never     map[int]bool // ports qui ne deviennent jamais prêts
}

func newFakeRunner(listening ...int) *fakeRunner {
	r := &fakeRunner{listening: map[int]bool{}, never: map[int]bool{}}
	for _, p := range listening {
		r.listening[p] = true
	}
	return r
}

func (r *fakeRunner) Start(s Server) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, fmt.Sprintf("start %d", s.Port))
	if !r.never[s.Port] {
		r.listening[s.Port] = true
	}
	return nil
}

func (r *fakeRunner) Stop(port int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, fmt.Sprintf("stop %d", port))
	delete(r.listening, port)
	return nil
}

func (r *fakeRunner) Healthy(ctx context.Context, port int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listening[port]
}

func (r *fakeRunner) Calls() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.calls, ",")
}

func testConfig() Config {
	return Config{Profiles: map[string][]Server{
		"documents": {{Name: "vlm", Port: 8080}, {Name: "llm", Port: 8081}},
		"code":      {{Name: "devstral", Port: 8082}},
	}}
}

func newSwitcher(r *fakeRunner) *Switcher {
	return &Switcher{Config: testConfig(), Runner: r, ReadyTimeout: 200 * time.Millisecond, poll: time.Millisecond}
}

func TestSwitcher_ColdStart(t *testing.T) {
	r := newFakeRunner()
	s := newSwitcher(r)
	if err := s.Use(context.Background(), "documents"); err != nil {
		t.Fatal(err)
	}
	if got := r.Calls(); got != "start 8080,start 8081" || s.Current() != "documents" {
		t.Errorf("calls = %s, current = %q", got, s.Current())
	}
}

// Déjà chargés (lancés à la main ou par un démarrage précédent) : réutilisés.
func TestSwitcher_ReusesRunningServers(t *testing.T) {
	r := newFakeRunner(8080, 8081)
	s := newSwitcher(r)
	s.Use(context.Background(), "documents")
	s.Use(context.Background(), "documents")
	if got := r.Calls(); got != "" {
		t.Errorf("calls = %s, want none", got)
	}
}

// Bascule : les serveurs de l'autre profil sont arrêtés AVANT le démarrage
// (la mémoire ne tient pas les deux).
func TestSwitcher_SwitchStopsBeforeStarting(t *testing.T) {
	r := newFakeRunner(8080, 8081)
	s := newSwitcher(r)
	s.Use(context.Background(), "documents")
	if err := s.Use(context.Background(), "code"); err != nil {
		t.Fatal(err)
	}
	if got := r.Calls(); got != "stop 8080,stop 8081,start 8082" {
		t.Errorf("calls = %s", got)
	}
	s.Use(context.Background(), "documents")
	if got := r.Calls(); !strings.HasSuffix(got, "stop 8082,start 8080,start 8081") {
		t.Errorf("calls = %s", got)
	}
}

// Un serveur du profil courant mort entre-temps est relancé.
func TestSwitcher_RestartsADeadServer(t *testing.T) {
	r := newFakeRunner(8080, 8081)
	s := newSwitcher(r)
	s.Use(context.Background(), "documents")
	r.Stop(8081)
	s.Use(context.Background(), "documents")
	if got := r.Calls(); got != "stop 8081,start 8081" {
		t.Errorf("calls = %s", got)
	}
}

// Un serveur commun à deux profils n'est pas arrêté par la bascule.
func TestSwitcher_SharedServerStays(t *testing.T) {
	r := newFakeRunner()
	s := newSwitcher(r)
	s.Config.Profiles["code"] = append(s.Config.Profiles["code"], Server{Name: "llm", Port: 8081})
	s.Use(context.Background(), "documents")
	s.Use(context.Background(), "code")
	if got := r.Calls(); strings.Contains(got, "stop 8081") {
		t.Errorf("calls = %s, shared server stopped", got)
	}
}

func TestSwitcher_Errors(t *testing.T) {
	r := newFakeRunner()
	s := newSwitcher(r)
	if err := s.Use(context.Background(), "inconnu"); err == nil {
		t.Error("unknown profile accepted")
	}
	r.never[8082] = true
	err := s.Use(context.Background(), "code")
	if err == nil || !strings.Contains(err.Error(), "devstral") {
		t.Errorf("err = %v, want the server that never got ready named", err)
	}
	if s.Current() == "code" {
		t.Error("current profile set although not ready")
	}
}

func TestLoadConfig(t *testing.T) {
	path := t.TempDir() + "/models.json"
	if err := SaveConfig(path, testConfig()); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(path)
	if err != nil || len(got.Profiles["documents"]) != 2 || got.Profiles["code"][0].Port != 8082 {
		t.Errorf("LoadConfig = %+v, %v", got, err)
	}
	if _, err := LoadConfig(t.TempDir() + "/absent.json"); err == nil {
		t.Error("missing file accepted")
	}
}
