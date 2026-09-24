package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// Vus en réel sur le ticket "Déplacer le filtre date documents" (analyse
// réussie, développement sans aucune modification, deux fois).

// templSample : un gabarit indenté par tabulations, comme le code du projet.
const templSample = "package templates\n\ntempl Page() {\n\t<form class=\"search-bar\">\n\t\t<input type=\"text\" name=\"q\"/>\n\t\t<label class=\"date-field\">du <input type=\"date\" name=\"from\"/></label>\n\t\t<button type=\"submit\">Rechercher</button>\n\t</form>\n}\n"

func editRepo(t *testing.T) (DevTools, string) {
	d, root := devTools(t)
	os.MkdirAll(filepath.Join(root, "cmd/app/templates"), 0o755)
	os.WriteFile(filepath.Join(root, "cmd/app/templates/page.templ"), []byte(templSample), 0o644)
	return d, root
}

func edit(d DevTools, old, new string) string {
	args, _ := jsonArgs(map[string]string{"path": "cmd/app/templates/page.templ", "old": old, "new": new})
	return d.Execute(context.Background(), ToolCall{Name: "edit_file", Arguments: args})
}

func readPage(t *testing.T, root string) string {
	b, _ := os.ReadFile(filepath.Join(root, "cmd/app/templates/page.templ"))
	return string(b)
}

// Le modèle recopie l'extrait avec des espaces au lieu des tabulations :
// retrouvé (unique), remplacé, et le nouveau texte prend l'indentation
// réelle du fichier.
func TestEditFile_ToleratesSpacesForTabs(t *testing.T) {
	d, root := editRepo(t)
	old := "        <label class=\"date-field\">du <input type=\"date\" name=\"from\"/></label>\n        <button type=\"submit\">Rechercher</button>"
	new := "        <button type=\"submit\">Rechercher</button>\n    </form>\n    <div class=\"date-row\">\n        <label class=\"date-field\">du <input type=\"date\" name=\"from\"/></label>\n    </div>"
	out := edit(d, old, new)
	if !strings.HasPrefix(out, "Modifié") {
		t.Fatalf("edit = %q", out)
	}
	got := readPage(t, root)
	want := "\t\t<button type=\"submit\">Rechercher</button>\n\t</form>\n\t<div class=\"date-row\">\n\t\t<label class=\"date-field\">du <input type=\"date\" name=\"from\"/></label>\n\t</div>\n"
	if !strings.Contains(got, want) {
		t.Errorf("file =\n%s\nwant it to contain, tab-indented:\n%s", got, want)
	}
	if !strings.Contains(out, "indentation") {
		t.Errorf("out = %q, want the model told the match ignored indentation", out)
	}
}

// Le modèle recopie les numéros de ligne affichés par read_file.
func TestEditFile_ToleratesCopiedLineNumbers(t *testing.T) {
	d, root := editRepo(t)
	old := "7\t\t\t<button type=\"submit\">Rechercher</button>\n8\t\t</form>"
	out := edit(d, old, "\t\t<button type=\"submit\">Chercher</button>\n\t</form>")
	if !strings.HasPrefix(out, "Modifié") || !strings.Contains(readPage(t, root), "\t\t<button type=\"submit\">Chercher</button>\n\t</form>") {
		t.Errorf("edit = %q, file =\n%s", out, readPage(t, root))
	}
}

// Sans tenir compte de l'indentation, un extrait présent deux fois reste
// refusé (jamais de remplacement au hasard).
func TestEditFile_LooseMatchMustBeUnique(t *testing.T) {
	d, root := devTools(t)
	os.WriteFile(filepath.Join(root, "x.go"), []byte("package x\n\nfunc a() {\n\treturn\n}\n\nfunc b() {\n\t\treturn\n}\n"), 0o644)
	args, _ := jsonArgs(map[string]string{"path": "x.go", "old": "  return", "new": "  panic(1)"})
	if out := d.Execute(context.Background(), ToolCall{Name: "edit_file", Arguments: args}); !strings.Contains(out, "2 fois") {
		t.Errorf("edit = %q, want an ambiguity refusal", out)
	}
}

// Extrait reformulé : les lignes proches sont trouvées par mots-clés,
// même si la première ligne n'existe pas telle quelle.
func TestEditFile_NotFoundShowsLinesByKeyword(t *testing.T) {
	d, _ := editRepo(t)
	out := edit(d, "<div class=\"date-field\">du <input type=\"date\"/></div>", "x")
	if !strings.Contains(out, "Lignes réelles les plus proches") || !strings.Contains(out, `name="from"`) {
		t.Errorf("edit = %q, want nearby real lines", out)
	}
}

func TestRunTests_AcceptsPackageWithoutDotSlash(t *testing.T) {
	d, _ := devTools(t)
	out := d.Execute(context.Background(), ToolCall{Name: "run_tests", Arguments: `{"package": "cmd/jarvisapp"}`})
	if !strings.HasPrefix(out, "SUCCÈS") || d.Checker.(*fakeChecker).gotPkg != "./cmd/jarvisapp" {
		t.Errorf("run_tests = %q, pkg = %q", out, d.Checker.(*fakeChecker).gotPkg)
	}
}

