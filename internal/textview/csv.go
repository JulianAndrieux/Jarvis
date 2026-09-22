package textview

import (
	"encoding/csv"
	"errors"
	"io"
	"strings"
)

// ErrNotText est retournée par ParseCSV quand le contenu ressemble à un
// binaire : mieux vaut un message clair qu'un tableau de caractères
// illisibles dans l'aperçu.
var ErrNotText = errors.New("textview: le contenu n'est pas du texte")

// Table est le résultat de ParseCSV, prêt à être rendu en tableau HTML.
type Table struct {
	Header    []string   // première ligne
	Rows      [][]string // lignes suivantes, au plus maxRows ; chaque ligne complétée par des "" jusqu'à la largeur maximale observée
	Delimiter rune       // séparateur détecté
	Truncated bool       // vrai si des lignes ont été omises à cause de maxRows
	TotalRows int        // nombre total de lignes de données (hors en-tête), même au-delà de maxRows
}

// candidateDelimiters est aussi l'ordre de départage final : à égalité
// parfaite, la virgule (norme RFC 4180) l'emporte.
var candidateDelimiters = []rune{',', ';', '\t', '|'}

// bomRune est U+FEFF construit par conversion : écrit littéralement dans
// une chaîne, le caractère serait refusé par le compilateur Go (BOM au
// milieu du fichier source).
const bomRune = string(rune(0xFEFF))

// sniffRecords est le nombre d'enregistrements non vides examinés pour
// détecter le séparateur : assez pour voir la structure, assez peu pour
// rester instantané sur un fichier de plusieurs Mo.
const sniffRecords = 20

// ParseCSV décode b (via Decode) puis le lit comme un CSV.
//
// Le séparateur est choisi parmi ',', ';', '\t', '|' : celui qui donne le
// nombre de colonnes le plus constant (et > 1) sur les 20 premiers
// enregistrements non vides, guillemets respectés ; départage par
// fréquence du séparateur. C'est ce critère de constance, et non la simple
// fréquence, qui évite de prendre les décimales françaises ("12,50") pour
// des séparateurs dans un export Excel à points-virgules.
//
// La lecture est volontairement tolérante (guillemets mal formés, lignes de
// largeurs différentes) : c'est un aperçu, pas une validation. maxRows <= 0
// signifie pas de limite. Erreur seulement si le contenu n'est pas du texte ;
// un CSV d'une seule colonne est valide (Delimiter ',' par défaut).
//
// L'en-tête est lui aussi complété à la largeur maximale, pour que le
// rendu n'ait jamais à gérer des lignes de tailles différentes.
func ParseCSV(b []byte, maxRows int) (Table, error) {
	if !IsProbablyText(b) {
		return Table{}, ErrNotText
	}
	text, _ := Decode(b)
	// Decode a retiré le BOM ; on retire aussi un éventuel second BOM
	// (fichiers réenregistrés plusieurs fois) et les blancs de tête. Les
	// tabulations ne sont pas retirées : dans un TSV, une tabulation
	// initiale signifie un premier champ vide.
	text = strings.TrimLeft(text, bomRune+" \n")

	delim := detectDelimiter(text)
	t := Table{Delimiter: delim}
	width := 0
	first := true
	readRecords(text, delim, func(rec []string) bool {
		if len(rec) > width {
			width = len(rec)
		}
		if first {
			t.Header = rec
			first = false
			return true
		}
		t.TotalRows++
		if maxRows > 0 && len(t.Rows) >= maxRows {
			// On continue la lecture pour compter les lignes : un simple
			// comptage de \n serait faux avec des champs multilignes.
			t.Truncated = true
			return true
		}
		t.Rows = append(t.Rows, rec)
		return true
	})
	// La largeur ne prend en compte que les lignes lues : une ligne plus
	// large au-delà de maxRows compte aussi, puisqu'elle a été observée.
	t.Header = pad(t.Header, width)
	for i := range t.Rows {
		t.Rows[i] = pad(t.Rows[i], width)
	}
	return t, nil
}

// detectDelimiter applique l'heuristique décrite dans ParseCSV.
func detectDelimiter(text string) rune {
	best := ','
	bestScore, bestCount := 0.0, -1
	for _, d := range candidateDelimiters {
		var counts []int
		readRecords(text, d, func(rec []string) bool {
			counts = append(counts, len(rec))
			return len(counts) < sniffRecords
		})
		mode, freq := modeOf(counts)
		if mode <= 1 {
			continue
		}
		// Constance : part des enregistrements qui ont le nombre de
		// colonnes majoritaire.
		score := float64(freq) / float64(len(counts))
		occurrences := countOccurrences(text, d, len(counts))
		if score > bestScore || (score == bestScore && occurrences > bestCount) {
			best, bestScore, bestCount = d, score, occurrences
		}
	}
	return best
}

// readRecords lit text avec encoding/csv en mode tolérant et appelle fn
// pour chaque enregistrement, jusqu'à ce que fn retourne faux. Les erreurs
// de syntaxe (*csv.ParseError) ne sont pas fatales : le lecteur a déjà
// consommé la ligne fautive, on passe à la suivante.
func readRecords(text string, delim rune, fn func([]string) bool) {
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = delim
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	// Les enregistrements sont conservés dans la Table : pas de réutilisation.
	r.ReuseRecord = false
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return
		}
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				continue
			}
			return
		}
		if isBlankRecord(rec) {
			continue
		}
		if !fn(rec) {
			return
		}
	}
}

// isBlankRecord repère une ligne faite uniquement d'espaces : encoding/csv
// n'ignore que les lignes strictement vides.
func isBlankRecord(rec []string) bool {
	return len(rec) == 1 && strings.TrimSpace(rec[0]) == ""
}

// modeOf retourne la valeur la plus fréquente de counts (la plus grande en
// cas d'égalité) et sa fréquence.
func modeOf(counts []int) (mode, freq int) {
	seen := map[int]int{}
	for _, c := range counts {
		seen[c]++
	}
	for v, f := range seen {
		if f > freq || (f == freq && v > mode) {
			mode, freq = v, f
		}
	}
	return mode, freq
}

// countOccurrences compte les occurrences de d hors guillemets dans les
// maxLines premières lignes logiques non vides de text : même échantillon
// que celui qui a servi à calculer la constance.
func countOccurrences(text string, d rune, maxLines int) int {
	n, lines := 0, 0
	inQuotes, lineHasContent := false, false
	for _, r := range text {
		switch {
		case r == '"':
			inQuotes = !inQuotes
			lineHasContent = true
		case r == '\n' && !inQuotes:
			if lineHasContent {
				lines++
				if lines >= maxLines {
					return n
				}
			}
			lineHasContent = false
		case r == d && !inQuotes:
			n++
			lineHasContent = true
		default:
			lineHasContent = true
		}
	}
	return n
}

func pad(rec []string, width int) []string {
	for len(rec) < width {
		rec = append(rec, "")
	}
	return rec
}
