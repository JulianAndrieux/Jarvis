// Package triage implémente l'étage 1 du pipeline : décider si un PDF a
// déjà une couche texte exploitable, pour décider si l'étage Parsing (VLM)
// est nécessaire.
package triage

// PageText est le texte brut extrait d'une page, tel que retourné par un
// TextExtractor.
type PageText struct {
	// Page est le numéro de page, 1-indexé.
	Page int
	Text string
}
