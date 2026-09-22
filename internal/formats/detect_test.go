package formats

import "testing"

func TestDetect_ByExtension(t *testing.T) {
	cases := []struct {
		name   string
		family Family
		mime   string
	}{
		{"Facture.PDF", PDF, "application/pdf"},
		{"note.docx", Word, "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"vieux.doc", Word, "application/msword"},
		{"lettre.odt", Word, "application/vnd.oasis.opendocument.text"},
		{"contrat.rtf", Word, "application/rtf"},
		{"deck.pptx", Slides, "application/vnd.openxmlformats-officedocument.presentationml.presentation"},
		{"vieux.ppt", Slides, "application/vnd.ms-powerpoint"},
		{"pres.odp", Slides, "application/vnd.oasis.opendocument.presentation"},
		{"budget.xlsx", Sheet, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"macro.xlsm", Sheet, "application/vnd.ms-excel.sheet.macroEnabled.12"},
		{"binaire.xlsb", Sheet, "application/vnd.ms-excel.sheet.binary.macroEnabled.12"},
		{"vieux.xls", Sheet, "application/vnd.ms-excel"},
		{"calc.ods", Sheet, "application/vnd.oasis.opendocument.spreadsheet"},
		{"export.csv", CSV, "text/csv"},
		{"export.tsv", CSV, "text/tab-separated-values"},
		{"notes.txt", Text, "text/plain"},
		{"README.md", Text, "text/markdown"},
		{"config.json", Text, "application/json"},
		{"page.html", HTML, "text/html"},
		{"photo.JPG", Image, "image/jpeg"},
		{"iphone.heic", Image, "image/heic"},
		{"scan.tiff", Image, "image/tiff"},
		{"capture.png", Image, "image/png"},
		{"message.eml", Email, "message/rfc822"},
		{"archive.zip", Other, "application/zip"},
		{"installeur.dmg", Other, "application/x-apple-diskimage"},
	}
	for _, c := range cases {
		f := Detect(c.name, nil)
		if f.Family != c.family || f.MIME != c.mime {
			t.Errorf("Detect(%q) = %s %q, want %s %q", c.name, f.Family, f.MIME, c.family, c.mime)
		}
	}
}

// Le contenu l'emporte sur une extension absente ou trompeuse pour les
// cas sans ambiguïté : un PDF reste un PDF, du texte sans extension est
// du texte, un binaire inconnu est stocké tel quel.
func TestDetect_BySniffingWhenExtensionIsMissingOrUnknown(t *testing.T) {
	cases := []struct {
		name   string
		head   []byte
		family Family
	}{
		{"scan_sans_extension", []byte("%PDF-1.7\n%\xe2\xe3"), PDF},
		{"document.bin", []byte("%PDF-1.4\n"), PDF},
		{"LISEZMOI", []byte("Bonjour,\nceci est un simple texte.\n"), Text},
		{"donnees.dat", []byte{0x00, 0x01, 0x02, 0xff, 0x00}, Other},
		{"photo", []byte("\x89PNG\r\n\x1a\n\x00\x00"), Image},
		{"photo2", []byte("\xff\xd8\xff\xe0\x00\x10JFIF"), Image},
	}
	for _, c := range cases {
		if got := Detect(c.name, c.head).Family; got != c.family {
			t.Errorf("Detect(%q, %q...) = %s, want %s", c.name, c.head[:4], got, c.family)
		}
	}
}

func TestFamily_Capabilities(t *testing.T) {
	cases := []struct {
		f         Family
		rendition bool // produit une version PDF (aperçu, miniature, pipeline)
		pipeline  bool // passe par classification + extraction
	}{
		{PDF, false, true},
		{Word, true, true},
		{Slides, true, true},
		{Sheet, true, true},
		{CSV, true, true},
		{Text, true, true},
		{HTML, true, true},
		{Image, true, true},
		{Email, true, true},
		{Other, false, false},
	}
	for _, c := range cases {
		if c.f.NeedsRendition() != c.rendition || c.f.Pipeline() != c.pipeline {
			t.Errorf("%s: NeedsRendition=%v Pipeline=%v, want %v %v", c.f, c.f.NeedsRendition(), c.f.Pipeline(), c.rendition, c.pipeline)
		}
	}
}

func TestFamily_Label(t *testing.T) {
	if Sheet.Label() != "Tableur" || Other.Label() != "Fichier" {
		t.Errorf("labels = %q %q", Sheet.Label(), Other.Label())
	}
}

func TestDetect_Windows1252TextWithoutExtensionIsText(t *testing.T) {
	head := []byte("Caf\xe9 cr\xe8me \x80 12,50\r\nLigne 2\r\n")
	if got := Detect("MEMO", head).Family; got != Text {
		t.Errorf("Detect(windows-1252 text) = %s, want text", got)
	}
}
