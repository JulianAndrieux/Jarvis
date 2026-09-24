//go:build integration

package visual

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

const chromePath = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

// Vrai Chrome : une page servie localement devient un PNG.
func TestChrome_CapturesAPage(t *testing.T) {
	if _, err := os.Stat(chromePath); err != nil {
		t.Skip("Chrome absent")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<body style="font:30px sans-serif"><form><input name="q"></form></body>`)
	}))
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "page.png")
	if err := (Chrome{Path: chromePath, Width: 800, Height: 400}).Capture(context.Background(), srv.URL, out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if !bytes.HasPrefix(b, []byte("\x89PNG")) || len(b) < 1000 {
		t.Errorf("capture = %d octets, pas un PNG", len(b))
	}
}
