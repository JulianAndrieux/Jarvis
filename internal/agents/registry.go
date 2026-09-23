// Package agents recense les agents (modèles pilotés par un prompt) de
// Jarvis : classification et extraction des documents, analyse et
// développement des tickets. Chaque agent a un prompt par défaut, dans
// le code ; l'utilisateur peut le remplacer depuis l'interface. Le
// prompt en vigueur est persisté (MongoDB en production) et relu par
// l'agent à chaque appel : une modification s'applique sans redémarrage.
package agents

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// MaxHistory borne les versions précédentes gardées par agent.
const MaxHistory = 20

// Placeholder est un repère {{Name}} que le prompt doit contenir : le
// code le remplace par des données de l'appel (types candidats,
// description du type de document...).
type Placeholder struct {
	Name    string
	Meaning string
}

// Definition décrit un agent. Seul le prompt est modifiable ; le reste
// vient du code et de la configuration.
type Definition struct {
	ID   string
	Name string
	// Group regroupe les agents à l'affichage ("Documents", "Tickets").
	Group string
	// Role : ce que fait l'agent, en une ou deux phrases.
	Role string
	// Model : modèle utilisé (renseigné au démarrage, depuis les options).
	Model string
	// Tools : outils que l'agent peut appeler (vide : aucun).
	Tools         []string
	DefaultPrompt string
	Placeholders  []Placeholder
	// Appended : ce que le code ajoute toujours au prompt (contexte,
	// directives) — affiché pour que l'utilisateur ne le recopie pas.
	Appended string
}

// Version est un prompt tel qu'il était en vigueur jusqu'à At.
type Version struct {
	Prompt string
	At     time.Time
}

// Record est ce qui est persisté pour un agent. Prompt vide : le prompt
// par défaut est en vigueur.
type Record struct {
	ID        string
	Prompt    string
	UpdatedAt time.Time
	History   []Version
}

// Store persiste les Record (MongoStore en production, FakeStore en test).
type Store interface {
	List(ctx context.Context) ([]Record, error)
	Save(ctx context.Context, r Record) error
}

// Agent est la vue complète d'un agent : définition + prompt en vigueur.
type Agent struct {
	Definition
	Prompt    string
	Custom    bool
	UpdatedAt time.Time
	History   []Version
}

// Registry garde en mémoire le prompt en vigueur de chaque agent (lu à
// chaque appel de modèle : pas d'aller-retour MongoDB par page extraite)
// et écrit chaque modification dans le Store avant de l'appliquer.
type Registry struct {
	store Store
	defs  []Definition

	mu      sync.RWMutex
	records map[string]Record
}

// NewRegistry charge les prompts enregistrés pour defs. Un Record dont
// l'agent n'existe plus est ignoré.
func NewRegistry(ctx context.Context, store Store, defs []Definition) (*Registry, error) {
	recs, err := store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("agents: chargement : %w", err)
	}
	r := &Registry{store: store, defs: defs, records: map[string]Record{}}
	for _, rec := range recs {
		if _, ok := r.def(rec.ID); ok {
			r.records[rec.ID] = rec
		}
	}
	return r, nil
}

func (r *Registry) def(id string) (Definition, bool) {
	for _, d := range r.defs {
		if d.ID == id {
			return d, true
		}
	}
	return Definition{}, false
}

// Prompt est le prompt en vigueur de l'agent id ("" si id est inconnu).
func (r *Registry) Prompt(id string) string {
	a, _ := r.Get(id)
	return a.Prompt
}

// PromptFunc : le prompt en vigueur, relu à chaque appel — à donner aux
// agents pour qu'une modification s'applique sans redémarrage.
func (r *Registry) PromptFunc(id string) func() string {
	return func() string { return r.Prompt(id) }
}

// Get retourne l'agent id.
func (r *Registry) Get(id string) (Agent, bool) {
	d, ok := r.def(id)
	if !ok {
		return Agent{}, false
	}
	r.mu.RLock()
	rec := r.records[id]
	r.mu.RUnlock()
	a := Agent{Definition: d, Prompt: d.DefaultPrompt, UpdatedAt: rec.UpdatedAt, History: rec.History}
	if rec.Prompt != "" {
		a.Prompt, a.Custom = rec.Prompt, true
	}
	return a, true
}

// Agents retourne tous les agents, dans l'ordre des définitions.
func (r *Registry) Agents() []Agent {
	out := make([]Agent, 0, len(r.defs))
	for _, d := range r.defs {
		a, _ := r.Get(d.ID)
		out = append(out, a)
	}
	return out
}

// SetPrompt remplace le prompt de l'agent id (espaces de début et de fin
// retirés) après validation ; l'ancien passe dans l'historique.
func (r *Registry) SetPrompt(ctx context.Context, id, prompt string) error {
	d, ok := r.def(id)
	if !ok {
		return fmt.Errorf("agents: agent inconnu %q", id)
	}
	prompt = strings.TrimSpace(prompt)
	if err := Validate(d, prompt); err != nil {
		return err
	}
	return r.save(ctx, id, prompt)
}

// Reset rétablit le prompt par défaut de l'agent id.
func (r *Registry) Reset(ctx context.Context, id string) error {
	if _, ok := r.def(id); !ok {
		return fmt.Errorf("agents: agent inconnu %q", id)
	}
	return r.save(ctx, id, "")
}

// save : prompt vide = prompt par défaut. Rien ne change en mémoire si
// le Store refuse.
func (r *Registry) save(ctx context.Context, id, prompt string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, _ := r.getLocked(id)
	rec := r.records[id]
	next := Record{ID: id, Prompt: prompt, UpdatedAt: time.Now()}
	next.History = append([]Version{{Prompt: current, At: next.UpdatedAt}}, rec.History...)
	if len(next.History) > MaxHistory {
		next.History = next.History[:MaxHistory]
	}
	if err := r.store.Save(ctx, next); err != nil {
		return fmt.Errorf("agents: enregistrement de %s : %w", id, err)
	}
	r.records[id] = next
	return nil
}

// getLocked : le prompt en vigueur, verrou déjà pris.
func (r *Registry) getLocked(id string) (string, bool) {
	d, ok := r.def(id)
	if !ok {
		return "", false
	}
	if p := r.records[id].Prompt; p != "" {
		return p, true
	}
	return d.DefaultPrompt, true
}

var placeholderRe = regexp.MustCompile(`\{\{\s*([^{}]*?)\s*\}\}`)

// Validate vérifie un prompt pour la définition d : non vide, chaque
// repère obligatoire présent, aucun repère inconnu (une faute de frappe
// laisserait le repère tel quel dans le prompt envoyé au modèle).
func Validate(d Definition, prompt string) error {
	if strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("le prompt est vide")
	}
	known := map[string]bool{}
	for _, p := range d.Placeholders {
		known[p.Name] = true
		if !strings.Contains(prompt, "{{"+p.Name+"}}") {
			return fmt.Errorf("le repère {{%s}} manque (%s)", p.Name, p.Meaning)
		}
	}
	for _, m := range placeholderRe.FindAllStringSubmatch(prompt, -1) {
		if !known[m[1]] {
			return fmt.Errorf("repère inconnu {{%s}} : cet agent n'accepte que %s", m[1], placeholderList(d))
		}
	}
	return nil
}

func placeholderList(d Definition) string {
	if len(d.Placeholders) == 0 {
		return "aucun repère"
	}
	var names []string
	for _, p := range d.Placeholders {
		names = append(names, "{{"+p.Name+"}}")
	}
	return strings.Join(names, ", ")
}
