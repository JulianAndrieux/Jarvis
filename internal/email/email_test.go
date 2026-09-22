package email

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fixturePDF est le contenu exact encodé en base64 dans mixed_pdf.eml :
// comparer octet à octet prouve que le décodage base64 (lignes de 60
// caractères, CRLF entre elles) ne perd ni n'ajoute rien.
var fixturePDF = []byte("%PDF-1.4\n%Jarvis fixture\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n")

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("lecture de la fixture %s : %v", name, err)
	}
	return raw
}

func parseFixture(t *testing.T, name string) Message {
	t.Helper()
	m, err := Parse(readFixture(t, name))
	if err != nil {
		t.Fatalf("Parse(%s) error = %v, want nil", name, err)
	}
	return m
}

func findAttachment(t *testing.T, m Message, filename string) Attachment {
	t.Helper()
	for _, a := range m.Attachments {
		if a.Filename == filename {
			return a
		}
	}
	var names []string
	for _, a := range m.Attachments {
		names = append(names, a.Filename)
	}
	t.Fatalf("pièce jointe %q absente, pièces présentes : %q", filename, names)
	return Attachment{}
}

func TestParse_SimpleTextUTF8(t *testing.T) {
	m := parseFixture(t, "simple_utf8.eml")

	wantFrom := []Address{{Name: "Jean Dupont", Email: "jean.dupont@example.fr"}}
	if !reflect.DeepEqual(m.From, wantFrom) {
		t.Errorf("From = %#v, want %#v", m.From, wantFrom)
	}
	wantTo := []Address{
		{Name: "Marie Curie", Email: "marie@example.com"},
		{Name: "", Email: "paul@example.com"},
	}
	if !reflect.DeepEqual(m.To, wantTo) {
		t.Errorf("To = %#v, want %#v", m.To, wantTo)
	}
	wantCc := []Address{{Name: "Service Comptabilité", Email: "compta@example.fr"}}
	if !reflect.DeepEqual(m.Cc, wantCc) {
		t.Errorf("Cc = %#v, want %#v", m.Cc, wantCc)
	}
	if m.Subject != "Réunion de lundi" {
		t.Errorf("Subject = %q, want %q", m.Subject, "Réunion de lundi")
	}
	wantText := "Bonjour Marie,\n\nLa réunion de lundi est déplacée à 14h, salle Été.\nMerci de prévenir l'équipe.\n\nJean\n"
	if m.Text != wantText {
		t.Errorf("Text = %q, want %q", m.Text, wantText)
	}
	if m.HTML != "" {
		t.Errorf("HTML = %q, want empty", m.HTML)
	}
	if len(m.Attachments) != 0 {
		t.Errorf("Attachments = %d, want 0", len(m.Attachments))
	}
}

func TestParse_Date(t *testing.T) {
	m := parseFixture(t, "simple_utf8.eml")
	want := time.Date(2026, 9, 22, 9, 14, 0, 0, time.FixedZone("", 2*3600))
	if !m.Date.Equal(want) {
		t.Errorf("Date = %v, want %v", m.Date, want)
	}

	cases := map[string]string{
		"absente":   "From: a@example.com\r\nSubject: x\r\n\r\ncorps\r\n",
		"illisible": "From: a@example.com\r\nDate: pas une date du tout\r\n\r\ncorps\r\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			m, err := Parse([]byte(raw))
			if err != nil {
				t.Fatalf("Parse error = %v, want nil", err)
			}
			if !m.Date.IsZero() {
				t.Errorf("Date = %v, want zero", m.Date)
			}
		})
	}
}

