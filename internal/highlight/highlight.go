// Package highlight colore du code Go à la manière du thème par défaut
// de VS Code (Dark+ : mots-clés bleus, contrôle violet, types turquoise,
// fonctions jaunes, variables bleu clair...) — jalon 24, pages Classes et
// Tests de cmd/jarvisapp.
//
// Construit sur go/scanner (bibliothèque standard) : aucune dépendance,
// et le découpage en jetons est exactement celui du compilateur. La
// classification des identifiants est lexicale, comme la grammaire
// TextMate utilisée par VS Code sans jetons sémantiques : elle s'appuie
// sur les jetons voisins (nom déclaré après "type", appel suivi de "(",
// sélecteur après un package importé...) et sur les ensembles de types et
// de packages connus fournis par l'appelant.
package highlight

import (
	"go/scanner"
	"go/token"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Class est la catégorie de coloration d'un jeton. La valeur sert aussi de
// suffixe de classe CSS (voir cmd/jarvisapp/templates).
type Class string

const (
	Plain   Class = ""     // ponctuation, opérateurs, espaces
	Keyword Class = "kw"   // func type struct var ... + true false nil iota
	Control Class = "ctl"  // if for return switch ...
	Type    Class = "type" // types prédéclarés, déclarés ou qualifiés
	Func    Class = "fn"   // fonctions et méthodes (déclarations et appels)
	Var     Class = "var"  // variables, paramètres, champs, packages
	String  Class = "str"
	Number  Class = "num"
	Comment Class = "com"
)

// Token est un morceau de source et sa classe. La concaténation des Text
// de tous les jetons retournés par Go reproduit exactement la source.
type Token struct {
	Text  string
	Class Class
}

// Options fournit ce que le seul fichier ne permet pas de savoir : les
// types et packages connus ailleurs (ex. un extrait de méthode, sans ses
// imports). Les imports et les types déclarés dans src sont toujours
// détectés en plus.
type Options struct {
	Types    map[string]bool
	Packages map[string]bool
}

var controlKeywords = map[token.Token]bool{
	token.IF: true, token.ELSE: true, token.FOR: true, token.RANGE: true,
	token.RETURN: true, token.SWITCH: true, token.CASE: true, token.DEFAULT: true,
	token.BREAK: true, token.CONTINUE: true, token.GOTO: true, token.FALLTHROUGH: true,
	token.GO: true, token.DEFER: true, token.SELECT: true,
}

var predeclaredTypes = map[string]bool{
	"bool": true, "byte": true, "complex64": true, "complex128": true, "error": true,
	"float32": true, "float64": true, "int": true, "int8": true, "int16": true,
	"int32": true, "int64": true, "rune": true, "string": true, "uint": true,
	"uint8": true, "uint16": true, "uint32": true, "uint64": true, "uintptr": true,
	"any": true, "comparable": true,
}

var constants = map[string]bool{"true": true, "false": true, "nil": true, "iota": true}

// notTypeAfter : un identifiant suivi de l'un de ces jetons est un nom
// (champ, paramètre, variable, clé de littéral), pas un type — ex. le
// champ "Result *Result" : le premier Result est suivi de "*".
var notTypeAfter = map[token.Token]bool{
	token.IDENT: true, token.MUL: true, token.LBRACK: true, token.MAP: true,
	token.CHAN: true, token.FUNC: true, token.STRUCT: true, token.INTERFACE: true,
	token.DEFINE: true, token.ASSIGN: true, token.COLON: true, token.ARROW: true,
	token.ELLIPSIS: true,
}

type lexeme struct {
	tok      token.Token
	lit      string
	off, end int // position dans src ; end == off pour un point-virgule implicite
}

// Go découpe et classe src. src n'a pas besoin d'être un fichier complet
// (un extrait de déclaration convient) ; une source syntaxiquement
// invalide est quand même découpée au mieux, jamais d'erreur.
func Go(src string, opts Options) []Token {
	lex := scan(src)

	types := map[string]bool{}
	for k := range opts.Types {
		types[k] = true
	}
	packages := map[string]bool{}
	for k := range opts.Packages {
		packages[k] = true
	}
	collectDeclarations(lex, types, packages)

	var out []Token
	pos := 0
	for i, l := range lex {
		if l.end == l.off {
			continue // point-virgule implicite : pas de texte propre
		}
		if l.off > pos {
			out = append(out, Token{Text: src[pos:l.off], Class: Plain})
		}
		out = append(out, Token{Text: src[l.off:l.end], Class: classify(lex, i, types, packages)})
		pos = l.end
	}
	if pos < len(src) {
		out = append(out, Token{Text: src[pos:], Class: Plain})
	}
	return out
}

func scan(src string) []lexeme {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), func(token.Position, string) {}, scanner.ScanComments)

	var lex []lexeme
	for {
		p, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		off := file.Offset(p)
		length := len(lit)
		switch {
		case tok == token.SEMICOLON && lit == "\n":
			length = 0 // implicite
		case lit == "":
			length = len(tok.String())
		}
		end := off + length
		if end > len(src) {
			end = len(src)
		}
		lex = append(lex, lexeme{tok: tok, lit: lit, off: off, end: end})
	}
	return lex
}

