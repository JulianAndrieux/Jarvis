package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Checker lance les vérifications et les tests dans une copie de travail.
// Le code testé a été écrit par l'agent et s'exécute avant la revue
// humaine : environnement vidé (aucun secret, MONGO_URI compris),
// téléchargement de modules interdit (GOPROXY=off), délai borné, et tout
// le groupe de processus tué au dépassement. Ce n'est pas un bac à sable
// complet (même utilisateur, réseau local possible) — une vraie isolation
// (conteneur, VM) est prévue sur la machine dédiée.
type Checker struct {
	// Timeout borne une commande (0 : 10 minutes).
	Timeout time.Duration
	// MaxReport borne le rapport rendu (0 : 6000 caractères, la fin de la
	// sortie : c'est là que sont les échecs).
	MaxReport int
}

var (
	goEnvOnce sync.Once
	goEnv     map[string]string
)

// goPaths : caches et chemins de Go de l'utilisateur, pour que les tests
// réutilisent les modules déjà téléchargés sans réseau.
func goPaths() map[string]string {
	goEnvOnce.Do(func() {
		goEnv = map[string]string{}
		out, err := exec.Command("go", "env", "GOCACHE", "GOMODCACHE", "GOPATH").Output()
		if err != nil {
			return
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		for i, k := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
			if i < len(lines) {
				goEnv[k] = lines[i]
			}
		}
	})
	return goEnv
}

// env est l'environnement minimal des commandes : de quoi trouver les
// outils et les caches, rien d'autre.
func (c Checker) env() []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"TMPDIR=" + os.TempDir(),
		"GOPROXY=off",
		"GOFLAGS=-mod=readonly",
	}
	for k, v := range goPaths() {
		env = append(env, k+"="+v)
	}
	return env
}

func (c Checker) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 10 * time.Minute
}

// run exécute une commande dans dir ; ok=false si elle échoue ou dépasse
// le délai.
func (c Checker) run(ctx context.Context, dir, name string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = c.env()
	// Groupe de processus : au délai, tuer aussi le binaire de test lancé
	// par `go test` (sinon il continue, orphelin).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Sprintf("%s\n→ délai dépassé (%s) : commande arrêtée.", out.String(), c.timeout()), false
	}
	return out.String(), err == nil
}

func (c Checker) clip(s string) string {
	max := c.MaxReport
	if max <= 0 {
		max = 6000
	}
	if len(s) <= max {
		return s
	}
	cut := len(s) - max
	for cut < len(s) && (s[cut]&0xC0) == 0x80 {
		cut++
	}
	return "… (début tronqué)\n" + s[cut:]
}

// Tests lance `go test -count=1 pkg` ("./..." : toute la suite unitaire).
func (c Checker) Tests(ctx context.Context, dir, pkg string) (string, bool) {
	if pkg == "" {
		pkg = "./..."
	}
	out, ok := c.run(ctx, dir, "go", "test", "-count=1", pkg)
	return c.clip(out), ok
}

// Checks : génération des gabarits templ (s'il y en a), gofmt, go vet,
// go build. S'arrête au premier échec.
func (c Checker) Checks(ctx context.Context, dir string) (string, bool) {
	var report strings.Builder
	if hasTempl(dir) {
		if _, err := exec.LookPath("templ"); err == nil {
			if out, ok := c.run(ctx, dir, "templ", "generate", "./..."); !ok {
				return c.clip("templ generate : échec\n" + out), false
			}
		}
	}
	out, ok := c.run(ctx, dir, "gofmt", "-l", ".")
	if !ok || strings.TrimSpace(out) != "" {
		return c.clip("gofmt : fichiers à formater\n" + out), false
	}
	report.WriteString("gofmt : ok\n")
	if out, ok := c.run(ctx, dir, "go", "vet", "./..."); !ok {
		return c.clip(report.String() + "go vet : échec\n" + out), false
	}
	report.WriteString("go vet : ok\n")
	if out, ok := c.run(ctx, dir, "go", "build", "./..."); !ok {
		return c.clip(report.String() + "go build : échec\n" + out), false
	}
	report.WriteString("go build : ok\n")
	return report.String(), true
}

func hasTempl(dir string) bool {
	found := false
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || found {
			return filepath.SkipAll
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if strings.HasSuffix(d.Name(), ".templ") {
			found = true
		}
		return nil
	})
	return found
}
