package formats

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/textview"
)

// LocalConverter convertit localement, sans rien envoyer hors de la
// machine : LibreOffice (soffice) pour les documents bureautiques, le
// texte et les tableaux, sips (macOS) pour les images.
type LocalConverter struct {
	// Soffice et Sips surchargent le chemin des binaires ("" : "soffice",
	// "sips" sur le PATH).
	Soffice string
	Sips    string
	// ProfileDir est le profil LibreOffice dédié à Jarvis ("" : un
	// dossier "jarvis-libreoffice" dans le répertoire temporaire). Sans
	// profil dédié, une conversion échoue silencieusement si l'utilisateur
	// a LibreOffice ouvert en même temps (profil verrouillé).
	ProfileDir string
	// Timeout borne une conversion (0 : 3 minutes) — LibreOffice peut se
	// bloquer sur un fichier corrompu.
	Timeout time.Duration

	// mu : une seule instance de LibreOffice à la fois par profil.
	mu sync.Mutex
}

// imageLongSide et imageDPI normalisent une image en page A4 : 2400 px de
// long à 205 DPI font 11,7 pouces, la hauteur d'une A4. Sans cela, sips
// produit une page à la taille de l'image à 72 DPI (une photo de 4000 px
// = une page de 1,4 m), que le pipeline rendrait ensuite à 200 DPI.
const (
	imageLongSide = "2400"
	imageDPI      = "205"
)

// Convert produit la version PDF (et l'aperçu natif éventuel) de srcPath.
func (c *LocalConverter) Convert(ctx context.Context, f Format, srcPath string) (Rendition, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	work, err := os.MkdirTemp("", "jarvis-convert-*")
	if err != nil {
		return Rendition{}, fmt.Errorf("formats: temp dir: %w", err)
	}
	defer os.RemoveAll(work)

	switch f.Family {
	case Word, Slides:
		pdf, err := c.soffice(ctx, work, srcPath, "pdf", "")
		return Rendition{PDF: pdf}, err
	case Sheet:
		pdf, err := c.soffice(ctx, work, srcPath, "pdf", "")
		if err != nil {
			return Rendition{}, err
		}
		// Aperçu : toutes les feuilles en HTML, bien plus lisible qu'un
		// PDF paginé. Facultatif : un échec ne prive pas du PDF.
		preview, err := c.soffice(ctx, work, srcPath, "html", "")
		if err != nil {
			return Rendition{PDF: pdf}, nil
		}
		return Rendition{PDF: pdf, Preview: preview, PreviewMIME: "text/html"}, nil
	case CSV:
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return Rendition{}, fmt.Errorf("formats: read %s: %w", srcPath, err)
		}
		table, err := textview.ParseCSV(data, 5000)
		if err != nil {
			return Rendition{}, fmt.Errorf("formats: csv: %w", err)
		}
		pdf, err := c.htmlToPDF(ctx, work, tableHTML(filepath.Base(srcPath), table))
		return Rendition{PDF: pdf}, err
	case Text:
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return Rendition{}, fmt.Errorf("formats: read %s: %w", srcPath, err)
		}
		text, _ := textview.Decode(data)
		pdf, err := c.textToPDF(ctx, work, text)
		return Rendition{PDF: pdf}, err
	case HTML, Email:
		return c.convertMarkup(ctx, work, f, srcPath)
	case Image:
		return c.convertImage(ctx, work, f, srcPath)
	default:
		return Rendition{}, fmt.Errorf("formats: nothing to convert for %s files", f.Family)
	}
}

// soffice convertit src vers le format to dans work et retourne le
// fichier produit. infilter force le filtre d'import ("" : choisi par
// LibreOffice d'après l'extension).
func (c *LocalConverter) soffice(ctx context.Context, work, src, to, infilter string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	bin := c.Soffice
	if bin == "" {
		bin = "soffice"
	}
	profile := c.ProfileDir
	if profile == "" {
		profile = filepath.Join(os.TempDir(), "jarvis-libreoffice")
	}
	outDir := filepath.Join(work, "out-"+to)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("formats: out dir: %w", err)
	}

	args := []string{"-env:UserInstallation=file://" + filepath.ToSlash(profile), "--headless", "--norestore", "--nolockcheck"}
	if infilter != "" {
		args = append(args, "--infilter="+infilter)
	}
	args = append(args, "--convert-to", to, "--outdir", outDir, src)
	var output bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr = &output, &output
	runErr := cmd.Run()

	// LibreOffice sort souvent en 0 même quand la conversion échoue
	// ("Error: source file could not be loaded") : c'est la présence du
	// fichier produit qui fait foi.
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	out := filepath.Join(outDir, base+"."+to)
	data, err := os.ReadFile(out)
	if err == nil && len(data) > 0 {
		return data, nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("formats: libreoffice: timeout converting %s", filepath.Base(src))
	}
	detail := lastLines(output.String(), 3)
	if runErr != nil {
		return nil, fmt.Errorf("formats: libreoffice %s -> %s: %w: %s", filepath.Base(src), to, runErr, detail)
	}
	return nil, fmt.Errorf("formats: libreoffice produced no %s for %s: %s", to, filepath.Base(src), detail)
}

