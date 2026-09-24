package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// Vu en réel (ticket "Déplacer le filtre date documents", plan juste) :
// Qwen3-8B n'a jamais lu les lignes du formulaire (partant de zones sans
// rapport) et n'a rien modifié. Jarvis donne d'emblée, dans la consigne,
// les lignes réelles des fichiers du plan qui contiennent les mots du
// ticket.
func TestPlanExcerpts_FindTheLinesTheTicketTalksAbout(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	b.WriteString("package templates\n\n")
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&b, "// remplissage %d\n", i)
	}
	b.WriteString("templ DocumentsPage() {\n\t<form class=\"search-bar\">\n\t\t<input type=\"text\" name=\"q\" placeholder=\"Rechercher dans les documents\"/>\n\t\t<label class=\"date-field\">Importé du <input type=\"date\" name=\"from\"/></label>\n\t\t<button type=\"submit\">Rechercher</button>\n\t</form>\n}\n")
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&b, "// suite %d\n", i)
	}
	os.MkdirAll(filepath.Join(root, "cmd/app/templates"), 0o755)
	os.WriteFile(filepath.Join(root, "cmd/app/templates/documents.templ"), []byte(b.String()), 0o644)

	req := tickets.DevRequest{
		Title: "Déplacer le filtre date documents",
		Need:  "Le filtre dates des documents est à côté de la barre de recherche. J'aimerais l'avoir en dessous",
		Plan:  "## Fichiers concernés\n- `cmd/app/templates/documents.templ`\n- `cmd/app/templates/documents_templ.go`\n- `absent.go`",
	}
	got := planExcerpts(root, req, 2000)
	for _, want := range []string{"cmd/app/templates/documents.templ", "\t\t<label class=\"date-field\">", "search-bar"} {
		if !strings.Contains(got, want) {
			t.Errorf("excerpts lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "remplissage 10") || strings.Contains(got, "documents_templ.go") || len(got) > 2000 {
		t.Errorf("excerpts too broad, include generated files, or exceed the budget:\n%s", got)
	}
	// Dans la consigne du développeur.
	model := &scriptedModel{replies: []Message{call("f", "finish", `{"summary": "ok"}`)}}
	req.Dir = root
	(&Developer{Model: model, Checker: &fakeChecker{ok: true}}).Develop(context.Background(), req, nil)
	if user := model.calls[0][1].Content; !strings.Contains(user, "date-field") {
		t.Errorf("user prompt lacks the excerpts:\n%s", user)
	}
}

func TestTicketKeywords_DropsStopWordsBeforeAndAfterSingular(t *testing.T) {
	got := strings.Join(ticketKeywords("J'aimerais l'avoir en dessous des Documents"), ",")
	if got != "document" {
		t.Errorf("keywords = %s, want document", got)
	}
}