func TestParse_MultipartAlternative(t *testing.T) {
	m := parseFixture(t, "alternative.eml")

	wantText := "Votre commande n° 1234 a bien été enregistrée. Elle sera expédiée sous 48h."
	if m.Text != wantText {
		t.Errorf("Text = %q, want %q", m.Text, wantText)
	}
	wantHTML := "<html><head><title>Commande</title></head><body><p>Votre commande n&deg; 1234 a bien &eacute;t&eacute; enregistr&eacute;e.</p></body></html>"
	if m.HTML != wantHTML {
		t.Errorf("HTML = %q, want %q", m.HTML, wantHTML)
	}
	if len(m.Attachments) != 0 {
		t.Errorf("Attachments = %d, want 0 (le préambule/épilogue ne sont pas des parties)", len(m.Attachments))
	}
	wantFrom := []Address{{Name: "Boutique Exemple", Email: "commandes@boutique.example"}}
	if !reflect.DeepEqual(m.From, wantFrom) {
		t.Errorf("From = %#v, want %#v", m.From, wantFrom)
	}
}

func TestParse_MixedWithAlternativeAndPDF(t *testing.T) {
	m := parseFixture(t, "mixed_pdf.eml")

	if want := "Bonjour à tous,\nLe fichier est joint.\n"; m.Text != want {
		t.Errorf("Text = %q, want %q", m.Text, want)
	}
	if want := "<p>Bonjour à tous,<br>Le fichier est joint.</p>"; m.HTML != want {
		t.Errorf("HTML = %q, want %q", m.HTML, want)
	}
	if len(m.Attachments) != 1 {
		t.Fatalf("Attachments = %d, want 1", len(m.Attachments))
	}
	a := m.Attachments[0]
	if a.Filename != "facture-2026-09.pdf" {
		t.Errorf("Filename = %q, want %q", a.Filename, "facture-2026-09.pdf")
	}
	if a.ContentType != "application/pdf" {
		t.Errorf("ContentType = %q, want application/pdf", a.ContentType)
	}
	if a.Size != len(fixturePDF) {
		t.Errorf("Size = %d, want %d", a.Size, len(fixturePDF))
	}
	if !bytes.Equal(a.Data, fixturePDF) {
		t.Errorf("Data = %q, want %q", a.Data, fixturePDF)
	}
}

func TestParse_MultipartRelatedInlineImage(t *testing.T) {
	m := parseFixture(t, "related_inline.eml")

	if !strings.Contains(m.HTML, `src="cid:logo@example.org"`) {
		t.Errorf("HTML = %q, want it to reference cid:logo@example.org", m.HTML)
	}
	if m.Text != "" {
		t.Errorf("Text = %q, want empty", m.Text)
	}
	a := findAttachment(t, m, "logo.png")
	if a.ContentType != "image/png" {
		t.Errorf("ContentType = %q, want image/png", a.ContentType)
	}
	wantPNG := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDRfake")
	if !bytes.Equal(a.Data, wantPNG) || a.Size != len(wantPNG) {
		t.Errorf("Data = %q (Size %d), want %q (Size %d)", a.Data, a.Size, wantPNG, len(wantPNG))
	}
}

func TestParse_TransferEncodings(t *testing.T) {
	cases := []struct {
		name     string
		encoding string
		body     string
		want     string
	}{
		{"7bit", "7bit", "Texte ASCII simple.\r\nDeuxieme ligne.\r\n", "Texte ASCII simple.\nDeuxieme ligne.\n"},
		{"8bit", "8bit", "Texte accentué : été.\r\n", "Texte accentué : été.\n"},
		{"absent vaut 7bit", "", "Sans en-tête d'encodage.\r\n", "Sans en-tête d'encodage.\n"},
		{"base64", "base64", "w4l0w6kgMjAyNiA6IGJhc2U2NCDinJM=\r\n", "Été 2026 : base64 ✓"},
		{"base64 sur plusieurs lignes", "BASE64", "w4l0w6kgMjAy\r\nNiA6IGJhc2U2\r\nNCDinJM=\r\n", "Été 2026 : base64 ✓"},
		{"base64 sans padding", "base64", "w4l0w6k\r\n", "Été"},
		{"quoted-printable", "quoted-printable", "caf=C3=A9 cr=C3=A8me=\r\n bio =3D 3=E2=82=AC\r\n", "café crème bio = 3€\n"},
		{"quoted-printable minuscules et = orphelin", "quoted-printable", "caf=c3=a9 =ZZ fin=", "café =ZZ fin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := "From: a@example.com\r\nSubject: enc\r\nContent-Type: text/plain; charset=utf-8\r\n"
			if c.encoding != "" {
				raw += "Content-Transfer-Encoding: " + c.encoding + "\r\n"
			}
			raw += "\r\n" + c.body
			m, err := Parse([]byte(raw))
			if err != nil {
				t.Fatalf("Parse error = %v, want nil", err)
			}
			if m.Text != c.want {
				t.Errorf("Text = %q, want %q", m.Text, c.want)
			}
		})
	}
}

