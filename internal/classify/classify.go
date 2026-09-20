// Package classify identifie automatiquement le type d'un document
// parmi ceux enregistrés dans internal/doctype, avant l'étage
// Extraction — répond à la demande "uploader un document et laisser le
// modèle trouver le type". Réutilise le LLM d'extraction déjà en place
// (aucun nouveau modèle choisi) avec un JSON Schema dont l'énumération
// des valeurs possibles est construite dynamiquement à partir des types
// enregistrés, pour un décodage contraint plutôt qu'une simple
// instruction en langage naturel.
package classify

import "context"

// Candidate est un type de document candidat pour la classification —
// projection minimale de doctype.Registration (nom + description),
// pour ne pas faire dépendre ce paquet de internal/doctype.
type Candidate struct {
	Name        string
	Description string
}

// Result est la décision de classification. DocType est vide si aucun
// candidat ne correspond avec suffisamment de certitude — jamais un nom
// de type deviné ou par défaut.
type Result struct {
	DocType    string
	Confidence float64
}

// Classifier est le port de classification.
type Classifier interface {
	Classify(ctx context.Context, text string, candidates []Candidate) (Result, error)
}
