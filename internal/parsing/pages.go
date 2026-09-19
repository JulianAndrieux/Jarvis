package parsing

import "github.com/JulianAndrieux/Jarvis/internal/triage"

// PagesNeedingParsing retourne les numéros de page d'un triage.Result qui
// n'ont pas de couche texte exploitable, et doivent donc passer par
// l'étage Parsing (VLM).
func PagesNeedingParsing(r triage.Result) []int {
	var pages []int
	for _, p := range r.Pages {
		if !p.Usable {
			pages = append(pages, p.Page)
		}
	}
	return pages
}
