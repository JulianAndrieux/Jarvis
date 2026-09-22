package main

import (
	"errors"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/email"
	"github.com/JulianAndrieux/Jarvis/internal/formats"
	"github.com/JulianAndrieux/Jarvis/internal/textview"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

// Fichiers et aperçus des documents de tous types (jalon 25).

const (
	// maxUploadFileSize borne un fichier déposé (512 Mio) ; le formulaire
	// entier est borné à maxUploadTotal.
	maxUploadFileSize = 512 << 20
	maxUploadTotal    = 2 << 30
	// maxViewRows / maxViewText bornent un aperçu natif (CSV, texte) : un
	// export de plusieurs centaines de Mo ne doit pas partir tel quel au
	// navigateur — le fichier complet reste téléchargeable.
	maxViewRows = 2000
	maxViewText = 2 << 20
)

// sandboxCSP isole un aperçu natif, dont le contenu vient de
// l'utilisateur (page web, e-mail, export de tableur) : aucun script,
// aucune ressource distante (pas de pixel de suivi d'un e-mail), styles
// et images intégrés seulement, et le document est traité comme une
// origine opaque (directive sandbox). L'iframe qui l'affiche porte en
// plus l'attribut sandbox.
const sandboxCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:; sandbox"

// fileJob charge le job de la route et répond 404/500 lui-même en cas
// d'échec.
func (s *Server) fileJob(w http.ResponseWriter, r *http.Request) (webapp.Job, bool) {
	id := chi.URLParam(r, "id")
	job, ok, err := s.Jobs.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "erreur de lecture du document : "+err.Error(), http.StatusInternalServerError)
		return webapp.Job{}, false
	}
	if !ok {
		http.NotFound(w, r)
		return webapp.Job{}, false
	}
	return job, true
}

func (s *Server) readFile(w http.ResponseWriter, r *http.Request, id string, name webapp.FileName) ([]byte, bool) {
	data, ok, err := s.Jobs.ReadFile(r.Context(), id, name)
	if err != nil {
		http.Error(w, "erreur de lecture du fichier : "+err.Error(), http.StatusInternalServerError)
		return nil, false
	}
	if !ok {
		http.NotFound(w, r)
		return nil, false
	}
	return data, true
}

func familyOf(job webapp.Job) formats.Family {
	if job.Format == "" {
		return formats.PDF
	}
	return formats.Family(job.Format)
}

// handleDocumentOriginal renvoie le fichier tel que déposé, en
// téléchargement, avec son nom (RFC 6266/5987 pour les accents).
func (s *Server) handleDocumentOriginal(w http.ResponseWriter, r *http.Request) {
	job, ok := s.fileJob(w, r)
	if !ok {
		return
	}
	data, ok := s.readFile(w, r, job.ID, webapp.FileOriginal)
	if !ok {
		return
	}
	mime := job.MIME
	if mime == "" {
		mime = "application/pdf"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", contentDisposition("attachment", job.Filename))
	w.Write(data)
}

func contentDisposition(kind, filename string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, filename)
	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, kind, ascii, strings.ReplaceAll(url.PathEscape(filename), "+", "%2B"))
}

// handleDocumentPDF sert la version PDF : l'original d'un PDF, la
// conversion d'un autre document ; 404 pour un fichier seulement stocké
// ou pas encore converti.
func (s *Server) handleDocumentPDF(w http.ResponseWriter, r *http.Request) {
	job, ok := s.fileJob(w, r)
	if !ok {
		return
	}
	fam := familyOf(job)
	name := webapp.FileOriginal
	switch {
	case !fam.Pipeline():
		http.NotFound(w, r)
		return
	case fam.NeedsRendition():
		name = webapp.FileRendition
	}
	data, ok := s.readFile(w, r, job.ID, name)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", contentDisposition("inline", strings.TrimSuffix(job.Filename, fileExt(job.Filename))+".pdf"))
	w.Write(data)
}

// handleDocumentImage sert l'image d'un document image : l'original si
// le navigateur sait l'afficher, sinon le JPEG de la conversion (HEIC,
// TIFF, BMP).
func (s *Server) handleDocumentImage(w http.ResponseWriter, r *http.Request) {
	job, ok := s.fileJob(w, r)
	if !ok {
		return
	}
	if familyOf(job) != formats.Image {
		http.NotFound(w, r)
		return
	}
	name, mime := webapp.FileOriginal, job.MIME
	if !formats.BrowserDisplayable(job.MIME) {
		name, mime = webapp.FilePreview, "image/jpeg"
	}
	data, ok := s.readFile(w, r, job.ID, name)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(data)
}

