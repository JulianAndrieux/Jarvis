package textview

import (
	"reflect"
	"strings"
	"testing"
)

// utf16le encode s en UTF-16LE précédé du BOM, à la main : on ne veut pas
// que la fixture dépende du code testé.
func utf16le(s string) []byte {
	out := []byte{0xFF, 0xFE}
	for _, u := range utf16Units(s) {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

func utf16be(s string) []byte {
	out := []byte{0xFE, 0xFF}
	for _, u := range utf16Units(s) {
		out = append(out, byte(u>>8), byte(u))
	}
	return out
}

// utf16Units découpe s en unités UTF-16, paires de substitution comprises
// (calcul explicite, sans unicode/utf16, pour rester indépendant de l'impl).
func utf16Units(s string) []uint16 {
	var units []uint16
	for _, r := range s {
		if r >= 0x10000 {
			r -= 0x10000
			units = append(units, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
			continue
		}
		units = append(units, uint16(r))
	}
	return units
}

func TestDecode(t *testing.T) {
	cases := []struct {
		name     string
		in       []byte
		wantText string
		wantEnc  string
	}{
		{"utf-8 simple", []byte("bonjour été"), "bonjour été", "utf-8"},
		{"vide", []byte{}, "", "utf-8"},
		{"utf-8 avec BOM", append([]byte{0xEF, 0xBB, 0xBF}, "café"...), "café", "utf-8-bom"},
		{"utf-16le avec BOM, accent et emoji", utf16le("été 😀"), "été 😀", "utf-16le"},
		{"utf-16be avec BOM", utf16be("Noël 😀"), "Noël 😀", "utf-16be"},
		{"utf-16le taille impaire", append(utf16le("ab"), 0x41), "ab�", "utf-16le"},
		{
			"windows-1252",
			[]byte("caf\xe9 \x80 l\x92\x9cuvre \x93ok\x94"),
			"café € l’œuvre “ok”",
			"windows-1252",
		},
		{"CRLF normalisé", []byte("a\r\nb\r\n"), "a\nb\n", "utf-8"},
		{"CR seul normalisé", []byte("a\rb\r\rc"), "a\nb\n\nc", "utf-8"},
		{"CRLF en windows-1252", []byte("\xe9\r\n\xe8\r"), "é\nè\n", "windows-1252"},
		{"CRLF en utf-16le", utf16le("a\r\nb"), "a\nb", "utf-16le"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, enc := Decode(c.in)
			if text != c.wantText {
				t.Errorf("texte = %q, attendu %q", text, c.wantText)
			}
			if enc != c.wantEnc {
				t.Errorf("encodage = %q, attendu %q", enc, c.wantEnc)
			}
		})
	}
}

func TestIsProbablyText(t *testing.T) {
	pdf := append([]byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n"), 0x00, 0x01, 0xFF, 0x00, 0x8A, 0x12, 0x00)
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	cases := []struct {
		name string
		in   []byte
		want bool
	}{
		{"utf-8", []byte("Bonjour, voici un texte accentué.\n"), true},
		{"windows-1252", []byte("Cr\xe9dit \x80 12,50;d\xe9bit\r\n"), true},
		{"pdf binaire", pdf, false},
		{"png", png, false},
		{"utf-16le à BOM", utf16le("été"), true},
		{"utf-16be à BOM", utf16be("été"), true},
		{"vide", nil, true},
		{"trop de caractères de contrôle", []byte("ab\x01\x02\x03\x04cd"), false},
		{"tabulations et saut de page tolérés", []byte("a\tb\fc\r\nd"), true},
		{"NUL au-delà de 8000 octets", append([]byte(strings.Repeat("a", 9000)), 0x00), true},
		// L'échantillon de 8000 octets coupe un "Ł" (C5 81) en deux. S'il
		// était alors pris pour du Windows-1252, chaque 0x81 deviendrait un
		// contrôle C1 (≈ 50 % du texte) et un polonais valide serait rejeté.
		{"échantillon coupé au milieu d'un caractère", []byte("a" + strings.Repeat("Ł", 4000)), true},
		// Contrôles C1 (0x81, 0x8D…) : non définis en Windows-1252, ils
		// comptent comme contrôles et trahissent un binaire.
		{"octets C1 majoritaires", []byte("\x81\x8d\x8f\x90\x9dab"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsProbablyText(c.in); got != c.want {
				t.Errorf("IsProbablyText = %v, attendu %v", got, c.want)
			}
		})
	}
}

func TestParseCSV(t *testing.T) {
	cases := []struct {
		name    string
		in      []byte
		maxRows int
		want    Table
	}{
		{
			name: "anglais à virgules",
			in:   []byte("name,qty,price\napple,3,1.50\npear,2,0.99\n"),
			want: Table{
				Header:    []string{"name", "qty", "price"},
				Rows:      [][]string{{"apple", "3", "1.50"}, {"pear", "2", "0.99"}},
				Delimiter: ',',
				TotalRows: 2,
			},
		},
		{
			// Les décimales à virgule multiplient les virgules : seul le
			// point-virgule donne un nombre de colonnes constant.
			name: "français à points-virgules et décimales à virgule",
			in:   []byte("Libellé;Montant;TVA\nCafé;12,50;2,50\nThé;3,20;0,64\nPain;1,10;0,06\n"),
			want: Table{
				Header:    []string{"Libellé", "Montant", "TVA"},
				Rows:      [][]string{{"Café", "12,50", "2,50"}, {"Thé", "3,20", "0,64"}, {"Pain", "1,10", "0,06"}},
				Delimiter: ';',
				TotalRows: 3,
			},
		},
		{
			name: "tabulations",
			in:   []byte("a\tb\tc\n1\t2\t3\n"),
			want: Table{
				Header:    []string{"a", "b", "c"},
				Rows:      [][]string{{"1", "2", "3"}},
				Delimiter: '\t',
				TotalRows: 1,
			},
		},
		{
			name: "pipes",
			in:   []byte("id|nom\n1|Alice, Bob\n2|Chloé\n"),
			want: Table{
				Header:    []string{"id", "nom"},
				Rows:      [][]string{{"1", "Alice, Bob"}, {"2", "Chloé"}},
				Delimiter: '|',
				TotalRows: 2,
			},
		},
		{
			name: "champ entre guillemets avec séparateur et retour à la ligne",
			in:   []byte("nom;adresse;ville\nDupont;\"12; rue des Lilas\r\nBât. B\";Lyon\nMartin;\"3 av. Foch\";Paris\n"),
			want: Table{
				Header:    []string{"nom", "adresse", "ville"},
				Rows:      [][]string{{"Dupont", "12; rue des Lilas\nBât. B", "Lyon"}, {"Martin", "3 av. Foch", "Paris"}},
				Delimiter: ';',
				TotalRows: 2,
			},
		},
		{
			name: "guillemets doublés",
			in:   []byte("titre,auteur\n\"Le \"\"Petit\"\" Prince\",Saint-Exupéry\n"),
			want: Table{
				Header:    []string{"titre", "auteur"},
				Rows:      [][]string{{"Le \"Petit\" Prince", "Saint-Exupéry"}},
				Delimiter: ',',
				TotalRows: 1,
			},
		},
		{
			name: "largeurs inégales complétées",
			in:   []byte("a;b;c\n1;2;3\n4;5\n6;7;8;9\n"),
			want: Table{
				Header:    []string{"a", "b", "c", ""},
				Rows:      [][]string{{"1", "2", "3", ""}, {"4", "5", "", ""}, {"6", "7", "8", "9"}},
				Delimiter: ';',
				TotalRows: 3,
			},
		},
		{
			// Export Excel FR typique : pas de BOM, CRLF, accents et € en
			// Windows-1252, décimales à virgule.
			name: "Excel français en windows-1252",
			in:   []byte("Date;Libell\xe9;Montant (\x80)\r\n01/02/2026;Caf\xe9 cr\xe8me;12,50\r\n02/02/2026;\x93Pr\xeat\x94 \x9cuvre;1\xa0200,00\r\n"),
			want: Table{
				Header:    []string{"Date", "Libellé", "Montant (€)"},
				Rows:      [][]string{{"01/02/2026", "Café crème", "12,50"}, {"02/02/2026", "“Prêt” œuvre", "1 200,00"}},
				Delimiter: ';',
				TotalRows: 2,
			},
		},
		{
			name: "BOM, espaces et lignes vides en tête, lignes vides au milieu",
			in:   append([]byte{0xEF, 0xBB, 0xBF}, "\n\n  a;b\n1;2\n\n3;4\n\n"...),
			want: Table{
				Header:    []string{"a", "b"},
				Rows:      [][]string{{"1", "2"}, {"3", "4"}},
				Delimiter: ';',
				TotalRows: 2,
			},
		},
		{
			name:    "maxRows tronque",
			in:      []byte("n,carre\n1,1\n2,4\n3,9\n4,16\n5,25\n"),
			maxRows: 2,
			want: Table{
				Header:    []string{"n", "carre"},
				Rows:      [][]string{{"1", "1"}, {"2", "4"}},
				Delimiter: ',',
				Truncated: true,
				TotalRows: 5,
			},
		},
		{
			name:    "maxRows exactement atteint : pas de troncature",
			in:      []byte("n,carre\n1,1\n2,4\n"),
			maxRows: 2,
			want: Table{
				Header:    []string{"n", "carre"},
				Rows:      [][]string{{"1", "1"}, {"2", "4"}},
				Delimiter: ',',
				TotalRows: 2,
			},
		},
		{
			name: "une seule colonne",
			in:   []byte("prenom\nAlice\nBob\n"),
			want: Table{
				Header:    []string{"prenom"},
				Rows:      [][]string{{"Alice"}, {"Bob"}},
				Delimiter: ',',
				TotalRows: 2,
			},
		},
		{
			name: "vide",
			in:   []byte{},
			want: Table{Delimiter: ','},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseCSV(c.in, c.maxRows)
			if err != nil {
				t.Fatalf("erreur inattendue : %v", err)
			}
			if !reflect.DeepEqual(got.Header, c.want.Header) {
				t.Errorf("Header = %q, attendu %q", got.Header, c.want.Header)
			}
			if len(got.Rows) != len(c.want.Rows) || (len(got.Rows) > 0 && !reflect.DeepEqual(got.Rows, c.want.Rows)) {
				t.Errorf("Rows = %q, attendu %q", got.Rows, c.want.Rows)
			}
			if got.Delimiter != c.want.Delimiter {
				t.Errorf("Delimiter = %q, attendu %q", got.Delimiter, c.want.Delimiter)
			}
			if got.Truncated != c.want.Truncated {
				t.Errorf("Truncated = %v, attendu %v", got.Truncated, c.want.Truncated)
			}
			if got.TotalRows != c.want.TotalRows {
				t.Errorf("TotalRows = %d, attendu %d", got.TotalRows, c.want.TotalRows)
			}
		})
	}
}

func TestParseCSVBinaire(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x01\x00")
	if _, err := ParseCSV(png, 0); err == nil {
		t.Fatal("erreur attendue pour un contenu binaire")
	}
}

func TestParseCSVGuillemetNonFerme(t *testing.T) {
	// Un guillemet jamais refermé ne doit ni paniquer ni faire échouer
	// l'aperçu : on affiche ce qu'on peut.
	if _, err := ParseCSV([]byte("a;b\n\"x;y\n1;2\n"), 0); err != nil {
		t.Fatalf("erreur inattendue : %v", err)
	}
}

// TestEntreesArbitrairesSansPanic balaie des octets pseudo-aléatoires
// déterministes : aucune entrée, même absurde, ne doit faire paniquer le
// paquet (contrainte du dépôt : jamais de panic sur une entrée malformée).
func TestEntreesArbitrairesSansPanic(t *testing.T) {
	seed := uint32(1)
	for n := 0; n < 300; n++ {
		b := make([]byte, n)
		for i := range b {
			seed = seed*1664525 + 1013904223
			b[i] = byte(seed >> 24)
		}
		text, _ := Decode(b)
		IsProbablyText(b)
		_, _ = ParseCSV(b, 3)
		Truncate(text, n/2)
		Truncate(string(b), n/3)
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		name     string
		s        string
		max      int
		want     string
		wantCoup bool
	}{
		{"coupe au milieu d'un é", "abé", 3, "ab", true},
		{"coupe juste après le é", "abéc", 4, "abé", true},
		{"plus court", "abé", 10, "abé", false},
		{"longueur exacte", "abé", 4, "abé", false},
		{"coupe au milieu d'un emoji", "a😀", 3, "a", true},
		{"max nul", "abc", 0, "", true},
		{"max négatif", "abc", -1, "", true},
		{"vide", "", 0, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, cut := Truncate(c.s, c.max)
			if got != c.want || cut != c.wantCoup {
				t.Errorf("Truncate(%q, %d) = (%q, %v), attendu (%q, %v)", c.s, c.max, got, cut, c.want, c.wantCoup)
			}
		})
	}
}
