package formats

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func testJPEG(t *testing.T, w, h int, gray bool) []byte {
	t.Helper()
	var img image.Image
	if gray {
		g := image.NewGray(image.Rect(0, 0, w, h))
		for i := range g.Pix {
			g.Pix[i] = uint8(i)
		}
		img = g
	} else {
		c := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				c.Set(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
			}
		}
		img = c
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Une page à la taille physique de l'image : 2400 px à 205 DPI font la
// hauteur d'une A4 (842 points), pas une page de 2400 points.
func TestJPEGToPDF_PageHasThePhysicalSizeOfTheImage(t *testing.T) {
	pdf, err := jpegToPDF(testJPEG(t, 1697, 2400, false), 205)
	if err != nil {
		t.Fatal(err)
	}
	s := string(pdf)
	if !strings.HasPrefix(s, "%PDF-1.") || !strings.HasSuffix(strings.TrimSpace(s), "%%EOF") {
		t.Fatalf("not a PDF: starts %q", s[:8])
	}
	m := regexp.MustCompile(`/MediaBox \[0 0 ([0-9.]+) ([0-9.]+)\]`).FindStringSubmatch(s)
	if m == nil {
		t.Fatal("no MediaBox")
	}
	w, _ := strconv.ParseFloat(m[1], 64)
	h, _ := strconv.ParseFloat(m[2], 64)
	if w < 595 || w > 597 || h < 842 || h > 844 {
		t.Errorf("page = %v x %v points, want about 596 x 843 (A4)", w, h)
	}
	for _, want := range []string{"/Filter /DCTDecode", "/Width 1697", "/Height 2400", "/ColorSpace /DeviceRGB"} {
		if !strings.Contains(s, want) {
			t.Errorf("PDF lacks %q", want)
		}
	}
}

func TestJPEGToPDF_GrayImageUsesDeviceGray(t *testing.T) {
	pdf, err := jpegToPDF(testJPEG(t, 100, 50, true), 205)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pdf), "/ColorSpace /DeviceGray") {
		t.Error("gray JPEG not declared DeviceGray")
	}
}

// La table des références doit pointer exactement sur chaque objet : un
// lecteur strict refuse sinon le fichier.
func TestJPEGToPDF_XrefOffsetsPointAtTheObjects(t *testing.T) {
	pdf, err := jpegToPDF(testJPEG(t, 64, 64, false), 205)
	if err != nil {
		t.Fatal(err)
	}
	start := regexp.MustCompile(`startxref\n(\d+)\n%%EOF`).FindSubmatch(pdf)
	if start == nil {
		t.Fatal("no startxref")
	}
	xref, _ := strconv.Atoi(string(start[1]))
	if !bytes.HasPrefix(pdf[xref:], []byte("xref\n")) {
		t.Fatalf("startxref does not point at the xref table")
	}
	entries := regexp.MustCompile(`(\d{10}) 00000 n `).FindAllSubmatch(pdf[xref:], -1)
	if len(entries) != 5 {
		t.Fatalf("%d xref entries, want 5", len(entries))
	}
	for i, e := range entries {
		off, _ := strconv.Atoi(string(e[1]))
		want := []byte(strconv.Itoa(i+1) + " 0 obj")
		if !bytes.HasPrefix(pdf[off:], want) {
			t.Errorf("object %d: offset %d does not point at %q", i+1, off, want)
		}
	}
}

func TestJPEGToPDF_RefusesWhatIsNotAJPEG(t *testing.T) {
	if _, err := jpegToPDF([]byte("pas une image"), 205); err == nil {
		t.Error("error = nil on garbage")
	}
}