func TestParse_QuotedPrintableLatin1SoftBreak(t *testing.T) {
	m := parseFixture(t, "qp_latin1.eml")
	want := "Voici un très long texte qui dépasse la limite de soixante-seize caractères par ligne.\nFinée = ok.\n"
	if m.Text != want {
		t.Errorf("Text = %q, want %q", m.Text, want)
	}
}

func TestParse_BodyCharsets(t *testing.T) {
	cases := []struct {
		fixture string
		want    string
	}{
		{"latin1_8bit.eml", "Café crème, à bientôt. Symbole : ¤\n"},
		{"latin9_euro.eml", "Prix : 12,50 € Œœ\n"},
		{"cp1252_quotes.eml", "“Bonjour” – total : 99€ …\n"},
		{"unknown_charset_utf8.eml", "Déjà vu\n"},
		{"unknown_charset_bytes.eml", "Déjà vu €\n"},
		{"related_inline.eml", ""}, // us-ascii, corps HTML seul : vérifié ailleurs
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			m := parseFixture(t, c.fixture)
			if m.Text != c.want {
				t.Errorf("Text = %q, want %q", m.Text, c.want)
			}
		})
	}
}

func TestDecodeCharset(t *testing.T) {
	cases := []struct {
		charset string
		in      string
		want    string
	}{
		{"utf-8", "Déjà", "Déjà"},
		{"UTF8", "Déjà", "Déjà"},
		{"us-ascii", "plain", "plain"},
		{"", "plain", "plain"},
		{"iso-8859-1", "\xe9\xe8\xff", "éèÿ"},
		{"latin1", "\xa4", "¤"},
		{"ISO-8859-15", "\xa4\xa6\xa8\xb4\xb8\xbc\xbd\xbe\xe9", "€ŠšŽžŒœŸé"},
		{"windows-1252", "\x80\x85\x91\x92\x93\x94\x96\x97\x99\x9c", "€…‘’“”–—™œ"},
		{"cp1252", "\xe0", "à"},
		// us-ascii mal étiqueté : les octets hauts sont lus en windows-1252
		// plutôt que perdus.
		{"us-ascii", "caf\xe9", "café"},
		// utf-8 déclaré mais invalide : on garde ce qui est valide et on
		// remplace l'octet fautif, sans jamais produire d'UTF-8 invalide.
		{"utf-8", "ok\xff", "ok�"},
		{"x-inconnu", "Déjà", "Déjà"},
		{"x-inconnu", "D\xe9j\xe0 \x80", "Déjà €"},
	}
	for _, c := range cases {
		if got := decodeCharset(c.charset, []byte(c.in)); got != c.want {
			t.Errorf("decodeCharset(%q, %q) = %q, want %q", c.charset, c.in, got, c.want)
		}
	}
}

func TestParse_RFC2047Headers(t *testing.T) {
	m := parseFixture(t, "rfc2047_headers.eml")

	if want := "Réunion de lundi : ordre du jour ✓"; m.Subject != want {
		t.Errorf("Subject = %q, want %q", m.Subject, want)
	}
	wantFrom := []Address{{Name: "François Müller", Email: "francois@example.de"}}
	if !reflect.DeepEqual(m.From, wantFrom) {
		t.Errorf("From = %#v, want %#v", m.From, wantFrom)
	}
	wantTo := []Address{
		{Name: "Société €uro", Email: "contact@euro.example"},
		{Name: "Zoé Lefèvre", Email: "zoe@example.fr"},
	}
	if !reflect.DeepEqual(m.To, wantTo) {
		t.Errorf("To = %#v, want %#v", m.To, wantTo)
	}
	wantCc := []Address{{Name: "“Cité”", Email: "cite@example.fr"}}
	if !reflect.DeepEqual(m.Cc, wantCc) {
		t.Errorf("Cc = %#v, want %#v", m.Cc, wantCc)
	}
}

