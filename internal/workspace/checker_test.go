package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newModule écrit un petit module Go (go.mod repris du vrai projet pour la
// même version de Go).
func newModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n\ngo 1.25\n"), 0o644)
	for name, content := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
	}
	return dir
}

func TestChecker_TestsPassAndFail(t *testing.T) {
	c := Checker{}
	ok := newModule(t, map[string]string{"a/a.go": "package a\n\nfunc A() int { return 1 }\n", "a/a_test.go": "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { if A() != 1 { t.Fatal(\"x\") } }\n"})
	if report, pass := c.Tests(context.Background(), ok, "./..."); !pass {
		t.Errorf("passing tests reported as failing:\n%s", report)
	}
	bad := newModule(t, map[string]string{"a/a.go": "package a\n\nfunc A() int { return 2 }\n", "a/a_test.go": "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { if A() != 1 { t.Fatal(\"A vaut 2\") } }\n"})
	report, pass := c.Tests(context.Background(), bad, "./...")
	if pass || !strings.Contains(report, "A vaut 2") || !strings.Contains(report, "FAIL") {
		t.Errorf("failing tests: pass=%v report=\n%s", pass, report)
	}
}

// Le code écrit par l'agent s'exécute avant la revue : aucun secret dans
// son environnement (MONGO_URI...), pas de téléchargement de module.
func TestChecker_TestsRunWithScrubbedEnvironment(t *testing.T) {
	t.Setenv("MONGO_URI", "mongodb+srv://secret")
	t.Setenv("JARVIS_SECRET", "x")
	dir := newModule(t, map[string]string{"a/a_test.go": `package a

import (
	"os"
	"testing"
)

func TestEnv(t *testing.T) {
	for _, k := range []string{"MONGO_URI", "JARVIS_SECRET"} {
		if os.Getenv(k) != "" {
			t.Fatalf("%s leaked into the test environment", k)
		}
	}
	if os.Getenv("GOPROXY") != "off" {
		t.Fatalf("GOPROXY = %q, want off", os.Getenv("GOPROXY"))
	}
}
`})
	if report, pass := (Checker{}).Tests(context.Background(), dir, "./..."); !pass {
		t.Errorf("environment not scrubbed:\n%s", report)
	}
}

func TestChecker_ChecksCatchFormattingVetAndBuild(t *testing.T) {
	c := Checker{}
	clean := newModule(t, map[string]string{"a/a.go": "package a\n\nfunc A() int { return 1 }\n"})
	if report, pass := c.Checks(context.Background(), clean); !pass {
		t.Errorf("clean module failed checks:\n%s", report)
	}
	unformatted := newModule(t, map[string]string{"a/a.go": "package a\nfunc   A() int {return 1}\n"})
	if report, pass := c.Checks(context.Background(), unformatted); pass || !strings.Contains(report, "gofmt") || !strings.Contains(report, "a/a.go") {
		t.Errorf("unformatted file: pass=%v report=\n%s", pass, report)
	}
	broken := newModule(t, map[string]string{"a/a.go": "package a\n\nfunc A() int { return \"x\" }\n"})
	if report, pass := c.Checks(context.Background(), broken); pass || !strings.Contains(report, "a/a.go") {
		t.Errorf("non-compiling code: pass=%v report=\n%s", pass, report)
	}
}

func TestChecker_TimeoutStopsRunawayTests(t *testing.T) {
	dir := newModule(t, map[string]string{"a/a_test.go": "package a\n\nimport (\"testing\"; \"time\")\n\nfunc TestSlow(t *testing.T) { time.Sleep(time.Minute) }\n"})
	start := time.Now()
	report, pass := (Checker{Timeout: 3 * time.Second}).Tests(context.Background(), dir, "./...")
	if pass || time.Since(start) > 30*time.Second {
		t.Errorf("runaway test: pass=%v after %s", pass, time.Since(start))
	}
	if !strings.Contains(report, "délai") {
		t.Errorf("report should say it timed out:\n%s", report)
	}
}
