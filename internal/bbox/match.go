package bbox

import "strings"

// FindSnippetBBox cherche snippet parmi words et retourne son rectangle
// englobant. Deux niveaux de correspondance, du plus fiable au plus
// tolérant :
//
//  1. Correspondance exacte d'une séquence contiguë de mots (après
//     normalisation : minuscules, ponctuation de bord retirée). C'est le
//     cas attendu : le prompt d'extraction demande un extrait recopié tel
//     quel depuis le texte source.
//  2. À défaut, repli sur l'enveloppe des occurrences individuelles de
//     chaque token du snippet, trouvées dans l'ordre. Moins précis (les
//     mots peuvent ne pas être contigus sur la page), mais un bbox
//     approximatif reste préférable à une absence de bbox.
//
// Retourne ok=false si aucun token du snippet n'a pu être localisé.
func FindSnippetBBox(words []Word, snippet string) (Box, bool) {
	tokens := tokenize(snippet)
	if len(tokens) == 0 || len(words) == 0 {
		return Box{}, false
	}

	wordTokens := make([]string, len(words))
	for i, w := range words {
		wordTokens[i] = normalize(w.Text)
	}

	if b, ok := findContiguous(words, wordTokens, tokens); ok {
		return b, true
	}
	return findEnvelope(words, wordTokens, tokens)
}

func findContiguous(words []Word, wordTokens, tokens []string) (Box, bool) {
	n := len(tokens)
	for start := 0; start+n <= len(wordTokens); start++ {
		match := true
		for i := 0; i < n; i++ {
			if wordTokens[start+i] != tokens[i] {
				match = false
				break
			}
		}
		if match {
			return unionBox(words[start : start+n]), true
		}
	}
	return Box{}, false
}

func findEnvelope(words []Word, wordTokens, tokens []string) (Box, bool) {
	var matched []Word
	searchFrom := 0
	for _, tok := range tokens {
		for i := searchFrom; i < len(wordTokens); i++ {
			if wordTokens[i] == tok {
				matched = append(matched, words[i])
				searchFrom = i + 1
				break
			}
		}
	}
	if len(matched) == 0 {
		return Box{}, false
	}
	return unionBox(matched), true
}

func unionBox(words []Word) Box {
	b := Box{XMin: words[0].XMin, YMin: words[0].YMin, XMax: words[0].XMax, YMax: words[0].YMax}
	for _, w := range words[1:] {
		b.XMin = min(b.XMin, w.XMin)
		b.YMin = min(b.YMin, w.YMin)
		b.XMax = max(b.XMax, w.XMax)
		b.YMax = max(b.YMax, w.YMax)
	}
	return b
}

func tokenize(s string) []string {
	fields := strings.Fields(s)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if n := normalize(f); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func normalize(s string) string {
	return strings.ToLower(strings.Trim(s, ".,;:!?()[]{}\"'"))
}
