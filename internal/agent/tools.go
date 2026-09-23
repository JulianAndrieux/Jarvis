// Package agent est l'agent LLM local qui prend en charge les tickets
// (jalon 27 : analyse en lecture seule). L'agent n'a pas de terminal :
// seulement des outils écrits ici, confinés au dépôt — il ne peut lancer
// aucune commande arbitraire.
package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ToolCall est un appel d'outil demandé par le modèle.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON
}

// ToolSpec décrit un outil au modèle (format "function" de l'API
// compatible OpenAI).
type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Tools sont les outils en lecture seule de l'agent, sur le dépôt Root.
type Tools struct {
	Root string
	// MaxLines borne une lecture de fichier (0 : 250) ; MaxResults une
	// recherche (0 : 60). Le contexte du modèle local est petit (8192
	// jetons sur le serveur actuel).
	MaxLines   int
	MaxResults int
}

// skipDirs : jamais lus ni listés — historique git, copies de travail
// des agents, binaires, données, fichiers de test lourds.
var skipDirs = map[string]bool{".git": true, ".claude": true, "bin": true, "dist": true, "data": true, "node_modules": true}

// ReadOnlySpecs décrit les outils d'analyse au modèle.
func ReadOnlySpecs() []ToolSpec {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	integer := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	return []ToolSpec{
		{Name: "list_files", Description: "Liste le contenu d'un dossier du dépôt (\"\" = racine). Les dossiers finissent par /.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"dir": str("dossier relatif à la racine")}}},
		{Name: "read_file", Description: "Lit un fichier du dépôt, lignes numérotées. start_line/end_line facultatifs pour lire une partie.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"path": str("chemin relatif à la racine"), "start_line": integer("première ligne (1 = début)"), "end_line": integer("dernière ligne incluse")},
				"required": []string{"path"}}},
		{Name: "search", Description: "Cherche une expression régulière (syntaxe Go) dans les fichiers texte du dépôt. Retourne fichier:ligne: texte.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"pattern": str("expression régulière"), "dir": str("dossier où chercher (\"\" = tout le dépôt)")},
				"required": []string{"pattern"}}},
		{Name: "propose_plan", Description: "Termine l'analyse en proposant le plan de réalisation du ticket (Markdown).",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"plan": str("le plan complet")}, "required": []string{"plan"}}},
	}
}

