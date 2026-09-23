package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	switch n := strings.Count(content, old); {
	case n == 0:
		return "ERREUR : extrait introuvable dans " + path + " (il doit correspondre exactement, indentation comprise)." + nearbyLines(content, old)
	case n > 1:
		return fmt.Sprintf("ERREUR : extrait présent %d fois dans %s — ajoute du contexte pour qu'il soit unique", n, path)
	}
	if err := os.WriteFile(abs, []byte(strings.Replace(content, old, new, 1)), 0o644); err != nil {
		return "ERREUR : " + err.Error()
	}
	return "Modifié : " + path
}

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
	for i, l := range lines {
		if strings.Contains(strings.TrimSpace(l), first) {
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
