package triage

import "fmt"

// Thresholds paramètre le score de triage. Toutes les valeurs sont des
// constantes nommées et modifiables explicitement — pas d'heuristique
// cachée dans une condition.
type Thresholds struct {
	// MinCharsPerPage est le nombre minimal de caractères non-blancs
	// extraits sur une page pour qu'elle soit candidate à "usable". En
	// dessous, on considère qu'il s'agit d'un artefact résiduel (watermark,
	// numéro de page OCRisé par erreur, etc.), pas d'une vraie couche
	// texte.
	MinCharsPerPage int

	// MinPrintableRatio est la proportion minimale de caractères
	// imprimables (hors caractères de contrôle) parmi le texte extrait.
	MinPrintableRatio float64

	// MinLetterRatio est la proportion minimale de lettres parmi les
	// caractères imprimables. Un texte dominé par des symboles/ponctuation
	// (bruit d'extraction) est rejeté même s'il a beaucoup de caractères.
	MinLetterRatio float64

	// DocumentThreshold est la proportion minimale de pages "usable" pour
	// que le document entier soit considéré comme ayant une couche texte
	// exploitable (HasTextLayer = true).
	DocumentThreshold float64
}

// DefaultThresholds retourne les seuils par défaut du triage.
func DefaultThresholds() Thresholds {
	return Thresholds{
		MinCharsPerPage:   20,
		MinPrintableRatio: 0.85,
		MinLetterRatio:    0.5,
		DocumentThreshold: 0.8,
	}
}

// PageResult est le détail du score pour une page.
type PageResult struct {
	Page           int
	CharCount      int
	PrintableRatio float64
	LetterRatio    float64
	Usable         bool
}

// Result est le résultat du triage pour un document entier.
type Result struct {
	// HasTextLayer indique si le document a une couche texte exploitable
	// (Score >= Thresholds.DocumentThreshold).
	HasTextLayer bool
	// Score est la proportion de pages "usable" (dans [0, 1]).
	Score   float64
	Pages   []PageResult
	Reasons []string
}

// Score calcule le résultat du triage à partir du texte extrait par page.
// C'est une fonction pure : aucune I/O, testable sans PDF ni binaire
// externe.
func Score(pages []PageText, th Thresholds) Result {
	if len(pages) == 0 {
		return Result{
			HasTextLayer: false,
			Score:        0,
			Reasons:      []string{"document has no pages"},
		}
	}

	pageResults := make([]PageResult, len(pages))
	usableCount := 0
	for i, p := range pages {
		pr := scorePage(p, th)
		pageResults[i] = pr
		if pr.Usable {
			usableCount++
		}
	}

	score := float64(usableCount) / float64(len(pages))
	hasTextLayer := score >= th.DocumentThreshold

	reasons := []string{}
	if !hasTextLayer {
		reasons = append(reasons, fmt.Sprintf(
			"%d/%d pages usable (score %.2f) is below document threshold %.2f",
			usableCount, len(pages), score, th.DocumentThreshold,
		))
	}

	return Result{
		HasTextLayer: hasTextLayer,
		Score:        score,
		Pages:        pageResults,
		Reasons:      reasons,
	}
}

func scorePage(p PageText, th Thresholds) PageResult {
	charCount, printableRatio, letterRatio := textStats(p.Text)

	usable := charCount >= th.MinCharsPerPage &&
		printableRatio >= th.MinPrintableRatio &&
		letterRatio >= th.MinLetterRatio

	return PageResult{
		Page:           p.Page,
		CharCount:      charCount,
		PrintableRatio: printableRatio,
		LetterRatio:    letterRatio,
		Usable:         usable,
	}
}

// textStats calcule, sur le texte non-blanc d'une page :
//   - charCount : nombre de caractères non-blancs (rune)
//   - printableRatio : proportion de ces caractères qui sont imprimables
//     (hors caractères de contrôle)
//   - letterRatio : proportion des caractères imprimables qui sont des
//     lettres (unicode.IsLetter)
func textStats(text string) (charCount int, printableRatio, letterRatio float64) {
	var printable, letters int

	for _, r := range text {
		if isSpace(r) {
			continue
		}
		charCount++
		if isPrintable(r) {
			printable++
			if isLetter(r) {
				letters++
			}
		}
	}

	if charCount == 0 {
		return 0, 0, 0
	}

	printableRatio = float64(printable) / float64(charCount)

	if printable == 0 {
		letterRatio = 0
	} else {
		letterRatio = float64(letters) / float64(printable)
	}

	return charCount, printableRatio, letterRatio
}
