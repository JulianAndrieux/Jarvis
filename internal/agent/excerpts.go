package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// planExcerpts : les lignes réelles des fichiers cités par le plan qui
// contiennent le plus de mots du ticket, avec quelques lignes autour,
// numérotées comme read_file, dans la limite de maxChars. Vu en réel :
// avec un plan juste, Qwen3-8B partait lire des zones sans rapport et ne
// modifiait rien — Jarvis lui montre d'emblée où regarder. Simple
// recherche de mots, pas d'index.
func planExcerpts(root string, req tickets.DevRequest, maxChars int) string {
	keywords := ticketKeywords(req.Title + " " + req.Need + " " + req.Acceptance)
	if len(keywords) == 0 {
		return ""
	}
	var b strings.Builder
	for _, path := range planFiles(root, req.Plan) {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		for _, w := range bestWindows(lines, keywords) {
			var block strings.Builder
			fmt.Fprintf(&block, "%s, lignes %d-%d :\n", path, w[0]+1, w[1]+1)
			for i := w[0]; i <= w[1]; i++ {
				fmt.Fprintf(&block, "%d\t%s\n", i+1, lines[i])
			}
			if b.Len()+block.Len() > maxChars {
				return b.String()
			}
			b.WriteString(block.String())
		}
	}
	return b.String()
}

// planFiles : les fichiers cités par le plan qui existent, hors fichiers
// générés (on ne les modifie pas) et tests.
func planFiles(root, plan string) []string {
	var out []string
	for _, m := range planPathRe.FindAllStringSubmatch(plan, -1) {
		p := m[1]
		if strings.HasSuffix(p, "_templ.go") || strings.HasSuffix(p, "_test.go") || strings.Contains(p, "..") {
			continue
		}
		if info, err := os.Stat(filepath.Join(root, p)); err == nil && !info.IsDir() && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// excerptContext : lignes montrées de part et d'autre d'une ligne trouvée ;
// excerptWindows : fenêtres au plus par fichier.
const (
	excerptContext = 5
	excerptWindows = 3
)

// bestWindows : les fenêtres autour des lignes qui contiennent le plus de
// mots-clés (au moins deux), fusionnées si elles se touchent, dans
// l'ordre du fichier.
func bestWindows(lines []string, keywords []string) [][2]int {
	type scored struct{ line, score int }
	var hits []scored
	for i, l := range lines {
		norm := normalize(l)
		n := 0
		for _, k := range keywords {
			if strings.Contains(norm, k) {
				n++
			}
		}
		if n >= 2 {
			hits = append(hits, scored{i, n})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	var windows [][2]int
	for _, h := range hits {
		lo, hi := max(0, h.line-excerptContext), min(len(lines)-1, h.line+excerptContext)
		merged := false
		for i, w := range windows {
			if lo <= w[1]+1 && hi >= w[0]-1 {
				windows[i] = [2]int{min(lo, w[0]), max(hi, w[1])}
				merged = true
				break
			}
		}
		if !merged {
			if len(windows) == excerptWindows {
				continue
			}
			windows = append(windows, [2]int{lo, hi})
		}
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i][0] < windows[j][0] })
	return windows
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}]+`)

// stopWords : mots trop courants pour situer du code.
var stopWords = map[string]bool{
	"dans": true, "pour": true, "avec": true, "cette": true, "sont": true, "plus": true, "comme": true,
	"aimerais": true, "voudrais": true, "avoir": true, "faire": true, "etre": true, "tout": true, "tous": true,
	"quand": true, "mais": true, "sans": true, "sous": true, "votre": true, "notre": true, "leur": true,
	"cote": true, "dessous": true, "dessus": true, "actuellement": true, "deplacer": true, "ajouter": true,
}

// ticketKeywords : les mots du ticket (4 lettres ou plus, sans accents,
// au singulier approximatif), sans les mots trop courants.
func ticketKeywords(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range wordRe.FindAllString(normalize(text), -1) {
		if stopWords[w] {
			continue
		}
		w = strings.TrimSuffix(w, "s")
		if len([]rune(w)) < 4 || stopWords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// normalize : minuscules, sans accents (ceux du français : pas de
// dépendance pour si peu).
func normalize(s string) string { return accents.Replace(strings.ToLower(s)) }

var accents = strings.NewReplacer(
	"à", "a", "â", "a", "ä", "a", "ç", "c", "é", "e", "è", "e", "ê", "e", "ë", "e",
	"î", "i", "ï", "i", "ô", "o", "ö", "o", "ù", "u", "û", "u", "ü", "u", "ÿ", "y", "œ", "oe", "æ", "ae",
)
