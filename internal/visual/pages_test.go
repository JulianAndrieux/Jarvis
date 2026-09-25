package visual

import (
	"strings"
	"testing"
)

// Les pages à capturer : celles dont un gabarit est modifié par le diff.
func TestPagesFor(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/cmd/jarvisapp/templates/documents.templ b/cmd/jarvisapp/templates/documents.templ",
		"+++ b/cmd/jarvisapp/templates/documents.templ",
		"+++ b/cmd/jarvisapp/templates/documents_templ.go", // généré : ignoré
		"+++ b/cmd/jarvisapp/templates/layout.templ",
		"+++ b/internal/webapp/jobs.go",
		"+++ b/cmd/jarvisapp/templates/agents.templ",
	}, "\n")
	got := strings.Join(PagesFor(diff), ",")
	if got != "/documents,/,/admin/agents" {
		t.Errorf("PagesFor = %s", got)
	}
	if pages := PagesFor("+++ b/internal/webapp/jobs.go\n"); len(pages) != 0 {
		t.Errorf("no template changed, pages = %v", pages)
	}
	// Ticket "Revoir ordre des sections" : « / » est le tableau de bord,
	// l'import vit sur /import.
	if got := strings.Join(PagesFor("+++ b/cmd/jarvisapp/templates/upload.templ"), ","); got != "/import" {
		t.Errorf("upload.templ → %s, want /import", got)
	}
	if got := strings.Join(PagesFor("+++ b/cmd/jarvisapp/templates/job.templ"), ","); got != "/import" {
		t.Errorf("job.templ → %s, want /import (le suivi vit sur la page d'import)", got)
	}
	if got := strings.Join(PagesFor("+++ b/cmd/jarvisapp/templates/dashboard.templ"), ","); got != "/" {
		t.Errorf("dashboard.templ → %s, want /", got)
	}
	// Au plus MaxPages (chaque capture coûte une lecture par le modèle).
	var many []string
	for _, p := range []string{"documents", "notes", "tickets", "agents", "classes", "tests"} {
		many = append(many, "+++ b/cmd/jarvisapp/templates/"+p+".templ")
	}
	if n := len(PagesFor(strings.Join(many, "\n"))); n != MaxPages {
		t.Errorf("pages = %d, want %d", n, MaxPages)
	}
}
