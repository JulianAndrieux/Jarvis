// Package schema dérive un JSON Schema à partir d'un struct Go, pour le
// décodage contraint (guided decoding) de l'étage Extraction. Le schéma
// est la source de vérité : les structs Go décrivent la forme attendue,
// jamais l'inverse.
package schema

// Field enveloppe une valeur extraite avec sa provenance : score de
// confiance et extrait de texte source d'où la valeur a été tirée. Une
// valeur dont la confiance est sous le seuil retenu est marquée pour
// revue humaine par l'étage Extraction — jamais acceptée silencieusement.
//
// Note : le brief demande aussi une provenance par bbox (position dans la
// page). Elle n'est pas incluse ici : ni le texte extrait par
// internal/triage (pdftotext en mode texte simple) ni le Markdown produit
// par internal/vlm ne portent de coordonnées aujourd'hui. L'ajouter
// proprement suppose de faire évoluer ces deux étages (ex. pdftotext en
// mode -bbox pour le texte natif ; stratégie à définir côté VLM) — décision
// à prendre séparément, pas un détail d'implémentation de ce paquet.
type Field[T any] struct {
	Value         T       `json:"value"`
	Confidence    float64 `json:"confidence"`
	SourceSnippet string  `json:"source_snippet"`
}
