package launcher

import (
	"strings"
	"testing"
)

// Lancé depuis le Finder (raccourci Dock, `open`), un .app reçoit un PATH
// minimal (/usr/bin:/bin:/usr/sbin:/sbin) : jarvisapp n'y trouvait ni
// `go` (navigateur de code) ni `soffice` (LibreOffice), le lanceur ni
// `llama-server` — trouvé au jalon 27, l'application ne démarrait plus.
func TestWithToolPaths_AddsHomebrewAndGoToAMinimalPath(t *testing.T) {
	got := WithToolPaths("/usr/bin:/bin:/usr/sbin:/sbin", "/Users/x")
	parts := strings.Split(got, ":")
	if parts[0] != "/usr/bin" {
		t.Errorf("existing entries must keep their order first: %q", got)
	}
	for _, want := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/local/go/bin", "/Users/x/go/bin"} {
		if !strings.Contains(":"+got+":", ":"+want+":") {
			t.Errorf("PATH %q lacks %s", got, want)
		}
	}
}

func TestWithToolPaths_NoDuplicates(t *testing.T) {
	got := WithToolPaths("/opt/homebrew/bin:/usr/bin", "/Users/x")
	if strings.Count(got, "/opt/homebrew/bin") != 1 {
		t.Errorf("PATH %q duplicates /opt/homebrew/bin", got)
	}
}
