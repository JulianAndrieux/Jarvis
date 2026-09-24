package agent

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// PackageMap décrit les paquets du dépôt root, un par ligne : chemin et
// rôle, lu dans le commentaire de paquet (première phrase). Vu en réel :
// sans elle, l'agent a cherché l'onglet Documents dans internal/store (le
// stockage sur disque de la CLI) au lieu de cmd/jarvisapp et
// internal/webapp. Bornée à maxChars : le contexte du modèle est petit.
func PackageMap(root string, maxChars int) string {
	dirs := map[string]*mapEntry{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && (strings.HasPrefix(d.Name(), ".") || skipDirs[d.Name()] || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		rel, _ := filepath.Rel(root, filepath.Dir(p))
		rel = filepath.ToSlash(rel)
		switch {
		case strings.HasSuffix(name, ".templ"):
			e := entry(dirs, rel)
			e.pages = append(e.pages, strings.TrimSuffix(name, ".templ"))
		case strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") && !strings.HasSuffix(name, "_templ.go"):
			e := entry(dirs, rel)
			if e.doc == "" {
				e.doc = packageDoc(p)
			}
		}
		return nil
	})

	paths := make([]string, 0, len(dirs))
	for p := range dirs {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	// Tous les paquets doivent figurer : on raccourcit les descriptions
	// jusqu'à tenir dans le budget, plutôt que de perdre les derniers
	// (vu sur le vrai dépôt : internal/webapp était coupé).
	for limit := mapLineChars; ; limit -= 10 {
		var b strings.Builder
		for _, p := range paths {
			e := dirs[p]
			desc := shorten(e.doc, limit)
			// Vu en réel : « gabarits des pages web » ne disait pas quelle
			// page est où — les pages sont nommées, jamais raccourcies.
			if len(e.pages) > 0 && limit > 0 {
				sort.Strings(e.pages)
				desc = "pages web (templ) : " + strings.Join(e.pages, ", ") + " — modifie le .templ de la page, les _templ.go sont générés"
			}
			b.WriteString(p)
			if desc != "" {
				b.WriteString(" — " + desc)
			}
			b.WriteString("\n")
		}
		out := strings.TrimRight(b.String(), "\n")
		if len(out) <= maxChars {
			return out
		}
		if limit <= 0 {
			// Même sans descriptions, trop de paquets : coupé à une fin de
			// ligne, sans dépasser le budget.
			out = out[:maxChars]
			if i := strings.LastIndex(out, "\n"); i > 0 {
				out = out[:i]
			}
			return out
		}
	}
}

// shorten borne s à limit octets (sans couper un caractère UTF-8) ;
// limit <= 0 : rien.
func shorten(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

type mapEntry struct {
	doc   string
	pages []string // gabarits .templ du dossier, sans extension
}

func entry(dirs map[string]*mapEntry, rel string) *mapEntry {
	if dirs[rel] == nil {
		dirs[rel] = &mapEntry{}
	}
	return dirs[rel]
}

// milestoneRe : « (jalon 12 : ...) », sans intérêt pour l'agent.
var milestoneRe = regexp.MustCompile(`\s*\(jalons? [^)]*\)`)

// mapLineChars borne le rôle d'un paquet sur la carte.
const mapLineChars = 90

// packageDoc : première phrase du commentaire de paquet du fichier path,
// sans « Package x » / « Command x » ni mention de jalon.
func packageDoc(path string) string {
	src, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil || f.Doc == nil {
		return ""
	}
	doc := strings.Join(strings.Fields(f.Doc.Text()), " ")
	doc = strings.TrimPrefix(doc, "Package "+f.Name.Name+" ")
	// Un programme est de paquet main, mais son commentaire dit
	// « Command <nom du binaire> ».
	if rest, ok := strings.CutPrefix(doc, "Command "); ok {
		if _, after, found := strings.Cut(rest, " "); found {
			doc = after
		}
	}
	doc = strings.TrimPrefix(doc, "est ")
	if i := strings.Index(doc, ". "); i >= 0 {
		doc = doc[:i]
	}
	doc = strings.TrimSuffix(doc, ".")
	doc = milestoneRe.ReplaceAllString(doc, "")
	// Phrase coupée au point de « (cf. x) » : parenthèse ouverte retirée.
	if strings.Count(doc, "(") > strings.Count(doc, ")") {
		doc = doc[:strings.LastIndex(doc, "(")]
	}
	return strings.TrimSpace(doc)
}

// writeCodeMap ajoute la carte du code au prompt système.
func writeCodeMap(b *strings.Builder, codeMap string) {
	if codeMap == "" {
		return
	}
	b.WriteString("\nCarte du code (chaque paquet et son rôle ; cherche d'abord dans les paquets concernés, avec l'argument dir) :\n")
	b.WriteString(codeMap)
	b.WriteString("\n")
}
