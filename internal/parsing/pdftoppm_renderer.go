package parsing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// PdftoppmRenderer rend une page de PDF en PNG en s'appuyant sur le binaire
// externe `pdftoppm` (poppler-utils, le même paquet que pdftotext utilisé
// par internal/triage). BinPath permet de surcharger le nom/chemin du
// binaire ; une valeur vide utilise "pdftoppm".
type PdftoppmRenderer struct {
	BinPath string
}

func (r PdftoppmRenderer) RenderPage(ctx context.Context, path string, page int, dpi int) ([]byte, error) {
	bin := r.BinPath
	if bin == "" {
		bin = "pdftoppm"
	}

	tmpDir, err := os.MkdirTemp("", "jarvis-render-*")
	if err != nil {
		return nil, fmt.Errorf("parsing: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// -singlefile : écrit exactement <prefix>.png, sans suffixe de
	// numéro de page (qui varierait sinon selon le nombre total de pages
	// du document).
	prefix := filepath.Join(tmpDir, "page")

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin,
		"-png",
		"-r", strconv.Itoa(dpi),
		"-f", strconv.Itoa(page),
		"-l", strconv.Itoa(page),
		"-singlefile",
		path, prefix,
	)
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("parsing: pdftoppm failed on %s page %d: %w: %s", path, page, err, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("parsing: run pdftoppm on %s page %d: %w", path, page, err)
	}

	pngPath := prefix + ".png"
	png, err := os.ReadFile(pngPath)
	if err != nil {
		return nil, fmt.Errorf("parsing: read rendered PNG for %s page %d: %w", path, page, err)
	}
	if len(png) == 0 {
		return nil, fmt.Errorf("parsing: pdftoppm produced an empty PNG for %s page %d (page out of range?)", path, page)
	}
	return png, nil
}
