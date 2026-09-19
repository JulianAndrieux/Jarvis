// Package doctype est le registre des types de documents supportés par
// l'étage Extraction. Le jeu de types n'est volontairement pas fermé
// (factures, pièces d'identité, correspondance, documents techniques,
// etc.) : chaque type s'enregistre avec son struct Go, dont le JSON
// Schema est dérivé via internal/schema.
package doctype

import (
	"fmt"
	"reflect"
	"sort"
	"sync"

	"github.com/JulianAndrieux/Jarvis/internal/schema"
)

// Registration décrit un type de document enregistré : son nom (clé du
// registre), une description à l'usage humain/prompt, et le struct Go
// dont le JSON Schema d'extraction est dérivé.
type Registration struct {
	Name        string
	Description string
	Type        reflect.Type
}

// Schema dérive le JSON Schema de ce type de document (voir
// internal/schema.Derive).
func (r Registration) Schema() (map[string]any, error) {
	return schema.Derive(r.Type)
}

// Registry associe un nom de type de document à sa Registration.
type Registry struct {
	mu    sync.RWMutex
	types map[string]Registration
}

// NewRegistry crée un registre vide.
func NewRegistry() *Registry {
	return &Registry{types: map[string]Registration{}}
}

// Register ajoute le type Go T au registre sous le nom donné. T doit être
// un struct (au sens attendu par schema.Derive) ; l'erreur est retournée
// immédiatement si le nom est déjà pris, plutôt que d'écraser
// silencieusement une registration existante.
func Register[T any](r *Registry, name, description string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.types[name]; exists {
		return fmt.Errorf("doctype: %q is already registered", name)
	}

	r.types[name] = Registration{
		Name:        name,
		Description: description,
		Type:        reflect.TypeOf(*new(T)),
	}
	return nil
}

// Get retourne la Registration pour name, si elle existe.
func (r *Registry) Get(name string) (Registration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.types[name]
	return reg, ok
}

// Names retourne les noms enregistrés, triés alphabétiquement.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.types))
	for name := range r.types {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
