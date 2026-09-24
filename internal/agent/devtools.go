package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Checker lance vérifications et tests dans la copie de travail —
// workspace.Checker en production (environnement vidé, délai borné).
type Checker interface {
	Checks(ctx context.Context, dir string) (string, bool)
	Tests(ctx context.Context, dir, pkg string) (string, bool)
}

// DevTools sont les outils du développement (jalon 28) : ceux de
// l'analyse, plus l'écriture et l'exécution des vérifications, tous
// confinés à la copie de travail du ticket (Tools.Root).
type DevTools struct {
	Tools
	Checker Checker
	// MaxFileBytes borne un fichier écrit (0 : 200 000 octets).
	MaxFileBytes int
}

// rewriteMaxLines : au-delà, un fichier existant ne se réécrit pas en
// entier (write_file), il se modifie (edit_file).
const rewriteMaxLines = 60

// DevSpecs décrit les outils du développement au modèle.
func DevSpecs() []ToolSpec {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	specs := ReadOnlySpecs()
	specs = specs[:len(specs)-1] // sans propose_plan
	return append(specs,
		ToolSpec{Name: "write_file", Description: "Crée ou remplace entièrement un fichier (dossiers créés au besoin). Pour modifier une partie d'un fichier existant, préfère edit_file.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": str("chemin relatif"), "content": str("contenu complet")}, "required": []string{"path", "content"}}},
		ToolSpec{Name: "edit_file", Description: "Remplace dans un fichier l'extrait exact old (qui doit y apparaître une seule fois, espaces et tabulations compris) par new. Lis le fichier avant.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": str("chemin relatif"), "old": str("extrait exact à remplacer"), "new": str("nouveau texte")}, "required": []string{"path", "old", "new"}}},
		ToolSpec{Name: "run_checks", Description: "Génère les gabarits templ puis lance gofmt, go vet et go build sur tout le dépôt.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
		ToolSpec{Name: "run_tests", Description: "Lance go test sur un paquet (ex. ./internal/webapp/) ou ./... pour tout le dépôt.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"package": str("motif de paquet, relatif (./...)")}}},
		ToolSpec{Name: "finish", Description: "Termine le développement quand les tests et les vérifications passent, avec un résumé des changements.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"summary": str("résumé des changements")}, "required": []string{"summary"}}},
	)
}

// Execute exécute un outil de développement. Comme pour Tools, une erreur
// est rendue au modèle ("ERREUR : ..."), jamais propagée.
func (d DevTools) Execute(ctx context.Context, call ToolCall) string {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Old     string `json:"old"`
		New     string `json:"new"`
		Package string `json:"package"`
	}
	switch call.Name {
	case "write_file", "edit_file", "run_checks", "run_tests":
		if strings.TrimSpace(call.Arguments) != "" {
			if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
				return fmt.Sprintf("ERREUR : arguments invalides pour %s (JSON attendu) : %v", call.Name, err)
			}
		}
	default:
		return d.Tools.Execute(call)
	}

	switch call.Name {
	case "write_file":
		return d.writeFile(args.Path, args.Content)
	case "edit_file":
		return d.editFile(args.Path, args.Old, args.New)
	case "run_checks":
		out, ok := d.Checker.Checks(ctx, d.Root)
		return verdict(out, ok)
	default: // run_tests
		pkg := strings.TrimSpace(args.Package)
		if pkg == "" {
			pkg = "./..."
		}
		// Vu en réel : « cmd/jarvisapp » refusé, une étape perdue. Un
		// chemin relatif sans « ./ » est complété (la validation suit).
		if !strings.HasPrefix(pkg, "./") && !strings.HasPrefix(pkg, "/") && !strings.HasPrefix(pkg, ".") {
			pkg = "./" + pkg
		}
		if !validPackagePattern(pkg) {
			return "ERREUR : motif de paquet refusé (relatif au dépôt, ex. ./internal/webapp/ ou ./...) : " + pkg
		}
		out, ok := d.Checker.Tests(ctx, d.Root, pkg)
		return verdict(out, ok)
	}
}

// validPackagePattern : motif relatif au dépôt ("./x/y", "./x/...",
// "./...") — jamais de "..", sauf le suffixe récursif "...".
func validPackagePattern(pkg string) bool {
	if !strings.HasPrefix(pkg, "./") {
		return false
	}
	return !strings.Contains(strings.TrimSuffix(pkg, "..."), "..")
}

func verdict(out string, ok bool) string {
	if ok {
		return "SUCCÈS\n" + out
	}
	return "ÉCHEC\n" + out
}