// handleDocumentView sert l'aperçu natif d'un document (tableur, CSV,
// texte, page web, e-mail), isolé par sandboxCSP.
func (s *Server) handleDocumentView(w http.ResponseWriter, r *http.Request) {
	job, ok := s.fileJob(w, r)
	if !ok {
		return
	}
	var page string
	switch familyOf(job) {
	case formats.Sheet:
		data, ok := s.readFile(w, r, job.ID, webapp.FilePreview)
		if !ok {
			return
		}
		page = string(data)
	case formats.CSV:
		data, ok := s.readFile(w, r, job.ID, webapp.FileOriginal)
		if !ok {
			return
		}
		table, err := textview.ParseCSV(data, maxViewRows)
		if err != nil {
			http.Error(w, "CSV illisible : "+err.Error(), http.StatusUnprocessableEntity)
			return
		}
		page = csvViewHTML(table)
	case formats.Text:
		data, ok := s.readFile(w, r, job.ID, webapp.FileOriginal)
		if !ok {
			return
		}
		text, _ := textview.Decode(data)
		text, truncated := textview.Truncate(text, maxViewText)
		page = textViewHTML(text, truncated)
	case formats.HTML:
		data, ok := s.readFile(w, r, job.ID, webapp.FileOriginal)
		if !ok {
			return
		}
		page, _ = textview.Decode(data)
	case formats.Email:
		data, ok := s.readFile(w, r, job.ID, webapp.FileOriginal)
		if !ok {
			return
		}
		msg, err := email.Parse(data)
		if err != nil {
			http.Error(w, "e-mail illisible : "+err.Error(), http.StatusUnprocessableEntity)
			return
		}
		page = emailViewHTML(msg)
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", sandboxCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	io.WriteString(w, page)
}

// viewStyle : mise en forme commune des aperçus natifs. Un aperçu est un
// document : couleurs claires imposées (l'iframe est blanche ; avec
// "light dark", le mode sombre du système passait le texte en clair sur
// ce fond blanc — invisible, trouvé sur capture). Les images distantes,
// bloquées par la CSP (pixels de suivi des e-mails), sont masquées
// plutôt qu'affichées cassées.
const viewStyle = `<style>
:root { color-scheme: light; }
body { margin: 0; padding: 16px; background: #fff; color: #1d1d1f; font: 14px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
img:not([src^="data:"]) { display: none; }
pre { margin: 0; white-space: pre-wrap; word-break: break-word; font: 13px/1.55 ui-monospace, Menlo, monospace; }
table { border-collapse: collapse; font-size: 13px; }
th, td { border: 1px solid #8885; padding: 4px 8px; text-align: left; vertical-align: top; white-space: nowrap; }
th { position: sticky; top: 0; background: #f2f2f4; font-weight: 600; }
tr:nth-child(even) td { background: #8881; }
.note { color: #888; font-size: 12px; margin: 10px 0; }
.mail-head { border-bottom: 1px solid #8884; padding-bottom: 10px; margin-bottom: 14px; }
.mail-head h1 { font-size: 18px; margin: 0 0 8px; }
.mail-head dl { display: grid; grid-template-columns: max-content 1fr; gap: 2px 12px; margin: 0; font-size: 13px; }
.mail-head dt { color: #888; }
.mail-head dd { margin: 0; }
.attachments { margin-top: 16px; padding-top: 10px; border-top: 1px solid #8884; font-size: 13px; }
</style>`

func csvViewHTML(t textview.Table) string {
	var b strings.Builder
	b.WriteString("<!doctype html><html><head><meta charset=\"utf-8\">" + viewStyle + "</head><body><table><thead><tr>")
	for _, h := range t.Header {
		b.WriteString("<th>" + html.EscapeString(h) + "</th>")
	}
	b.WriteString("</tr></thead><tbody>")
	for _, row := range t.Rows {
		b.WriteString("<tr>")
		for _, cell := range row {
			b.WriteString("<td>" + html.EscapeString(cell) + "</td>")
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</tbody></table>")
	if t.Truncated {
		fmt.Fprintf(&b, `<p class="note">%d lignes au total — seules les %d premières sont affichées. Télécharge l'original pour tout voir.</p>`, t.TotalRows, len(t.Rows))
	}
	b.WriteString("</body></html>")
	return b.String()
}

func textViewHTML(text string, truncated bool) string {
	note := ""
	if truncated {
		note = `<p class="note">Fichier tronqué pour l'aperçu — télécharge l'original pour tout voir.</p>`
	}
	return "<!doctype html><html><head><meta charset=\"utf-8\">" + viewStyle + "</head><body><pre>" + html.EscapeString(text) + "</pre>" + note + "</body></html>"
}

// emailViewHTML : en-têtes, corps (HTML de l'e-mail tel quel — la CSP
// bloque scripts et images distantes — sinon le texte), pièces jointes.
func emailViewHTML(m email.Message) string {
	var b strings.Builder
	b.WriteString("<!doctype html><html><head><meta charset=\"utf-8\">" + viewStyle + "</head><body><div class=\"mail-head\">")
	b.WriteString("<h1>" + html.EscapeString(orDefault(m.Subject, "(sans objet)")) + "</h1><dl>")
	field := func(label, value string) {
		if value != "" {
			b.WriteString("<dt>" + label + "</dt><dd>" + html.EscapeString(value) + "</dd>")
		}
	}
	field("De", addresses(m.From))
	field("À", addresses(m.To))
	field("Cc", addresses(m.Cc))
	if !m.Date.IsZero() {
		field("Date", m.Date.Format("02/01/2006 15:04"))
	}
	b.WriteString("</dl></div>")
	if m.HTML != "" {
		b.WriteString(m.HTML)
	} else {
		b.WriteString("<pre>" + html.EscapeString(m.Text) + "</pre>")
	}
	if len(m.Attachments) > 0 {
		b.WriteString(`<div class="attachments"><b>Pièces jointes</b><ul>`)
		for _, a := range m.Attachments {
			fmt.Fprintf(&b, "<li>%s <span class=\"note\">(%s, %s)</span></li>", html.EscapeString(orDefault(a.Filename, "sans nom")), html.EscapeString(a.ContentType), templates.HumanSize(int64(a.Size)))
		}
		b.WriteString("</ul></div>")
	}
	b.WriteString("</body></html>")
	return b.String()
}

func addresses(list []email.Address) string {
	parts := make([]string, 0, len(list))
	for _, a := range list {
		switch {
		case a.Name != "" && a.Email != "":
			parts = append(parts, a.Name+" <"+a.Email+">")
		case a.Email != "":
			parts = append(parts, a.Email)
		default:
			parts = append(parts, a.Name)
		}
	}
	return strings.Join(parts, ", ")
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func fileExt(name string) string {
	if i := strings.LastIndex(name, "."); i > 0 {
		return name[i:]
	}
	return ""
}

// handleSubmitFiles accepte un ou plusieurs fichiers de tous types (champ
// "file" répété) et renvoie une carte de suivi par fichier.
func (s *Server) handleSubmitFiles(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadTotal)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "formulaire invalide : "+err.Error(), http.StatusBadRequest)
		return
	}
	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 {
		http.Error(w, "fichier manquant", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	for _, h := range headers {
		if h.Size > maxUploadFileSize {
			fmt.Fprintf(w, `<div class="card"><div class="error-box">%s : fichier trop volumineux (%s, maximum %s).</div></div>`,
				html.EscapeString(h.Filename), templates.HumanSize(h.Size), templates.HumanSize(maxUploadFileSize))
			continue
		}
		content, err := readUploaded(h)
		if err != nil {
			fmt.Fprintf(w, `<div class="card"><div class="error-box">%s : %s</div></div>`, html.EscapeString(h.Filename), html.EscapeString(err.Error()))
			continue
		}
		job, err := s.Jobs.Submit(r.Context(), h.Filename, content)
		if err != nil {
			fmt.Fprintf(w, `<div class="card"><div class="error-box">%s : %s</div></div>`, html.EscapeString(h.Filename), html.EscapeString(err.Error()))
			continue
		}
		s.renderJob(w, r, job)
	}
}

func readUploaded(h *multipart.FileHeader) ([]byte, error) {
	f, err := h.Open()
	if err != nil {
		return nil, fmt.Errorf("lecture impossible : %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("lecture impossible : %w", err)
	}
	return data, nil
}

// thumbnailError traduit l'absence de miniature (fichier seulement
// stocké, conversion pas encore faite) en 404 — l'interface affiche
// alors une icône — et le reste en 500. Jamais mis en cache.
func thumbnailError(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("Cache-Control", "no-store")
	if errors.Is(err, webapp.ErrNoThumbnail) {
		http.NotFound(w, r)
		return
	}
	fmt.Fprintf(os.Stderr, "jarvisapp: thumbnail: %v\n", err)
	http.Error(w, "miniature indisponible : "+err.Error(), http.StatusInternalServerError)
}
