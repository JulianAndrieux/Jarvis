// Package models charge les modèles locaux par profil (jalon 37) : sur le
// Mac (24 Go), les modèles de documents (VLM + LLM, ~12 Go) et le modèle
// de code (Devstral Small 2, ~16 Go) ne tiennent pas ensemble. Jarvis
// bascule d'un profil à l'autre selon le travail, dans la file d'accès aux
// modèles (internal/gate) : documents et tickets ne s'exécutent jamais en
// même temps, la bascule ne coupe donc jamais un traitement.
package models

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Server : un serveur llama.cpp d'un profil.
type Server struct {
	Name string   `json:"name"`
	Port int      `json:"port"`
	Args []string `json:"args"` // arguments de llama-server (dont --port)
}

// Config : les profils (écrite par le lanceur dans ~/.jarvis/models.json).
type Config struct {
	Binary   string              `json:"binary"`  // llama-server
	LogDir   string              `json:"log_dir"` // un journal par serveur
	Profiles map[string][]Server `json:"profiles"`
}

// LoadConfig lit la configuration des profils.
func LoadConfig(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if err != nil {
		return c, fmt.Errorf("models: %w", err)
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("models: %s illisible : %w", path, err)
	}
	return c, nil
}

// SaveConfig écrit la configuration des profils.
func SaveConfig(path string, c Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Runner démarre et arrête les serveurs (ProcessRunner en production).
type Runner interface {
	Start(s Server) error
	// Stop arrête ce qui écoute sur port (rien : pas d'erreur).
	Stop(port int) error
	// Healthy : le serveur de ce port répond et a fini de charger.
	Healthy(ctx context.Context, port int) bool
}

// Switcher charge le profil demandé.
type Switcher struct {
	Config Config
	Runner Runner
	// ReadyTimeout : attente d'un serveur démarré (0 : 5 min — chargement
	// à froid d'un modèle de 14 Go).
	ReadyTimeout time.Duration
	// Log, s'il est donné, reçoit chaque bascule.
	Log func(format string, args ...any)

	poll    time.Duration // test : attente entre deux sondes
	mu      sync.Mutex
	current string
}

// Current : le profil chargé ("" : aucun encore).
func (s *Switcher) Current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// Use charge le profil : arrête d'abord les serveurs des autres profils
// (la mémoire ne tient pas les deux), puis démarre ceux du profil qui ne
// répondent pas, et attend qu'ils soient prêts. Déjà chargé et en bonne
// santé : rien à faire.
func (s *Switcher) Use(ctx context.Context, profile string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	wanted, ok := s.Config.Profiles[profile]
	if !ok {
		return fmt.Errorf("models: profil inconnu %q", profile)
	}
	keep := map[int]bool{}
	for _, srv := range wanted {
		keep[srv.Port] = true
	}
	var others []int
	for name, servers := range s.Config.Profiles {
		if name == profile {
			continue
		}
		for _, srv := range servers {
			if !keep[srv.Port] {
				others = append(others, srv.Port)
			}
		}
	}
	sort.Ints(others)
	if s.current != profile && len(others) > 0 {
		s.logf("models: bascule vers le profil %q", profile)
	}
	for _, port := range others {
		if s.Runner.Healthy(ctx, port) {
			if err := s.Runner.Stop(port); err != nil {
				return fmt.Errorf("models: arrêt du serveur :%d : %w", port, err)
			}
		}
	}
	var started []Server
	for _, srv := range wanted {
		if s.Runner.Healthy(ctx, srv.Port) {
			continue
		}
		if err := s.Runner.Start(srv); err != nil {
			return fmt.Errorf("models: démarrage de %s : %w", srv.Name, err)
		}
		started = append(started, srv)
	}
	for _, srv := range started {
		if err := s.waitReady(ctx, srv); err != nil {
			s.current = ""
			return err
		}
	}
	s.current = profile
	return nil
}

func (s *Switcher) waitReady(ctx context.Context, srv Server) error {
	timeout := s.ReadyTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	poll := s.poll
	if poll <= 0 {
		poll = time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		if s.Runner.Healthy(ctx, srv.Port) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("models: %s (:%d) jamais prêt en %s", srv.Name, srv.Port, timeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("models: attente de %s : %w", srv.Name, ctx.Err())
		case <-time.After(poll):
		}
	}
}

func (s *Switcher) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}
