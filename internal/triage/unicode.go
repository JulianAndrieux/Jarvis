package triage

import "unicode"

// Petits alias explicites autour du package unicode standard, pour garder
// score.go lisible et le vocabulaire du domaine (espace/imprimable/lettre)
// au premier plan.

func isSpace(r rune) bool {
	return unicode.IsSpace(r)
}

func isPrintable(r rune) bool {
	return unicode.IsPrint(r)
}

func isLetter(r rune) bool {
	return unicode.IsLetter(r)
}
