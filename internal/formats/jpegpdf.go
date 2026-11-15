package formats

import (
	"bytes"
	"fmt"
	"image/color"
	"image/jpeg"
)

// jpegToPDF enveloppe un JPEG dans un PDF d'une page, sans le réencoder
// (filtre DCTDecode : les octets du JPEG sont le flux de l'image). La page
// a la taille physique de l'image à dpi points par pouce. Sur macOS, sips
// fait ce travail ; ailleurs (instance hébergée sous Linux), aucun outil
// courant ne le fait sans dépendance lourde ni politique de sécurité à
// lever (ImageMagick refuse d'écrire du PDF sous Debian) — et le format
// tient en cinq objets.
func jpegToPDF(data []byte, dpi float64) ([]byte, error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("formats: lecture du JPEG : %w", err)
	}
	var space string
	switch cfg.ColorModel {
	case color.GrayModel:
		space = "/DeviceGray"
	case color.YCbCrModel:
		space = "/DeviceRGB"
	default:
		// CMJN : les composantes d'un JPEG Adobe sont inversées, un
		// lecteur l'afficherait en négatif. L'appelant normalise en sRGB.
		return nil, fmt.Errorf("formats: JPEG ni gris ni RVB (espace de couleur non géré)")
	}
	if dpi <= 0 {
		dpi = 72
	}
	w := float64(cfg.Width) * 72 / dpi
	h := float64(cfg.Height) * 72 / dpi
	content := fmt.Sprintf("q %.2f 0 0 %.2f 0 0 cm /Im0 Do Q", w, h)

	var out bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	out.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /XObject << /Im0 4 0 R >> >> /Contents 5 0 R >>", w, h))
	offsets = append(offsets, out.Len())
	fmt.Fprintf(&out, "4 0 obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace %s /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n",
		cfg.Width, cfg.Height, space, len(data))
	out.Write(data)
	out.WriteString("\nendstream\nendobj\n")
	obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))

	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, off := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return out.Bytes(), nil
}
