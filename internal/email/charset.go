package email

import (
	"strings"
	"unicode/utf8"
)

// Conversion des jeux de caractères vers UTF-8, par tables écrites ici
// plutôt que via golang.org/x/text : les e-mails réels n'utilisent en
// pratique que quelques charsets occidentaux, une table de 256 entrées par
// charset suffit et évite une dépendance.

// windows1252 : 0x00-0x7F et 0xA0-0xFF sont identiques à ISO-8859-1 ;
// seule la plage 0x80-0x9F diffère (€, guillemets typographiques,
// tirets...). Les 5 positions non définies (0x81, 0x8D, 0x8F, 0x90, 0x9D)
// sont mappées sur le point de code de même valeur, comme le fait WHATWG.
var windows1252 = func() [256]rune {
	var t [256]rune
	for i := range t {
		t[i] = rune(i)
	}
	high := [32]rune{
		0x20AC, 0x0081, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
		0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x008D, 0x017D, 0x008F,
		0x0090, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
		0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x009D, 0x017E, 0x0178,
	}
	copy(t[0x80:0xA0], high[:])
	return t
}()

// iso885915 (latin-9) : ISO-8859-1 avec 8 substitutions, dont € en 0xA4
// (là où latin-1 a ¤) — l'erreur classique qui transforme un prix en "12 ¤".
var iso885915 = func() [256]rune {
	var t [256]rune
	for i := range t {
		t[i] = rune(i)
	}
	t[0xA4] = '€'
	t[0xA6] = 'Š'
	t[0xA8] = 'š'
	t[0xB4] = 'Ž'
	t[0xB8] = 'ž'
	t[0xBC] = 'Œ'
	t[0xBD] = 'œ'
	t[0xBE] = 'Ÿ'
	return t
}()

// normalizeCharset ramène les alias courants à un nom canonique ; "" pour
// un charset absent ou inconnu.
func normalizeCharset(cs string) string {
	cs = strings.ToLower(strings.Trim(strings.TrimSpace(cs), `"'`))
	switch cs {
	case "utf-8", "utf8", "unicode-1-1-utf-8":
		return "utf-8"
	case "iso-8859-1", "iso8859-1", "iso_8859-1", "iso_8859-1:1987", "latin1", "latin-1", "l1", "cp819", "ibm819", "iso-ir-100":
		return "iso-8859-1"
	case "iso-8859-15", "iso8859-15", "iso_8859-15", "latin9", "latin-9", "l9":
		return "iso-8859-15"
	case "windows-1252", "cp1252", "x-cp1252", "win-1252":
		return "windows-1252"
	default:
		// us-ascii compris : un message étiqueté us-ascii qui contient des
		// octets hauts est mal étiqueté, la règle du charset inconnu est
		// la meilleure lecture possible.
		return ""
	}
}

// decodeCharset convertit b (déclaré dans charset) en UTF-8 valide.
func decodeCharset(charset string, b []byte) string {
	switch normalizeCharset(charset) {
	case "utf-8":
		// Octets invalides remplacés plutôt que réinterprétés : un texte
		// déclaré UTF-8 l'est presque entièrement, le relire en
		// windows-1252 abîmerait tous ses caractères valides.
		return strings.ToValidUTF8(string(b), "�")
	case "iso-8859-1", "windows-1252":
		// ISO-8859-1 est lu comme windows-1252 (choix WHATWG, suivi par
		// tous les navigateurs et clients mail) : les octets 0x80-0x9F
		// sont des codes de contrôle jamais utilisés dans un texte réel,
		// et en pratique ce sont des caractères windows-1252 mal étiquetés.
		return decodeSingleByte(b, &windows1252)
	case "iso-8859-15":
		return decodeSingleByte(b, &iso885915)
	default:
		// Charset inconnu : si c'est déjà de l'UTF-8 valide (le cas le plus
		// fréquent aujourd'hui), on le garde ; sinon windows-1252, le
		// charset 8 bits le plus répandu, qui décode tout octet sans échec.
		if utf8.Valid(b) {
			return string(b)
		}
		return decodeSingleByte(b, &windows1252)
	}
}

func decodeSingleByte(b []byte, table *[256]rune) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		sb.WriteRune(table[c])
	}
	return sb.String()
}
