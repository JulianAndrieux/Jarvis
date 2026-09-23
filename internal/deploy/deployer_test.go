package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeGit struct {
	calls                  []string
	dirty, conflict, noFF  error
	unchanged              bool // la branche n'apporte rien par rapport à main
	head                   string
	restoredTo, restoreMsg string
}

func (g *fakeGit) Prepare(ctx context.Context, id string) (string, error) {
	g.calls = append(g.calls, "prepare")
	return "/wt/" + id, nil
}
func (g *fakeGit) BaseClean(ctx context.Context) error {
	g.calls = append(g.calls, "clean")
	return g.dirty
}
func (g *fakeGit) SyncWithBase(ctx context.Context, dir string) error {
	g.calls = append(g.calls, "sync")
	return g.conflict
}
func (g *fakeGit) Diff(ctx context.Context, dir string) (string, error) {
	if g.unchanged {
		return "", nil
	}
	return "+x", nil
}
func (g *fakeGit) BaseHead(ctx context.Context) (string, error) { return g.head, nil }
func (g *fakeGit) Promote(ctx context.Context, id string) (string, error) {
	g.calls = append(g.calls, "promote")
	if g.noFF != nil {
		return "", g.noFF
	}
	g.head = "new123"
	return g.head, nil
}
func (g *fakeGit) RestoreBase(ctx context.Context, prev, msg string) (string, error) {
	g.calls = append(g.calls, "restore")
	g.restoredTo, g.restoreMsg = prev, msg
	return "rev456", nil
}

type fakeVerifier struct{ checksOK, testsOK bool }

func (v fakeVerifier) Checks(ctx context.Context, dir string) (string, bool) {
	return "gofmt : ok", v.checksOK
}
func (v fakeVerifier) Tests(ctx context.Context, dir, pkg string) (string, bool) {
	if !v.testsOK {
		return "--- FAIL: TestX", false
	}
	return "ok ./...", true
}

type harness struct {
	d        *Deployer
	git      *fakeGit
	bin      string
	marker   string
	steps    []string
	restarts []string
}

func newHarness(t *testing.T) *harness {
	dir := t.TempDir()
	h := &harness{git: &fakeGit{head: "old000"}, bin: filepath.Join(dir, "jarvisapp"), marker: filepath.Join(dir, "deploy.json")}
	os.WriteFile(h.bin, []byte("ancienne"), 0o755)
	h.d = &Deployer{
		Git:        h.git,
		Verifier:   fakeVerifier{checksOK: true, testsOK: true},
		Binary:     h.bin,
		MarkerPath: h.marker,
		Build: func(ctx context.Context, src, out string) (string, error) {
			h.git.calls = append(h.git.calls, "build")
			return "", os.WriteFile(out, []byte("nouvelle"), 0o755)
		},
		Smoke: func(ctx context.Context, binary string) (string, error) {
			h.git.calls = append(h.git.calls, "smoke")
			if b, _ := os.ReadFile(binary); string(b) != "nouvelle" {
				t.Errorf("smoke test run on %q, want the new build", b)
			}
			return "", nil
		},
		Restart: func(binary string) error {
			h.restarts = append(h.restarts, binary)
			return nil
		},
	}
	return h
}

func (h *harness) deploy() error {
	return h.d.Deploy(context.Background(), "t1", func(text, detail string) { h.steps = append(h.steps, text) })
}

func read(path string) string { b, _ := os.ReadFile(path); return string(b) }

func TestDeploy_VerifiesTriesThenPromotesSwapsAndRestarts(t *testing.T) {
	h := newHarness(t)
	if err := h.deploy(); err != nil {
		t.Fatalf("Deploy() = %v", err)
	}
	if got := strings.Join(h.git.calls, ","); got != "clean,prepare,sync,build,smoke,promote" {
		t.Errorf("order = %s (main must only move after verification, build and smoke test)", got)
	}
	if read(h.bin) != "nouvelle" || read(h.bin+".prev") != "ancienne" {
		t.Errorf("binaries: current=%q prev=%q", read(h.bin), read(h.bin+".prev"))
	}
	m, ok, _ := ReadMarker(h.marker)
	if !ok || m.TicketID != "t1" || m.Commit != "new123" || m.PrevCommit != "old000" || m.Binary != h.bin || m.PrevBinary != h.bin+".prev" {
		t.Errorf("marker = %+v, %v", m, ok)
	}
	if len(h.restarts) != 1 || h.restarts[0] != h.bin {
		t.Errorf("restarts = %v", h.restarts)
	}
	if len(h.steps) < 5 {
		t.Errorf("steps reported = %v", h.steps)
	}
}

