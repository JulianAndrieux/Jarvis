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
	// Au plus MaxPages (chaque capture coûte une lecture par le modèle).
	var many []string
	for _, p := range []string{"documents", "notes", "tickets", "agents", "classes", "tests"} {
		many = append(many, "+++ b/cmd/jarvisapp/templates/"+p+".templ")
	}
	if n := len(PagesFor(strings.Join(many, "\n"))); n != MaxPages {
		t.Errorf("pages = %d, want %d", n, MaxPages)
	}
}