// writable résout un chemin d'écriture : mêmes règles que la lecture,
// plus les fichiers que l'agent ne doit jamais toucher.
func (d DevTools) writable(path string) (string, error) {
	abs, err := d.resolve(path)
	if err != nil {
		return "", err
	}
	base := filepath.Base(path)
	switch {
	case base == "go.mod" || base == "go.sum":
		return "", fmt.Errorf("%s est interdit : pas de nouvelle dépendance (contrainte du projet)", base)
	case strings.HasSuffix(base, "_templ.go"):
		return "", fmt.Errorf("%s est généré par templ : modifie le .templ, run_checks régénère", base)
	case base == "CLAUDE.md":
		return "", fmt.Errorf("CLAUDE.md est tenu par les humains")
	}
	return abs, nil
}

func (d DevTools) writeFile(path, content string) string {
	abs, err := d.writable(path)
	if err != nil {
		return "ERREUR : " + err.Error()
	}
	max := d.MaxFileBytes
	if max <= 0 {
		max = 200_000
	}
	if len(content) > max {
		return fmt.Sprintf("ERREUR : contenu trop long (%d octets, maximum %d)", len(content), max)
	}
	// Vu en réel : un petit modèle réécrit un fichier entier pour changer
	// une ligne — réponse coupée par le contexte, JSON invalide, travail
	// perdu. Au-delà de rewriteMaxLines, seul edit_file est permis.
	if existing, err := os.ReadFile(abs); err == nil {
		if n := strings.Count(string(existing), "\n"); n > rewriteMaxLines {
			return fmt.Sprintf("ERREUR : %s existe déjà (%d lignes) : ne le réécris pas en entier, utilise edit_file pour ne changer que la partie concernée", path, n)
		}
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "ERREUR : " + err.Error()
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return "ERREUR : " + err.Error()
	}
	return fmt.Sprintf("Écrit : %s (%d lignes)", path, strings.Count(content, "\n")+1)
}

func (d DevTools) editFile(path, old, new string) string {
	abs, err := d.writable(path)
	if err != nil {
		return "ERREUR : " + err.Error()
	}
	data, err := os.ReadFile(abs)
	if os.IsNotExist(err) {
		return d.notFound(path)
	}
	if err != nil {
		return "ERREUR : " + err.Error()
	}
	if old == "" {
		return "ERREUR : old est vide — utilise write_file pour créer un fichier"
	}
	content := string(data)
	original := content
	note := ""
	switch n := strings.Count(content, old); {
	case n > 1:
		return fmt.Sprintf("ERREUR : extrait présent %d fois dans %s — ajoute du contexte pour qu'il soit unique", n, path)
	case n == 1:
		content = strings.Replace(content, old, new, 1)
	default:
		// Vu en réel : l'extrait recopié avec des espaces au lieu des
		// tabulations (ou avec les numéros de ligne de read_file) n'était
		// jamais trouvé. On le cherche en ignorant l'indentation — toujours
		// à un seul endroit — et le nouveau texte prend celle du fichier.
		replaced, count := looseReplace(content, old, new)
		switch {
		case count > 1:
			return fmt.Sprintf("ERREUR : extrait présent %d fois dans %s (indentation ignorée) — ajoute du contexte pour qu'il soit unique", count, path)
		case count == 0:
			return "ERREUR : extrait introuvable dans " + path + " (il doit reprendre des lignes réelles du fichier)." + nearbyLines(content, old)
		}
		content = replaced
		note = " (extrait retrouvé en ignorant l'indentation, remise à celle du fichier : relis la zone avec read_file si besoin)"
	}
	// Vu en réel : new reprenait old (indentation mise à part) — « Modifié »
	// alors que rien n'avait changé, puis « aucun fichier modifié ».
	if content == original {
		return "ERREUR : le nouveau texte est identique à l'ancien (indentation mise à part) : rien n'a changé dans " + path + ". Mets dans new la version modifiée de ces lignes."
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return "ERREUR : " + err.Error()
	}
	return "Modifié : " + path + note
}

// lineNumberPrefix : le numéro de ligne que read_file affiche devant
// chaque ligne (« 12<tab> »), parfois recopié par le modèle.
var lineNumberPrefix = regexp.MustCompile(`^\d+\t`)