// Tout échec avant l'avance de main laisse main, le binaire et le
// marqueur intacts.
func TestDeploy_FailuresBeforePromotionTouchNothing(t *testing.T) {
	cases := map[string]func(h *harness){
		"dépôt principal non commité": func(h *harness) { h.git.dirty = errors.New("travail non commité (a.go)") },
		"conflit":                     func(h *harness) { h.git.conflict = errors.New("conflit avec main dans a.go") },
		"vérification":                func(h *harness) { h.d.Verifier = fakeVerifier{checksOK: true, testsOK: false} },
		"compilation": func(h *harness) {
			h.d.Build = func(ctx context.Context, src, out string) (string, error) {
				return "undefined: x", errors.New("exit 1")
			}
		},
		"essai à blanc": func(h *harness) {
			h.d.Smoke = func(ctx context.Context, binary string) (string, error) {
				return "panic", errors.New("/documents répond 500")
			}
		},
		"main a avancé":   func(h *harness) { h.git.noFF = errors.New("pas d'avance rapide") },
		"rien à déployer": func(h *harness) { h.git.unchanged = true },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			setup(h)
			if err := h.deploy(); err == nil {
				t.Fatal("Deploy() = nil, want an error")
			}
			if read(h.bin) != "ancienne" {
				t.Errorf("binary replaced: %q", read(h.bin))
			}
			if _, err := os.Stat(h.bin + ".next"); err == nil {
				t.Error("new build left behind")
			}
			if _, ok, _ := ReadMarker(h.marker); ok {
				t.Error("marker written")
			}
			if len(h.restarts) != 0 {
				t.Error("restarted")
			}
		})
	}
}

func TestDeploy_VerificationReportInError(t *testing.T) {
	h := newHarness(t)
	h.d.Verifier = fakeVerifier{checksOK: true, testsOK: false}
	if err := h.deploy(); err == nil || !strings.Contains(err.Error(), "FAIL: TestX") {
		t.Errorf("err = %v, want the failing test named", err)
	}
}

// Le redémarrage échoue : tout est remis comme avant, main compris.
func TestDeploy_RestartFailureRollsEverythingBack(t *testing.T) {
	h := newHarness(t)
	h.d.Restart = func(string) error { return errors.New("exec: permission refusée") }
	err := h.deploy()
	if err == nil || !strings.Contains(err.Error(), "permission refusée") {
		t.Fatalf("Deploy() = %v", err)
	}
	if read(h.bin) != "ancienne" {
		t.Errorf("binary = %q, want the old version back", read(h.bin))
	}
	if h.git.restoredTo != "old000" || !strings.Contains(h.git.restoreMsg, "t1") {
		t.Errorf("main restored to %q (%q)", h.git.restoredTo, h.git.restoreMsg)
	}
	if _, ok, _ := ReadMarker(h.marker); ok {
		t.Error("marker left behind")
	}
}

// Vu en réel (ticket "Ajouter commentaire sur document") : la branche,
// une fois main intégré, était identique à main. Le déploiement a fait
// "avancer" main de 2f6a18f à 2f6a18f et affiché « Déployé » alors que
// rien n'avait été livré. Il doit s'arrêter avant toute vérification.
func TestDeploy_NothingToDeployIsRefusedEarly(t *testing.T) {
	h := newHarness(t)
	h.git.unchanged = true
	err := h.deploy()
	if err == nil || !strings.Contains(err.Error(), "rien à déployer") {
		t.Fatalf("Deploy() = %v, want a refusal naming the cause", err)
	}
	if got := strings.Join(h.git.calls, ","); got != "clean,prepare,sync" {
		t.Errorf("calls = %s, want a stop right after syncing with main", got)
	}
}
