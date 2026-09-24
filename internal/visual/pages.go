// Package visual relit visuellement un changement d'interface (jalon 38) :
// la version du ticket et la version en service sont lancées côte à côte,
// les pages concernées capturées, et un modèle de vision juge si le rendu
// répond au besoin. Vu en réel : des diffs corrects pour le compilateur et
// les tests, mais dont la mise en page n'était pas celle demandée (« à
// droite au lieu d'en dessous ») — ce qu'aucun test textuel ne voit.
package visual

import (
	"path/filepath"
	"strings"
)

// MaxPages : pages capturées au plus (chaque capture est lue par le
// modèle de vision, lent sur ce matériel).
const MaxPages = 3

// pageOf : la page principale de chaque gabarit de cmd/jarvisapp.
var pageOf = map[string]string{
	"upload":       "/",
	"layout":       "/",
	"job":          "/",
	"documents":    "/documents",
	"notes":        "/notes",
	"sidebar":      "/tasks",
	"tickets":      "/tickets",
	"agents":       "/admin/agents",
	"architecture": "/admin/architecture",
	"classes":      "/admin/classes",
	"model":        "/admin/model",
	"tests":        "/admin/tests",
	"code":         "/admin/classes",
}

// PagesFor : les pages dont un gabarit (.templ) est modifié par le diff,
// dans l'ordre du diff, sans doublon, MaxPages au plus.
func PagesFor(diff string) []string {
	var pages []string
	seen := map[string]bool{}
	for _, line := range strings.Split(diff, "\n") {
		path, ok := strings.CutPrefix(line, "+++ b/")
		if !ok || !strings.HasSuffix(path, ".templ") {
			continue
		}
		page, ok := pageOf[strings.TrimSuffix(filepath.Base(path), ".templ")]
		if !ok {
			page = "/"
		}
		if !seen[page] && len(pages) < MaxPages {
			seen[page] = true
			pages = append(pages, page)
		}
	}
	return pages
}