// collectDeclarations ajoute aux ensembles les types déclarés ("type X")
// et les noms sous lesquels les packages sont importés (alias compris).
func collectDeclarations(lex []lexeme, types, packages map[string]bool) {
	for i := 0; i < len(lex); i++ {
		switch lex[i].tok {
		case token.TYPE:
			if j := nextSignificant(lex, i); j >= 0 && lex[j].tok == token.IDENT {
				types[lex[j].lit] = true
			}
			// Bloc "type ( A ...; B ... )" : chaque ligne commence par un nom.
			if j := nextSignificant(lex, i); j >= 0 && lex[j].tok == token.LPAREN {
				for k := j + 1; k < len(lex) && lex[k].tok != token.RPAREN; k++ {
					if lex[k].tok == token.IDENT && prevSignificant(lex, k) >= 0 &&
						(lex[prevSignificant(lex, k)].tok == token.LPAREN || lex[prevSignificant(lex, k)].tok == token.SEMICOLON) {
						types[lex[k].lit] = true
					}
				}
			}
		case token.IMPORT:
			j := nextSignificant(lex, i)
			if j < 0 {
				continue
			}
			if lex[j].tok != token.LPAREN {
				addImport(lex, j, packages)
				continue
			}
			for k := j + 1; k < len(lex) && lex[k].tok != token.RPAREN; k++ {
				if lex[k].tok == token.STRING || lex[k].tok == token.IDENT {
					k = addImport(lex, k, packages)
				}
			}
		}
	}
}

// addImport lit une spécification d'import commençant en i (alias
// facultatif puis chemin) et retourne l'index du chemin.
func addImport(lex []lexeme, i int, packages map[string]bool) int {
	alias := ""
	if lex[i].tok == token.IDENT {
		alias = lex[i].lit
		i = nextSignificant(lex, i)
		if i < 0 {
			return len(lex)
		}
	}
	if lex[i].tok != token.STRING {
		return i
	}
	if alias == "_" || alias == "." {
		return i
	}
	if alias != "" {
		packages[alias] = true
		return i
	}
	path, err := strconv.Unquote(lex[i].lit)
	if err != nil {
		return i
	}
	parts := strings.Split(path, "/")
	name := parts[len(parts)-1]
	// ".../v2" : le package s'appelle d'après l'élément précédent.
	if len(parts) > 1 && len(name) > 1 && name[0] == 'v' && strings.Trim(name[1:], "0123456789") == "" {
		name = parts[len(parts)-2]
	}
	packages[strings.ReplaceAll(name, "-", "_")] = true
	return i
}

func classify(lex []lexeme, i int, types, packages map[string]bool) Class {
	l := lex[i]
	switch {
	case l.tok == token.COMMENT:
		return Comment
	case l.tok == token.STRING || l.tok == token.CHAR:
		return String
	case l.tok == token.INT || l.tok == token.FLOAT || l.tok == token.IMAG:
		return Number
	case controlKeywords[l.tok]:
		return Control
	case l.tok.IsKeyword():
		return Keyword
	case l.tok == token.IDENT:
		return classifyIdent(lex, i, types, packages)
	default:
		return Plain
	}
}

func classifyIdent(lex []lexeme, i int, types, packages map[string]bool) Class {
	name := lex[i].lit
	if constants[name] {
		return Keyword
	}
	prev, next := prevSignificant(lex, i), nextSignificant(lex, i)
	nextTok := token.EOF
	if next >= 0 {
		nextTok = lex[next].tok
	}

	// Sélecteur x.Name : dépend de ce qu'est x.
	if prev >= 0 && lex[prev].tok == token.PERIOD {
		if q := prevSignificant(lex, prev); q >= 0 && lex[q].tok == token.IDENT && packages[lex[q].lit] {
			if nextTok == token.LPAREN {
				return Func
			}
			if startsUpper(name) {
				return Type
			}
			return Var
		}
		if nextTok == token.LPAREN {
			return Func // appel de méthode
		}
		return Var // accès de champ
	}

	if prev >= 0 && lex[prev].tok == token.TYPE {
		return Type
	}
	if prev >= 0 && lex[prev].tok == token.FUNC {
		return Func
	}
	if (predeclaredTypes[name] || types[name]) && !notTypeAfter[nextTok] {
		return Type
	}
	if nextTok == token.LPAREN {
		return Func
	}
	return Var
}

func nextSignificant(lex []lexeme, i int) int {
	for j := i + 1; j < len(lex); j++ {
		if lex[j].tok != token.COMMENT {
			return j
		}
	}
	return -1
}

func prevSignificant(lex []lexeme, i int) int {
	for j := i - 1; j >= 0; j-- {
		if lex[j].tok != token.COMMENT {
			return j
		}
	}
	return -1
}

func startsUpper(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsUpper(r)
}

// Lines découpe des jetons en lignes (sans les "\n"), en coupant les
// jetons multi-lignes (commentaires /* */, chaînes brutes) sans perdre
// leur classe — pour un rendu avec numéros de ligne.
func Lines(toks []Token) [][]Token {
	lines := [][]Token{nil}
	for _, t := range toks {
		parts := strings.Split(t.Text, "\n")
		for k, part := range parts {
			if k > 0 {
				lines = append(lines, nil)
			}
			if part != "" {
				lines[len(lines)-1] = append(lines[len(lines)-1], Token{Text: part, Class: t.Class})
			}
		}
	}
	return lines
}