func TestParse_SubjectEncodedWordsVariants(t *testing.T) {
	cases := map[string]string{
		"=?ISO-8859-1?Q?R=E9sum=E9_annuel?=":             "Résumé annuel",
		"=?utf-8?q?caf=C3=A9?= =?utf-8?q?_cr=C3=A8me?=":  "café crème",
		"Facture =?UTF-8?B?bsKw?= 12":                    "Facture n° 12",
		"=?windows-1252?Q?=93cit=E9=94?=":                "“cité”",
		"=?x-inconnu?Q?D=E9j=E0?=":                       "Déjà",
		"Sujet sans encodage":                            "Sujet sans encodage",
		"=?UTF-8?B?cGFzIGR1IGJhc2U2NA!!?= reste lisible": "=?UTF-8?B?cGFzIGR1IGJhc2U2NA!!?= reste lisible",
		"=?utf-8?B?UsOpdW5pb24gZGUgbHVuZGkgOiA=?=\r\n =?utf-8?B?b3JkcmUgZHUgam91ciDinJM=?=": "Réunion de lundi : ordre du jour ✓",
	}
	for subject, want := range cases {
		raw := "From: a@example.com\r\nSubject: " + subject + "\r\n\r\ncorps\r\n"
		m, err := Parse([]byte(raw))
		if err != nil {
			t.Fatalf("Parse(Subject %q) error = %v", subject, err)
		}
		if m.Subject != want {
			t.Errorf("Subject %q décodé en %q, want %q", subject, m.Subject, want)
		}
	}
}

func TestParse_AttachmentNames(t *testing.T) {
	m := parseFixture(t, "attachment_names.eml")

	if want := "Voir les pieces jointes."; m.Text != want {
		t.Errorf("Text = %q, want %q (une pièce text/plain en attachment ne doit pas finir dans le corps)", m.Text, want)
	}
	if len(m.Attachments) != 4 {
		t.Fatalf("Attachments = %d, want 4", len(m.Attachments))
	}

	pdf := findAttachment(t, m, "facture n° 12.pdf") // RFC 2231 filename*=
	if pdf.ContentType != "application/pdf" || string(pdf.Data) != "%PDF-1.4\n" || pdf.Size != 9 {
		t.Errorf("facture = %+v", pdf)
	}

	txt := findAttachment(t, m, "résumé.txt") // RFC 2047 dans name=
	if txt.ContentType != "text/plain" {
		t.Errorf("résumé.txt ContentType = %q, want text/plain", txt.ContentType)
	}
	// Les octets d'une pièce jointe restent bruts : pas de conversion de
	// charset ni de fins de ligne, le fichier doit être restitué à l'identique.
	if want := "Contenu du résumé\n"; string(txt.Data) != want || txt.Size != len(want) {
		t.Errorf("résumé.txt Data = %q (Size %d), want %q", txt.Data, txt.Size, want)
	}

	findAttachment(t, m, "rapport annuel.xlsx") // RFC 2231 avec continuations
	// Un nom avec chemin est réduit à son dernier composant : l'appelant
	// peut s'en servir comme nom de fichier sans risque de traversée.
	findAttachment(t, m, "devis final.docx")
}

