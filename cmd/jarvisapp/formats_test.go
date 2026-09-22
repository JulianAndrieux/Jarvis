package main

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

// --- Jalon 25 : tous les types de fichiers ---

// seedFile enregistre un job terminé avec son fichier original (et les
// fichiers dérivés fournis), comme après un traitement réel.
func seedFile(t *testing.T, store *webapp.FakeStore, id, filename, format, mime string, original []byte, derived map[webapp.FileName][]byte) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.Create(ctx, webapp.Job{ID: id, Filename: filename, Format: format, MIME: mime, Size: int64(len(original)),
		Content: original, Status: webapp.StatusDone, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for name, data := range derived {
		if err := store.WriteFile(ctx, id, name, data); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSubmit_MultipleFilesInOneUpload(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, name := range []string{"a.pdf", "b.docx", "c.xlsx"} {
		part, _ := w.CreateFormFile("file", name)
		part.Write([]byte("contenu " + name))
	}
	w.Close()
	req := httptest.NewRequest(http.MethodPost, "/jobs", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	for _, name := range []string{"a.pdf", "b.docx", "c.xlsx"} {
		if !strings.Contains(rec.Body.String(), name) {
			t.Errorf("response has no card for %s", name)
		}
	}
	jobs, _ := store.List(context.Background(), webapp.ListQuery{})
	if len(jobs) != 3 {
		t.Fatalf("stored %d jobs, want 3", len(jobs))
	}
	formats := map[string]string{}
	for _, j := range jobs {
		formats[j.Filename] = j.Format
	}
	if formats["b.docx"] != "word" || formats["c.xlsx"] != "sheet" || formats["a.pdf"] != "pdf" {
		t.Errorf("detected formats = %v", formats)
	}
}

func TestDownloadOriginal_KeepsNameAndType(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	seedFile(t, store, "d1", "Budget été 2026.xlsx", "sheet", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", []byte("xlsx-bytes"), nil)

	rec := get(t, s, "/documents/d1/original")
	if rec.Code != http.StatusOK || rec.Body.String() != "xlsx-bytes" {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Errorf("Content-Type = %q", ct)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "filename*=UTF-8''Budget%20%C3%A9t%C3%A9%202026.xlsx") {
		t.Errorf("Content-Disposition = %q, want an attachment with the UTF-8 file name", cd)
	}
}

func TestPDF_ServesRenditionForConvertedFilesAnd404ForStoredOnly(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	seedFile(t, store, "w1", "lettre.docx", "word", "x", []byte("docx"), map[webapp.FileName][]byte{webapp.FileRendition: []byte("%PDF-rendition")})
	seedFile(t, store, "z1", "photos.zip", "other", "application/zip", []byte("PK"), nil)

	if rec := get(t, s, "/documents/w1/pdf"); rec.Body.String() != "%PDF-rendition" || rec.Header().Get("Content-Type") != "application/pdf" {
		t.Errorf("docx /pdf = %q (%s), want the rendition", rec.Body.String(), rec.Header().Get("Content-Type"))
	}
	if rec := get(t, s, "/documents/z1/pdf"); rec.Code != http.StatusNotFound {
		t.Errorf("zip /pdf status = %d, want 404", rec.Code)
	}
}

// Les aperçus natifs affichent du contenu fourni par l'utilisateur (HTML
// d'une page web, d'un e-mail, export d'un tableur) : servis avec une
// politique stricte — aucun script, aucune ressource distante (pas de
// pixel de suivi), isolés dans un bac à sable.
func assertSandboxed(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "sandbox"} {
		if !strings.Contains(csp, want) {
			t.Errorf("Content-Security-Policy = %q, want it to contain %q", csp, want)
		}
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing X-Content-Type-Options: nosniff")
	}
}

func TestView_CSVAsTable(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	csv := []byte("Date;Libell\xe9;Montant\n05/09/2026;Loyer;-1 250,00\n08/09/2026;<script>alert(1)</script>;3 420,15\n")
	seedFile(t, store, "c1", "releve.csv", "csv", "text/csv", csv, nil)

	rec := get(t, s, "/documents/c1/view")
	body := rec.Body.String()
	for _, want := range []string{"<th>Libellé</th>", "<td>-1 250,00</td>", "&lt;script&gt;"} {
		if !strings.Contains(body, want) {
			t.Errorf("CSV view does not contain %q: %s", want, body)
		}
	}
	assertSandboxed(t, rec)
}

func TestView_TextEscapedInPre(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	seedFile(t, store, "t1", "notes.txt", "text", "text/plain", []byte("Caf\xe9 <b>cr\xe8me</b>\n"), nil)

	rec := get(t, s, "/documents/t1/view")
	if !strings.Contains(rec.Body.String(), "Café &lt;b&gt;crème&lt;/b&gt;") {
		t.Errorf("text view = %s", rec.Body.String())
	}
	assertSandboxed(t, rec)
}

func TestView_SheetServesHTMLPreviewSandboxed(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	seedFile(t, store, "x1", "budget.xlsx", "sheet", "x", []byte("xlsx"), map[webapp.FileName][]byte{webapp.FilePreview: []byte("<html><body><table><tr><td>Loyer</td></tr></table></body></html>")})

	rec := get(t, s, "/documents/x1/view")
	if !strings.Contains(rec.Body.String(), "<td>Loyer</td>") {
		t.Errorf("sheet view = %s", rec.Body.String())
	}
	assertSandboxed(t, rec)
}

func TestView_HTMLPageSandboxed(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	seedFile(t, store, "h1", "page.html", "html", "text/html", []byte(`<html><body><h1>Bonjour</h1><img src="https://tracker.example/p.gif"></body></html>`), nil)

	rec := get(t, s, "/documents/h1/view")
	if !strings.Contains(rec.Body.String(), "<h1>Bonjour</h1>") {
		t.Errorf("html view = %s", rec.Body.String())
	}
	assertSandboxed(t, rec)
}

func TestImage_BrowserNativeServedAsIsHEICThroughPreview(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	seedFile(t, store, "p1", "photo.png", "image", "image/png", []byte("\x89PNG-original"), nil)
	seedFile(t, store, "p2", "iphone.heic", "image", "image/heic", []byte("heic"), map[webapp.FileName][]byte{webapp.FilePreview: []byte("jpeg-preview")})

	if rec := get(t, s, "/documents/p1/image"); rec.Body.String() != "\x89PNG-original" || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("png image = %q (%s)", rec.Body.String(), rec.Header().Get("Content-Type"))
	}
	if rec := get(t, s, "/documents/p2/image"); rec.Body.String() != "jpeg-preview" || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("heic image = %q (%s), want the JPEG preview", rec.Body.String(), rec.Header().Get("Content-Type"))
	}
}

// Chaque famille a l'aperçu qui lui convient à gauche de la vue détail,
// et toujours un lien de téléchargement de l'original.
func TestDetail_PreviewPaneByFamily(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	seedFile(t, store, "w1", "lettre.docx", "word", "x", []byte("docx"), map[webapp.FileName][]byte{webapp.FileRendition: []byte("%PDF")})
	seedFile(t, store, "x1", "budget.xlsx", "sheet", "x", []byte("xlsx"), map[webapp.FileName][]byte{webapp.FileRendition: []byte("%PDF"), webapp.FilePreview: []byte("<table>")})
	seedFile(t, store, "p1", "photo.jpg", "image", "image/jpeg", []byte("jpg"), nil)
	seedFile(t, store, "z1", "photos.zip", "other", "application/zip", []byte("PK"), nil)

	cases := []struct{ id, want, notWant string }{
		{"w1", `src="/documents/w1/pdf`, `src="/documents/w1/view"`},
		{"x1", `src="/documents/x1/view"`, ""},
		{"p1", `<img class="preview-image" src="/documents/p1/image"`, "<iframe"},
		{"z1", "Aperçu indisponible", "<iframe"},
	}
	for _, c := range cases {
		body := get(t, s, "/documents/"+c.id).Body.String()
		if !strings.Contains(body, c.want) {
			t.Errorf("%s detail does not contain %q", c.id, c.want)
		}
		if c.notWant != "" && strings.Contains(body, c.notWant) {
			t.Errorf("%s detail should not contain %q", c.id, c.notWant)
		}
		if !strings.Contains(body, `href="/documents/`+c.id+`/original"`) {
			t.Errorf("%s detail has no download link", c.id)
		}
	}
	if body := get(t, s, "/documents/x1").Body.String(); !strings.Contains(body, `href="/documents/x1/pdf"`) {
		t.Error("sheet detail should also offer the PDF version")
	}
}

func TestDocuments_TypeBadgeIconForStoredOnlyAndFilter(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	seedFile(t, store, "x1", "budget.xlsx", "sheet", "x", []byte("xlsx"), nil)
	seedFile(t, store, "z1", "photos.zip", "other", "application/zip", []byte("PK"), nil)

	body := get(t, s, "/documents").Body.String()
	if !strings.Contains(body, ">XLSX<") || !strings.Contains(body, ">ZIP<") {
		t.Errorf("grid has no extension badges: %s", body)
	}
	if strings.Contains(body, `src="/documents/z1/thumbnail"`) {
		t.Error("a stored-only file has no thumbnail: the grid should show an icon instead")
	}

	filtered := get(t, s, "/documents?type=sheet").Body.String()
	if !strings.Contains(filtered, "budget.xlsx") || strings.Contains(filtered, "photos.zip") {
		t.Errorf("?type=sheet should list only spreadsheets")
	}
}

func TestThumbnail_StoredOnlyIs404(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	s.Jobs.Renderer = fakePNGRenderer{png: []byte("x")}
	seedFile(t, store, "z1", "photos.zip", "other", "application/zip", []byte("PK"), nil)
	if rec := get(t, s, "/documents/z1/thumbnail"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// Trouvé sur capture : l'aperçu déclarait "color-scheme: light dark" dans
// une iframe à fond blanc — en mode sombre, texte clair sur blanc, donc
// invisible (cellules d'un CSV, en-têtes d'un e-mail). Un aperçu est un
// document : couleurs claires imposées, comme une page imprimée.
func TestView_ForcesLightDocumentColors(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	seedFile(t, store, "c1", "releve.csv", "csv", "text/csv", []byte("a;b\n1;2\n"), nil)
	body := get(t, s, "/documents/c1/view").Body.String()
	if strings.Contains(body, "light dark") || !strings.Contains(body, "color-scheme: light;") || !strings.Contains(body, "background: #fff") {
		t.Errorf("view style must force light colours: %s", body[:strings.Index(body, "</style>")])
	}
}

// Les images distantes d'un e-mail sont bloquées par la CSP (pas de
// pixel de suivi) : masquées plutôt qu'affichées comme images cassées.
func TestView_EmailHidesBlockedRemoteImages(t *testing.T) {
	s, store := newTestServer(t, &blockingRunner{})
	eml := "From: a@example.org\r\nSubject: Test\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Bonjour</p><img src=\"https://tracker.example/p.gif\">"
	seedFile(t, store, "m1", "m.eml", "email", "message/rfc822", []byte(eml), nil)
	body := get(t, s, "/documents/m1/view").Body.String()
	if !strings.Contains(body, `img:not([src^="data:"])`) {
		t.Errorf("email view should hide remote images: %s", body)
	}
}