// Relire un fichier déjà lu est permis : la compaction retire les
// anciennes sorties en disant « relis le fichier si besoin », et une
// modification peut l'avoir changé. Seul un appel stérile répété est
// bloqué.
func TestLoop_SuccessfulReadsCanBeRepeated(t *testing.T) {
	model := &scriptedModel{replies: []Message{
		call("r1", "read_file", `{"path": "internal/store/store.go"}`),
		call("r2", "read_file", `{"path": "internal/store/store.go"}`),
		call("m1", "read_file", `{"path": "absent.go"}`),
		call("m2", "read_file", `{"path": "absent.go"}`),
		call("f", "finish", `{"summary": "ok"}`),
	}}
	d := &Developer{Model: model, Checker: &fakeChecker{ok: true}}
	var steps []string
	d.Develop(context.Background(), devRequest(writeRepo(t)), func(s tickets.AgentStep) { steps = append(steps, s.Summary) })
	joined := strings.Join(steps, "|")
	if strings.Count(joined, "(répété, non réexécuté)") != 1 || !strings.Contains(steps[3], "répété") {
		t.Errorf("steps = %v, want only the repeated failing read blocked", steps)
	}
}

// En développement, une réponse en texte ne termine pas : le modèle est
// relancé vers finish (vu en réel : tentative close sans modification).
func TestDevelop_TextReplyDoesNotFinish(t *testing.T) {
	model := &scriptedModel{replies: []Message{
		{Role: "assistant", Content: "J'ai déplacé le filtre de dates sous la barre de recherche, comme demandé."},
		call("f", "finish", `{"summary": "fait"}`),
	}}
	d := &Developer{Model: model, Checker: &fakeChecker{ok: true}}
	summary, err := d.Develop(context.Background(), devRequest(writeRepo(t)), nil)
	if err != nil || summary != "fait" || len(model.calls) != 2 {
		t.Errorf("summary = %q, err = %v, model calls = %d — want a nudge then finish", summary, err, len(model.calls))
	}
	last := model.calls[1][len(model.calls[1])-1]
	if !strings.Contains(last.Content, "finish") {
		t.Errorf("nudge = %q", last.Content)
	}
}

func jsonArgs(m map[string]string) (string, error) {
	b, err := json.Marshal(m)
	return string(b), err
}

// Vu en réel : « Modifié » alors que new reprenait old à l'identique
// (indentation mise à part) — le fichier n'avait pas changé, la tentative
// s'est close sur « aucun fichier modifié ». Une modification sans effet
// est une erreur explicite.
func TestEditFile_NoOpIsAnError(t *testing.T) {
	d, root := editRepo(t)
	before := readPage(t, root)
	for _, c := range [][2]string{
		{"\t\t<button type=\"submit\">Rechercher</button>", "\t\t<button type=\"submit\">Rechercher</button>"},
		// Même texte, même indentation (en espaces) : sans effet une fois
		// remise aux tabulations du fichier.
		{"        <button type=\"submit\">Rechercher</button>", "        <button type=\"submit\">Rechercher</button>"},
	} {
		if out := edit(d, c[0], c[1]); !strings.HasPrefix(out, "ERREUR") || !strings.Contains(out, "identique") {
			t.Errorf("edit(%q) = %q, want a no-op error", c[0], out)
		}
	}
	if readPage(t, root) != before {
		t.Error("file changed")
	}
}

// Vu en réel : read_file rendait 250 lignes, coupées ensuite à 3000
// caractères par la boucle — le modèle ne voyait que ~60 lignes d'un
// gabarit de 536, jamais la barre de recherche, et devinait des classes
// CSS. La lecture tient dans le budget et dit comment lire la suite.
func TestReadFile_FitsTheBudgetAndSaysHowToContinue(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 500; i++ {
		b.WriteString("\t\t<div class=\"ligne assez longue pour remplir la sortie de l'outil\">texte</div>\n")
	}
	os.WriteFile(filepath.Join(root, "big.templ"), []byte(b.String()), 0o644)
	out := Tools{Root: root}.ReadFile("big.templ", 0, 0)
	if len(out) > readOutputChars {
		t.Errorf("output = %d chars, budget %d", len(out), readOutputChars)
	}
	if !strings.Contains(out, "sur 500") || !strings.Contains(out, "start_line=") {
		t.Errorf("output end = %q, want the range shown and how to continue", out[max(0, len(out)-200):])
	}
	// Une plage demandée est respectée (dans le budget).
	if out := (Tools{Root: root}).ReadFile("big.templ", 300, 305); !strings.HasPrefix(out, "300\t") || strings.Count(out, "\n") < 6 {
		t.Errorf("range = %q", out)
	}
}

// Une recherche dans un fichier rappelle comment lire autour d'une ligne.
func TestSearch_InOneFileHintsReadRange(t *testing.T) {
	d, _ := editRepo(t)
	out := d.Tools.Search(`class="date-field"`, "cmd/app/templates/page.templ")
	if !strings.Contains(out, "page.templ:6:") || !strings.Contains(out, "start") {
		t.Errorf("search = %q", out)
	}
}

// Vu en réel : une réécriture complète coupée arrive comme un appel
// write_file aux arguments tronqués (pas comme une erreur du serveur) —
// le garde-fou « après une coupure, plus de write_file » ne se
// déclenchait pas. Même traitement.
func TestDevelop_TruncatedWriteFileArgsWithdrawTheTool(t *testing.T) {
	model := &scriptedModel{replies: []Message{
		call("w", "write_file", `{"path": "internal/store/store.go", "content": "package store\n// tronqu`),
		call("f", "finish", `{"summary": "ok"}`),
	}}
	d := &Developer{Model: model, Checker: &fakeChecker{ok: true}}
	d.Develop(context.Background(), devRequest(writeRepo(t)), nil)
	for _, s := range model.toolSpecs[1] {
		if s.Name == "write_file" {
			t.Fatal("write_file still offered after a truncated call")
		}
	}
	last := model.calls[1][len(model.calls[1])-1]
	if !strings.Contains(last.Content, "edit_file") {
		t.Errorf("last message = %q, want the model told to use edit_file", last.Content)
	}
}