func TestParse_LFLineEndings(t *testing.T) {
	raw := readFixture(t, "lf_only.eml")
	if bytes.Contains(raw, []byte("\r")) {
		t.Fatal("la fixture lf_only.eml ne doit contenir aucun CR")
	}
	m := parseFixture(t, "lf_only.eml")

	if m.Subject != "Fins de ligne Unix" {
		t.Errorf("Subject = %q", m.Subject)
	}
	if want := []Address{{Name: "Alice", Email: "alice@example.com"}}; !reflect.DeepEqual(m.From, want) {
		t.Errorf("From = %#v, want %#v", m.From, want)
	}
	if want := "Ligne un avec un saut doux au milieu.\nLigne deux : café."; m.Text != want {
		t.Errorf("Text = %q, want %q", m.Text, want)
	}
	csv := findAttachment(t, m, "donnees.csv")
	if string(csv.Data) != "a;b\n1;2" || csv.ContentType != "text/csv" {
		t.Errorf("donnees.csv = %+v", csv)
	}
}

func TestParse_LFConvertedFixturesMatchCRLF(t *testing.T) {
	// Chaque fixture CRLF convertie en LF seul doit donner le même résultat :
	// les e-mails exportés par certains clients (mbox, copier-coller) n'ont
	// plus de CR.
	for _, name := range []string{"simple_utf8.eml", "alternative.eml", "mixed_pdf.eml", "attachment_names.eml", "rfc2047_headers.eml"} {
		t.Run(name, func(t *testing.T) {
			crlf := parseFixture(t, name)
			lf, err := Parse(bytes.ReplaceAll(readFixture(t, name), []byte("\r\n"), []byte("\n")))
			if err != nil {
				t.Fatalf("Parse(LF) error = %v", err)
			}
			if !reflect.DeepEqual(crlf, lf) {
				t.Errorf("résultat LF différent du CRLF :\nCRLF %+v\nLF   %+v", crlf, lf)
			}
		})
	}
}

func TestParse_MissingBoundary(t *testing.T) {
	m := parseFixture(t, "no_boundary.eml")
	if want := "Texte retrouve malgre l'absence de boundary."; m.Text != want {
		t.Errorf("Text = %q, want %q", m.Text, want)
	}
}

func TestParse_MissingBoundaryWithoutDelimiterFallsBackToText(t *testing.T) {
	raw := "From: a@example.com\r\nContent-Type: multipart/mixed\r\n\r\nPas de délimiteur ici.\r\n"
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}
	if want := "Pas de délimiteur ici.\n"; m.Text != want {
		t.Errorf("Text = %q, want %q", m.Text, want)
	}
}

func TestParse_InvalidBase64DoesNotBlockOtherParts(t *testing.T) {
	m := parseFixture(t, "bad_base64.eml")
	if want := "Bonjour à tous,\nLe fichier est joint.\n"; m.Text != want {
		t.Errorf("Text = %q, want %q", m.Text, want)
	}
	findAttachment(t, m, "casse.bin")
	ok := findAttachment(t, m, "ok.pdf")
	if string(ok.Data) != "%PDF-1.4\n" {
		t.Errorf("ok.pdf Data = %q", ok.Data)
	}
}

func TestParse_HeaderWithoutColon(t *testing.T) {
	m := parseFixture(t, "header_no_colon.eml")
	if want := []Address{{Name: "Robert", Email: "robert@example.com"}}; !reflect.DeepEqual(m.From, want) {
		t.Errorf("From = %#v, want %#v", m.From, want)
	}
	if m.Subject != "En-tete malforme" {
		t.Errorf("Subject = %q", m.Subject)
	}
	if !m.Date.IsZero() {
		t.Errorf("Date = %v, want zero", m.Date)
	}
	if want := "Le corps reste lisible.\n"; m.Text != want {
		t.Errorf("Text = %q, want %q", m.Text, want)
	}
}

func TestParse_UnreadableInputReturnsError(t *testing.T) {
	cases := map[string][]byte{
		"nil":                nil,
		"vide":               []byte(""),
		"blancs":             []byte("  \r\n\r\n\t\n"),
		"texte sans en-tête": []byte("juste du texte, sans aucun en-tête\nsur deux lignes\n"),
		"binaire":            {0x00, 0x01, 0xff, 0xfe, 0x89, 0x50},
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(raw); err == nil {
				t.Errorf("Parse(%q) error = nil, want non-nil", raw)
			}
		})
	}
}