// textToPDF met du texte (déjà en UTF-8) en page. L'import "Text
// (encoded)" en UTF-8 évite que LibreOffice ne devine un autre encodage.
func (c *LocalConverter) textToPDF(ctx context.Context, work, text string) ([]byte, error) {
	src := filepath.Join(work, "texte.txt")
	if err := os.WriteFile(src, []byte(text), 0o644); err != nil {
		return nil, fmt.Errorf("formats: write text: %w", err)
	}
	return c.soffice(ctx, work, src, "pdf", "Text (encoded):UTF8")
}

// htmlToPDF met en page du HTML produit par Jarvis (jamais du HTML
// fourni par l'utilisateur : il pourrait référencer des ressources
// distantes que LibreOffice irait chercher). Import "HTML (StarWriter)"
// imposé : sinon LibreOffice ouvre le fichier en Writer/Web, sans export
// PDF.
func (c *LocalConverter) htmlToPDF(ctx context.Context, work, page string) ([]byte, error) {
	src := filepath.Join(work, "page.html")
	if err := os.WriteFile(src, []byte(page), 0o644); err != nil {
		return nil, fmt.Errorf("formats: write html: %w", err)
	}
	return c.soffice(ctx, work, src, "pdf", "HTML (StarWriter)")
}

// convertImage normalise l'image en JPEG de taille A4 (voir imageDPI),
// puis en fait un PDF d'une page sans couche texte — le pipeline
// l'enverra au VLM comme un scan. Le JPEG sert aussi d'aperçu pour les
// formats que les navigateurs n'affichent pas (HEIC, TIFF, BMP).
func (c *LocalConverter) convertImage(ctx context.Context, work string, f Format, src string) (Rendition, error) {
	bin := c.Sips
	if bin == "" {
		bin = "sips"
	}
	jpg := filepath.Join(work, "image.jpg")
	if err := run(ctx, bin, "-s", "format", "jpeg", "-s", "formatOptions", "90", "-Z", imageLongSide,
		"-s", "dpiWidth", imageDPI, "-s", "dpiHeight", imageDPI, src, "--out", jpg); err != nil {
		return Rendition{}, fmt.Errorf("formats: sips jpeg: %w", err)
	}
	pdfPath := filepath.Join(work, "image.pdf")
	if err := run(ctx, bin, "-s", "format", "pdf", jpg, "--out", pdfPath); err != nil {
		return Rendition{}, fmt.Errorf("formats: sips pdf: %w", err)
	}
	pdf, err := os.ReadFile(pdfPath)
	if err != nil {
		return Rendition{}, fmt.Errorf("formats: read image pdf: %w", err)
	}
	r := Rendition{PDF: pdf}
	if !BrowserDisplayable(f.MIME) {
		if preview, err := os.ReadFile(jpg); err == nil {
			r.Preview, r.PreviewMIME = preview, "image/jpeg"
		}
	}
	return r, nil
}

// BrowserDisplayable indique si un navigateur affiche ce type d'image
// directement (sinon l'aperçu passe par le JPEG de la conversion).
func BrowserDisplayable(mime string) bool {
	switch mime {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	}
	return false
}

func run(ctx context.Context, bin string, args ...string) error {
	var output bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", bin, err, lastLines(output.String(), 3))
	}
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// tableHTML met un tableau CSV en page HTML (texte échappé).
func tableHTML(title string, t textview.Table) string {
	var b strings.Builder
	b.WriteString(`<html><head><meta charset="utf-8"><title>`)
	b.WriteString(html.EscapeString(title))
	b.WriteString(`</title></head><body><table border="1" cellpadding="3" style="border-collapse:collapse;font-family:sans-serif;font-size:9pt">`)
	b.WriteString("<tr>")
	for _, h := range t.Header {
		b.WriteString("<th>" + html.EscapeString(h) + "</th>")
	}
	b.WriteString("</tr>")
	for _, row := range t.Rows {
		b.WriteString("<tr>")
		for _, cell := range row {
			b.WriteString("<td>" + html.EscapeString(cell) + "</td>")
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</table>")
	if t.Truncated {
		fmt.Fprintf(&b, "<p><i>%d lignes au total — seules les %d premières sont reprises ici.</i></p>", t.TotalRows, len(t.Rows))
	}
	b.WriteString("</body></html>")
	return b.String()
}