// Execute exécute un appel d'outil et retourne sa sortie textuelle. Une
// erreur est rendue au modèle ("ERREUR : ...") pour qu'il corrige son
// appel, jamais propagée : un mauvais appel ne doit pas arrêter l'agent.
func (t Tools) Execute(call ToolCall) string {
	var args struct {
		Path      string `json:"path"`
		Dir       string `json:"dir"`
		Pattern   string `json:"pattern"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if strings.TrimSpace(call.Arguments) != "" {
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			return fmt.Sprintf("ERREUR : arguments invalides pour %s (JSON attendu) : %v", call.Name, err)
		}
	}
	switch call.Name {
	case "list_files":
		return t.ListFiles(args.Dir)
	case "read_file":
		return t.ReadFile(args.Path, args.StartLine, args.EndLine)
	case "search":
		return t.Search(args.Pattern, args.Dir)
	default:
		return fmt.Sprintf("ERREUR : outil inconnu %q", call.Name)
	}
}

// resolve transforme un chemin relatif en chemin absolu sous Root, ou
// retourne une erreur s'il en sort (.., chemin absolu, lien symbolique
// vers l'extérieur) ou touche un dossier exclu.
func (t Tools) resolve(rel string) (string, error) {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("chemin absolu refusé : %s", rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("chemin hors du dépôt : %s", rel)
	}
	for _, part := range strings.Split(filepath.ToSlash(clean), "/") {
		if skipDirs[part] {
			return "", fmt.Errorf("dossier non accessible : %s", part)
		}
	}
	root, err := filepath.EvalSymlinks(t.Root)
	if err != nil {
		return "", fmt.Errorf("racine du dépôt introuvable : %v", err)
	}
	abs := filepath.Join(root, clean)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", fmt.Errorf("chemin hors du dépôt : %s", rel)
	}
	return abs, nil
}

// ListFiles liste un dossier du dépôt.
func (t Tools) ListFiles(dir string) string {
	abs, err := t.resolve(dir)
	if err != nil {
		return "ERREUR : " + err.Error()
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "ERREUR : " + err.Error()
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || skipDirs[e.Name()] {
			continue
		}
		if e.IsDir() {
			names = append(names, e.Name()+"/")
		} else {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "(dossier vide)"
	}
	return strings.Join(names, "\n")
}

// ReadFile lit un fichier, lignes numérotées (start/end : 0 = début/fin).
func (t Tools) ReadFile(path string, start, end int) string {
	abs, err := t.resolve(path)
	if err != nil {
		return "ERREUR : " + err.Error()
	}
	info, err := os.Stat(abs)
	switch {
	case os.IsNotExist(err):
		return t.notFound(path)
	case err != nil:
		return "ERREUR : " + err.Error()
	case info.IsDir():
		return fmt.Sprintf("%s est un dossier, pas un fichier (utilise list_files). Contenu :\n%s", path, t.ListFiles(path))
	}
	f, err := os.Open(abs)
	if err != nil {
		return "ERREUR : " + err.Error()
	}
	defer f.Close()

	maxLines := t.MaxLines
	if maxLines <= 0 {
		maxLines = 250
	}
	if start <= 0 {
		start = 1
	}
	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	n, shown := 0, 0
	for sc.Scan() {
		n++
		if n < start || (end > 0 && n > end) {
			continue
		}
		if shown == maxLines {
			fmt.Fprintf(&b, "... (tronqué à %d lignes : relis avec start_line=%d)\n", maxLines, n)
			break
		}
		fmt.Fprintf(&b, "%d\t%s\n", n, sc.Text())
		shown++
	}
	if shown == 0 {
		return fmt.Sprintf("(aucune ligne dans l'intervalle ; le fichier en compte au moins %d)", n)
	}
	return b.String()
}

// notFound : erreur de fichier introuvable accompagnée des fichiers du
// dépôt au nom proche (même nom sans extension) — trouvé en réel : un
// modèle modeste qui reçoit une erreur brute redemande le même fichier.
func (t Tools) notFound(path string) string {
	base := filepath.Base(path)
	stem := strings.ToLower(strings.TrimSuffix(base, filepath.Ext(base)))
	root, _ := filepath.EvalSymlinks(t.Root)
	var near []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && (strings.HasPrefix(d.Name(), ".") || skipDirs[d.Name()]) {
				return filepath.SkipDir
			}
			return nil
		}
		name := strings.ToLower(d.Name())
		if stem != "" && strings.HasPrefix(name, stem) {
			rel, _ := filepath.Rel(root, p)
			near = append(near, filepath.ToSlash(rel))
			if len(near) >= 8 {
				return fs.SkipAll
			}
		}
		return nil
	})
	msg := "ERREUR : fichier introuvable : " + path
	if len(near) > 0 {
		msg += "\nFichiers au nom proche :\n" + strings.Join(near, "\n")
	} else {
		msg += "\n(aucun fichier au nom proche : utilise list_files ou search)"
	}
	return msg
}

// Search cherche pattern dans les fichiers texte sous dir.
func (t Tools) Search(pattern, dir string) string {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "ERREUR : expression régulière invalide : " + err.Error()
	}
	abs, err := t.resolve(dir)
	if err != nil {
		return "ERREUR : " + err.Error()
	}
	root, _ := filepath.EvalSymlinks(t.Root)
	maxResults := t.MaxResults
	if maxResults <= 0 {
		maxResults = 60
	}

	var results []string
	walkErr := filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != abs && (strings.HasPrefix(d.Name(), ".") || skipDirs[d.Name()] || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !searchable(d.Name()) {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		rel, _ := filepath.Rel(root, p)
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		line := 0
		for sc.Scan() {
			line++
			if re.MatchString(sc.Text()) {
				results = append(results, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), line, strings.TrimSpace(sc.Text())))
				if len(results) >= maxResults {
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		return "ERREUR : " + walkErr.Error()
	}
	if len(results) == 0 {
		return "(aucun résultat)"
	}
	out := strings.Join(results, "\n")
	if len(results) >= maxResults {
		out += fmt.Sprintf("\n... (limité à %d résultats : affine l'expression ou le dossier)", maxResults)
	}
	return out
}

// searchable : fichiers texte du projet (code, gabarits, docs, config).
func searchable(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go", ".templ", ".md", ".mod", ".sh", ".py", ".json", ".yaml", ".yml", ".txt", ".css", ".js", ".html", ".plist":
		return true
	}
	return name == "Makefile"
}