func TestParse_MalformedAddressesKeptBestEffort(t *testing.T) {
	raw := "From: Jean <jean@example.fr\r\nTo: pas-une-adresse, ok@example.com\r\n\r\ncorps\r\n"
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}
	found := false
	for _, a := range m.To {
		if a.Email == "ok@example.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("To = %#v, want it to contain ok@example.com", m.To)
	}
}

// TestParse_NeverPanics tronque chaque fixture à toutes les longueurs et
// injecte des entrées pathologiques : Parse doit toujours rendre la main,
// avec ou sans erreur, mais sans panic.
func TestParse_NeverPanics(t *testing.T) {
	for _, name := range fixtureNames(t) {
		raw := readFixture(t, name)
		for i := 0; i <= len(raw); i++ {
			mustNotPanic(t, name, raw[:i])
		}
	}
	pathological := []string{
		":",
		"\r\n",
		"From:\r\n\r\n",
		"Content-Type: multipart/mixed; boundary=\"\"\r\n\r\n--\r\n--\r\n",
		"Content-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\n--x\r\n--x--\r\n",
		"Content-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\n",
		"Content-Type: text/plain; charset=\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n=\r\n=4\r\n=",
		"Content-Type: ;;;==\r\nContent-Disposition: attachment; filename*=UTF-8''%ZZ\r\n\r\nx",
		"Subject: =?\r\nFrom: <>\r\nTo: ,,,\r\nDate: \r\n\r\n",
		"Subject: =?UTF-8?B?\r\n\r\n",
		strings.Repeat("Content-Type: multipart/mixed; boundary=a\r\n\r\n--a\r\n", 200),
	}
	for _, p := range pathological {
		mustNotPanic(t, "pathologique", []byte(p))
	}
}

func mustNotPanic(t *testing.T, label string, raw []byte) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s : panic sur %q : %v", label, raw, r)
		}
	}()
	m, _ := Parse(raw)
	_ = m.PlainText()
}

// fixtureNames liste les .eml de testdata (et seulement eux : testdata/fuzz
// contient le corpus du fuzzer, s'il existe).
func fixtureNames(tb testing.TB) []string {
	tb.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "*.eml"))
	if err != nil || len(paths) == 0 {
		tb.Fatalf("aucune fixture .eml trouvée (err = %v)", err)
	}
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = filepath.Base(p)
	}
	return names
}

func FuzzParse(f *testing.F) {
	for _, name := range fixtureNames(f) {
		raw, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		m, err := Parse(raw)
		if err != nil {
			return
		}
		_ = m.PlainText()
		for _, a := range m.Attachments {
			if a.Size != len(a.Data) {
				t.Errorf("Size = %d, len(Data) = %d", a.Size, len(a.Data))
			}
		}
	})
}

func TestPlainText_WithAttachments(t *testing.T) {
	m := parseFixture(t, "mixed_pdf.eml")
	out := m.PlainText()

	ordered := []string{
		"De : Fournisseur SA <factures@fournisseur.example>",
		"À : Julien Martin <julien@example.com>",
		"Date : ",
		"Objet : Votre facture de septembre",
		"Bonjour à tous,\nLe fichier est joint.",
		"Pièces jointes",
		"facture-2026-09.pdf",
	}
	pos := 0
	for _, want := range ordered {
		i := strings.Index(out[pos:], want)
		if i < 0 {
			t.Fatalf("PlainText ne contient pas %q après la position %d :\n%s", want, pos, out)
		}
		pos += i + len(want)
	}
	if strings.Contains(out, "<p>") {
		t.Errorf("PlainText doit préférer Text au HTML quand Text existe :\n%s", out)
	}
}