// splitSnippet découpe un extrait en lignes, sans lignes vides en bord et
// sans numéros de ligne recopiés (s'ils sont sur toutes les lignes).
func splitSnippet(s string) []string {
	lines := strings.Split(s, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	numbered := len(lines) > 0
	for _, l := range lines {
		if strings.TrimSpace(l) != "" && !lineNumberPrefix.MatchString(l) {
			numbered = false
		}
	}
	if numbered {
		for i, l := range lines {
			lines[i] = lineNumberPrefix.ReplaceAllString(l, "")
		}
	}
	return lines
}

func leading(l string) string { return l[:len(l)-len(strings.TrimLeft(l, " \t"))] }

// looseReplace cherche old dans content ligne à ligne, espaces de bord
// ignorés. Une seule occurrence : remplacée par new, réindenté comme le
// fichier (même indentation pour les mêmes lignes, espaces convertis en
// tabulations au besoin). count : nombre d'occurrences trouvées.
func looseReplace(content, old, new string) (string, int) {
	oldLines := splitSnippet(old)
	if len(oldLines) == 0 {
		return "", 0
	}
	fileLines := strings.Split(content, "\n")
	at, count := -1, 0
	for i := 0; i+len(oldLines) <= len(fileLines); i++ {
		match := true
		for k, ol := range oldLines {
			if strings.TrimSpace(fileLines[i+k]) != strings.TrimSpace(ol) {
				match = false
				break
			}
		}
		if match {
			at, count = i, count+1
		}
	}
	if count != 1 {
		return "", count
	}

	// Indentation : celle du modèle -> celle du fichier.
	indent := map[string]string{}
	unit, fileTabs := 0, false
	for k, ol := range oldLines {
		if strings.TrimSpace(ol) == "" {
			continue
		}
		ml, fl := leading(ol), leading(fileLines[at+k])
		indent[ml] = fl
		fileTabs = fileTabs || strings.Contains(fl, "\t")
		if unit == 0 && ml != "" && strings.Trim(ml, " ") == "" && fl != "" && strings.Trim(fl, "\t") == "" {
			unit = len(ml) / len(fl)
		}
	}
	if unit <= 0 {
		unit = 4
	}
	newLines := splitSnippet(new)
	for i, nl := range newLines {
		ml := leading(nl)
		switch fl, ok := indent[ml]; {
		case ok:
			newLines[i] = fl + nl[len(ml):]
		case fileTabs && ml != "" && strings.Trim(ml, " ") == "":
			newLines[i] = strings.Repeat("\t", len(ml)/unit) + strings.Repeat(" ", len(ml)%unit) + nl[len(ml):]
		}
	}
	out := append(append(append([]string{}, fileLines[:at]...), newLines...), fileLines[at+len(oldLines):]...)
	return strings.Join(out, "\n"), 1
}

// keywordScores : les lignes qui partagent le plus de mots-clés (4
// caractères ou plus) avec l'extrait, au moins deux.
func keywordScores(lines []string, old string) map[int]bool {
	words := map[string]bool{}
	for _, w := range keywordRe.FindAllString(old, -1) {
		if len(w) >= 4 {
			words[w] = true
		}
	}
	scores := make([]int, len(lines))
	max := 0
	for i, l := range lines {
		for w := range words {
			if strings.Contains(l, w) {
				scores[i]++
			}
		}
		if scores[i] > max {
			max = scores[i]
		}
	}
	out := map[int]bool{}
	if max < 2 {
		return out
	}
	for i, sc := range scores {
		if sc == max {
			out[i] = true
		}
	}
	return out
}

var keywordRe = regexp.MustCompile(`[\p{L}\p{N}_-]+`)

// nearbyLines montre les lignes du fichier qui ressemblent à la première
// ligne non vide de l'extrait (comparées sans les espaces de bord), avec
// leur numéro et leur indentation réelle.
func nearbyLines(content, old string) string {
	var first string
	for _, l := range strings.Split(old, "\n") {
		if strings.TrimSpace(l) != "" {
			first = strings.TrimSpace(l)
			break
		}
	}
	if first == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	var b strings.Builder
	found := 0
	matches := func(i int, l string) bool { return strings.Contains(strings.TrimSpace(l), first) }
	// Vu en réel : un extrait reformulé (première ligne inexistante telle
	// quelle) ne donnait aucune piste. Repli : les lignes qui partagent le
	// plus de mots-clés avec l'extrait.
	if !strings.Contains(content, first) {
		best := keywordScores(lines, old)
		matches = func(i int, l string) bool { return best[i] }
	}
	for i, l := range lines {
		if matches(i, l) {
			if found == 0 {
				b.WriteString("\nLignes réelles les plus proches (numéro, tabulation, texte) :\n")
			}
			for j := i; j < len(lines) && j < i+4; j++ {
				fmt.Fprintf(&b, "%d\t%s\n", j+1, lines[j])
			}
			found++
			if found == 3 {
				break
			}
		}
	}
	return b.String()
}
