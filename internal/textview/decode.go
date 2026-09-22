// Package textview décode des fichiers texte arbitraires et des CSV pour
// les afficher en aperçu dans l'application. Les fichiers viennent
// d'utilisateurs francophones : beaucoup d'exports Excel en Windows-1252,
// séparés par des points-virgules, d'où l'effort porté sur la détection
// d'encodage et de séparateur. Bibliothèque standard uniquement.
package textview

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Noms d'encodage retournés par Decode. Ce sont des chaînes (et non un type
// dédié) parce qu'elles sont destinées à être affichées telles quelles.
const (
	EncUTF8        = "utf-8"
	EncUTF8BOM     = "utf-8-bom"
	EncUTF16LE     = "utf-16le"
	EncUTF16BE     = "utf-16be"
	EncWindows1252 = "windows-1252"
)

// sniffLen est la taille de l'échantillon inspecté par IsProbablyText :
// même ordre de grandeur que l'heuristique de git (8000 octets), suffisant
// pour attraper les en-têtes binaires sans parcourir un fichier énorme.
const sniffLen = 8000

// cp1252High donne les caractères de la plage 0x80-0x9F de Windows-1252,
// la seule qui diffère de Latin-1. Les 5 positions non définies (0x81,
// 0x8D, 0x8F, 0x90, 0x9D) sont mappées sur le contrôle C1 de même valeur,
// comme le fait la norme WHATWG : on ne perd aucun octet et
// IsProbablyText les compte comme contrôles.
var cp1252High = [32]rune{
	'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8D, 'Ž', 0x8F,
	0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9D, 'ž', 'Ÿ',
}

// Decode convertit b en texte UTF-8 et retourne l'encodage détecté ("utf-8",
// "utf-8-bom", "utf-16le", "utf-16be", "windows-1252").
//
// Ordre : BOM UTF-8 (retiré) ; BOM UTF-16 LE/BE (décodé, BOM retiré, paires
// de substitution gérées) ; UTF-8 valide ; sinon windows-1252. On ne tente
// pas de deviner l'UTF-16 sans BOM : trop de faux positifs, et Excel/Windows
// en écrivent toujours un. Windows-1252 sert de repli universel car tout
// octet y a un sens : le décodage ne peut pas échouer.
//
// Les fins de ligne CRLF et CR seuls sont normalisées en LF, pour que
// l'aperçu et le lecteur CSV n'aient qu'une convention à gérer.
func Decode(b []byte) (text string, encoding string) {
	switch {
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		// Le reste peut être de l'UTF-8 invalide (fichier corrompu) :
		// strings.ToValidUTF8 garantit une chaîne UTF-8 valide en sortie.
		text, encoding = strings.ToValidUTF8(string(b[3:]), "�"), EncUTF8BOM
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		text, encoding = decodeUTF16(b[2:], false), EncUTF16LE
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		text, encoding = decodeUTF16(b[2:], true), EncUTF16BE
	case utf8.Valid(b):
		text, encoding = string(b), EncUTF8
	default:
		text, encoding = decodeWindows1252(b), EncWindows1252
	}
	return normalizeNewlines(text), encoding
}

// decodeUTF16 décode des unités 16 bits. unicode/utf16.Decode remplace les
// demi-paires orphelines par U+FFFD ; un octet final isolé (taille impaire,
// fichier tronqué) est lui aussi signalé par U+FFFD plutôt qu'ignoré.
func decodeUTF16(b []byte, bigEndian bool) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if bigEndian {
			units = append(units, uint16(b[i])<<8|uint16(b[i+1]))
		} else {
			units = append(units, uint16(b[i+1])<<8|uint16(b[i]))
		}
	}
	s := string(utf16.Decode(units))
	if len(b)%2 == 1 {
		s += "�"
	}
	return s
}

func decodeWindows1252(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b) + len(b)/4)
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c < 0xA0:
			sb.WriteRune(cp1252High[c-0x80])
		default:
			// 0xA0-0xFF : identique à Latin-1, donc au point de code.
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}

func normalizeNewlines(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// IsProbablyText indique si b ressemble à du texte (et non à un binaire) :
// vrai pour un BOM UTF-16, faux si des octets NUL apparaissent dans les 8000
// premiers octets (hors UTF-16 à BOM), faux si plus de 10 % des caractères
// sont des contrôles (hors \t \n \r \f) après décodage, vrai sinon. Un
// fichier vide est du texte.
//
// Seul l'échantillon de tête est examiné : c'est là que se trouvent les
// signatures binaires (PDF, PNG, ZIP…), et cela borne le coût sur un gros
// fichier.
func IsProbablyText(b []byte) bool {
	if len(b) >= 2 && ((b[0] == 0xFF && b[1] == 0xFE) || (b[0] == 0xFE && b[1] == 0xFF)) {
		return true
	}
	sample := b
	if len(sample) > sniffLen {
		sample = trimIncompleteUTF8(sample[:sniffLen])
	}
	for _, c := range sample {
		if c == 0 {
			return false
		}
	}
	text, _ := Decode(sample)
	total, controls := 0, 0
	for _, r := range text {
		total++
		if isControl(r) {
			controls++
		}
	}
	if total == 0 {
		return true
	}
	// Comparaison en entiers pour éviter tout arrondi flottant sur le seuil.
	return controls*10 <= total
}

// isControl reconnaît les contrôles C0, DEL et C1, hors blancs usuels.
// Decode a déjà transformé \r en \n, mais on l'exclut quand même pour ne
// pas dépendre de ce détail.
func isControl(r rune) bool {
	switch r {
	case '\t', '\n', '\r', '\f':
		return false
	}
	return r < 0x20 || (r >= 0x7F && r < 0xA0)
}

// trimIncompleteUTF8 retire une éventuelle séquence UTF-8 coupée en fin
// d'échantillon : sans cela, un fichier UTF-8 valide tronqué à 8000 octets
// au milieu d'un "é" serait vu comme invalide et décodé en Windows-1252.
func trimIncompleteUTF8(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			return b
		}
	}
	return b
}

// Truncate coupe s à au plus maxBytes octets sans couper un caractère UTF-8
// en deux, et indique si une coupe a eu lieu (pour afficher « fichier
// tronqué » dans l'aperçu d'un très gros fichier texte). Un maxBytes
// négatif est traité comme 0.
func Truncate(s string, maxBytes int) (string, bool) {
	if maxBytes < 0 {
		maxBytes = 0
	}
	if len(s) <= maxBytes {
		return s, false
	}
	cut := maxBytes
	// On recule jusqu'au début d'un caractère ; au plus UTFMax-1 pas sur
	// de l'UTF-8 valide. La borne évite de tout effacer sur une chaîne
	// invalide faite d'octets de continuation.
	for cut > 0 && cut > maxBytes-utf8.UTFMax && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}