func TestPlainText_CcAndNoAttachments(t *testing.T) {
	out := parseFixture(t, "simple_utf8.eml").PlainText()
	for _, want := range []string{
		"De : Jean Dupont <jean.dupont@example.fr>",
		"À : Marie Curie <marie@example.com>, paul@example.com",
		"Cc : Service Comptabilité <compta@example.fr>",
		"Objet : Réunion de lundi",
		"salle Été.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("PlainText ne contient pas %q :\n%s", want, out)
		}
	}
	if strings.Contains(out, "Pièces jointes") {
		t.Errorf("PlainText mentionne des pièces jointes alors qu'il n'y en a pas :\n%s", out)
	}
}

func TestPlainText_HTMLOnly(t *testing.T) {
	m := parseFixture(t, "html_only.eml")
	if m.Text != "" {
		t.Fatalf("Text = %q, want empty (fixture HTML seul)", m.Text)
	}
	out := m.PlainText()
	for _, want := range []string{
		"Objet : Votre relevé est disponible",
		"Votre relevé de septembre est disponible.",
		"Solde : 1 234,56 €",
		"Débit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("PlainText ne contient pas %q :\n%s", want, out)
		}
	}
	// "<" seul ne suffit pas : l'en-tête "De : Nom <adresse>" en contient
	// légitimement. On cherche des fragments de balises.
	for _, bad := range []string{"<p", "<div", "<br", "</", "<table", "<!DOCTYPE", "<html", "trackOpen", "color: red", "&eacute;", "&nbsp;", "Relevé\n"} {
		if strings.Contains(out, bad) {
			t.Errorf("PlainText contient %q :\n%s", bad, out)
		}
	}
}

func TestPlainText_AttachmentsListed(t *testing.T) {
	out := parseFixture(t, "attachment_names.eml").PlainText()
	for _, want := range []string{"Pièces jointes", "facture n° 12.pdf", "résumé.txt", "rapport annuel.xlsx", "devis final.docx"} {
		if !strings.Contains(out, want) {
			t.Errorf("PlainText ne contient pas %q :\n%s", want, out)
		}
	}
}

func TestHTMLToText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"vide", "", ""},
		{"texte brut", "Bonjour", "Bonjour"},
		{"br", "Ligne 1<br>Ligne 2<BR/>Ligne 3<br />Ligne 4", "Ligne 1\nLigne 2\nLigne 3\nLigne 4"},
		{"paragraphes", "<p>Un</p><p>Deux</p>", "Un\n\nDeux"},
		{"div", "<div>A</div><div>B</div>", "A\nB"},
		{"liste", "<ul><li>Un</li><li>Deux</li></ul>", "Un\nDeux"},
		{"tableau", "<table><tr><td>Débit</td><td>12,00</td></tr><tr><td>Crédit</td><td>40,00</td></tr></table>", "Débit 12,00\nCrédit 40,00"},
		{"entités", "&eacute;t&eacute; &amp; 5&#8364; &lt;b&gt; &quot;x&quot; &#x41;", "été & 5€ <b> \"x\" A"},
		{"nbsp", "1&nbsp;234,56", "1 234,56"},
		{"script et style", "<style>p { color: red; }</style><script type=\"text/javascript\">alert('x');</script>Texte", "Texte"},
		{"majuscules", "<SCRIPT>alert(1)</SCRIPT><Style>b{}</STYLE>Texte", "Texte"},
		{"script non fermé", "Avant<script>alert(1)", "Avant"},
		{"head", "<html><head><title>Titre</title><meta charset=utf-8></head><body>Corps</body></html>", "Corps"},
		{"commentaire", "<!-- secret -->visible<!--[if mso]>x<![endif]-->", "visible"},
		{"espaces du source", "<p>Un\n   texte\ttabulé</p>", "Un texte tabulé"},
		{"lignes vides multiples", "a<br><br><br><br><br>b", "a\n\nb"},
		{"chevrons littéraux", "a < b et c > d", "a < b et c > d"},
		{"attributs avec >", `<a href="x" title="a>b">lien</a>`, "lien"},
		{"titre", "<h1>Titre</h1>Texte", "Titre\nTexte"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HTMLToText(c.in); got != c.want {
				t.Errorf("HTMLToText(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
