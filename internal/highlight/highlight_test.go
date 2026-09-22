package highlight

import (
	"strings"
	"testing"
)

// classOf retourne la classe du premier jeton dont le texte est exactement
// text à partir de l'occurrence n (0 = première) — assez lisible pour
// exprimer "le deuxième Job de ce snippet est un type".
func classOf(t *testing.T, toks []Token, text string, n int) Class {
	t.Helper()
	for _, tok := range toks {
		if tok.Text == text {
			if n == 0 {
				return tok.Class
			}
			n--
		}
	}
	t.Fatalf("token %q (occurrence %d) not found in %+v", text, n, toks)
	return ""
}

func TestGo_ConcatenatedTokensReproduceSourceExactly(t *testing.T) {
	src := "package x\n\n// Doc.\ntype Job struct {\n\tID   string `json:\"id\"`\n\tN    int // compteur\n}\n\nfunc (j *Job) Run() error {\n\tif j.N > 0x1F {\n\t\treturn nil\n\t}\n\treturn fmt.Errorf(\"boom %d\", 3.5)\n}\n"
	var b strings.Builder
	for _, tok := range Go(src, Options{}) {
		b.WriteString(tok.Text)
	}
	if b.String() != src {
		t.Errorf("tokens do not reproduce the source:\n got: %q\nwant: %q", b.String(), src)
	}
}

func TestGo_ClassifiesLikeVSCodeDarkPlus(t *testing.T) {
	src := `package x

import "fmt"

// Doc comment.
type Job struct {
	ID     string
	Result *Result
	Count  int
}

func (j *Job) Run(ctx context.Context) error {
	if j.Count > 42 {
		return nil
	}
	for _, r := range j.Items {
		fmt.Println("item", r, string(b), true)
	}
	return Helper(j.Result)
}
`
	toks := Go(src, Options{Types: map[string]bool{"Result": true}, Packages: map[string]bool{"context": true}})

	cases := []struct {
		text string
		n    int
		want Class
	}{
		{"package", 0, Keyword},
		{"import", 0, Keyword},
		{`"fmt"`, 0, String},
		{"// Doc comment.", 0, Comment},
		{"type", 0, Keyword},
		{"Job", 0, Type}, // nom déclaré par "type"
		{"struct", 0, Keyword},
		{"ID", 0, Var},      // nom de champ
		{"string", 0, Type}, // type prédéclaré
		{"Result", 0, Var},  // nom de champ, même si "Result" est un type connu
		{"Result", 1, Type}, // type du champ
		{"int", 0, Type},
		{"func", 0, Keyword},
		{"j", 0, Var},
		{"Job", 1, Type}, // récepteur *Job
		{"Run", 0, Func}, // nom de méthode déclarée
		{"ctx", 0, Var},
		{"context", 0, Var},  // qualificatif de package
		{"Context", 0, Type}, // pkg.Type
		{"error", 0, Type},
		{"if", 0, Control},
		{"Count", 1, Var}, // accès champ j.Count
		{"42", 0, Number},
		{"return", 0, Control},
		{"nil", 0, Keyword},
		{"for", 0, Control},
		{"range", 0, Control},
		{"Println", 0, Func}, // pkg.Func()
		{`"item"`, 0, String},
		{"string", 1, Type}, // conversion string(b)
		{"true", 0, Keyword},
		{"Helper", 0, Func}, // appel de fonction
		{"Result", 2, Var},  // j.Result : champ, pas type
	}
	for _, c := range cases {
		if got := classOf(t, toks, c.text, c.n); got != c.want {
			t.Errorf("%q (occurrence %d) = %q, want %q", c.text, c.n, got, c.want)
		}
	}
}

// Les imports d'un fichier complet sont reconnus sans Options : fmt.Errorf
// est un appel qualifié, pas un accès de champ.
func TestGo_PackagesFromImportsAreDetected(t *testing.T) {
	src := "package x\n\nimport (\n\t\"fmt\"\n\tjson \"encoding/json\"\n)\n\nvar _ = fmt.Sprint(json.RawMessage{})\n"
	toks := Go(src, Options{})
	if got := classOf(t, toks, "RawMessage", 0); got != Type {
		t.Errorf("json.RawMessage = %q, want type (json imported under an alias)", got)
	}
	if got := classOf(t, toks, "Sprint", 0); got != Func {
		t.Errorf("fmt.Sprint = %q, want func", got)
	}
}

func TestGo_StructTagAndRawStringsAreStrings(t *testing.T) {
	toks := Go("type T struct {\n\tA int `json:\"a\"`\n}\n", Options{})
	if got := classOf(t, toks, "`json:\"a\"`", 0); got != String {
		t.Errorf("struct tag = %q, want string", got)
	}
}

func TestLines_SplitsMultilineTokensKeepingClasses(t *testing.T) {
	toks := []Token{{Text: "a := ", Class: Plain}, {Text: "/* x\ny */", Class: Comment}, {Text: "\nb", Class: Var}}
	lines := Lines(toks)
	if len(lines) != 3 {
		t.Fatalf("Lines() = %d lines, want 3: %+v", len(lines), lines)
	}
	if lines[1][0].Text != "y */" || lines[1][0].Class != Comment {
		t.Errorf("line 2 = %+v, want the comment's second line, still a comment", lines[1])
	}
	if lines[2][0].Text != "b" || lines[2][0].Class != Var {
		t.Errorf("line 3 = %+v, want b as var", lines[2])
	}
}
