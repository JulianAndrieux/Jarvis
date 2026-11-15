package models

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Remote : des serveurs de modèles tenus par une autre machine (instance
// hébergée dont l'inférence reste sur le Mac, joint par un réseau privé).
// Jarvis ne les démarre ni ne les arrête : il ne peut qu'attendre qu'ils
// répondent — la machine peut être éteinte ou en veille.
type Remote struct {
	// URLs : adresses de base compatibles OpenAI (".../v1").
	URLs []string
	// Poll : intervalle entre deux sondes (0 : 30 s).
	Poll time.Duration
	// HTTP : nil, un client à délai de 5 s.
	HTTP *http.Client
	Log  func(format string, args ...any)
}

// HealthURL : l'adresse /health d'un llama-server à partir de son adresse
// de base (".../v1").
func HealthURL(base string) string {
	base = strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")
	return base + "/health"
}

// Reachable : tous les serveurs répondent maintenant.
func (r Remote) Reachable(ctx context.Context) bool {
	return r.firstDown(ctx) == ""
}

// Wait bloque jusqu'à ce que tous les serveurs répondent 200 sur /health
// (llama-server répond 503 tant qu'un modèle se charge), ou que ctx
// s'arrête — l'erreur nomme alors le serveur qui manque.
func (r Remote) Wait(ctx context.Context) error {
	poll := r.Poll
	if poll <= 0 {
		poll = 30 * time.Second
	}
	waiting := false
	for {
		down := r.firstDown(ctx)
		if down == "" {
			if waiting && r.Log != nil {
				r.Log("modèles : de nouveau joignables, les traitements reprennent")
			}
			return nil
		}
		if !waiting && r.Log != nil {
			r.Log("modèles : %s ne répond pas (machine éteinte, en veille ou hors du réseau privé ?) — les documents attendent", down)
		}
		waiting = true
		select {
		case <-ctx.Done():
			return fmt.Errorf("models: %s ne répond pas : %w", down, ctx.Err())
		case <-time.After(poll):
		}
	}
}

// firstDown : l'adresse du premier serveur qui ne répond pas ("" : aucun).
func (r Remote) firstDown(ctx context.Context) string {
	client := r.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	for _, base := range r.URLs {
		url := HealthURL(base)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return url
		}
		resp, err := client.Do(req)
		if err != nil {
			return url
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return url
		}
	}
	return ""
}
