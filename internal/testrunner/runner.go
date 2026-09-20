// Package testrunner exécute `go test -json` pour un ou plusieurs
// packages et agrège les événements en résultats structurés — le
// "lancer les tests localement" de la page de contrôle demandée.
package testrunner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"time"
)

// Status est le statut d'un test ou d'un package.
type Status string

const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
	StatusSkip Status = "skip"
)

// TestResult est le résultat d'un test top-level.
type TestResult struct {
	Name    string
	Status  Status
	Elapsed float64
	Output  string // sortie concaténée (trace en cas d'échec)
}

// PackageResult regroupe les résultats des tests d'un package, plus la
// sortie de niveau package (utile si le build a échoué avant qu'aucun
// test n'ait pu démarrer).
type PackageResult struct {
	Package     string
	Status      Status
	Elapsed     float64
	Tests       []TestResult
	BuildOutput string // sortie sans Test associé (ex. erreur de compilation)
}

// Options paramètre une exécution.
type Options struct {
	Dir         string        // répertoire du module
	Patterns    []string      // ex. []string{"./internal/triage/..."}
	Run         string        // regex passée à -run ; "" = tous les tests
	Integration bool          // ajoute -tags=integration
	Timeout     time.Duration // "" -> défaut de `go test`
}

// Run exécute `go test -json` selon opts et retourne les résultats
// agrégés par package. Un échec de test (Status fail) n'est PAS une
// erreur de Run — Run ne retourne une erreur que si l'outil lui-même n'a
// pas pu s'exécuter (go introuvable, sortie JSON illisible...).
func Run(ctx context.Context, opts Options) ([]PackageResult, error) {
	args := []string{"test", "-json"}
	if opts.Run != "" {
		args = append(args, "-run", opts.Run)
	}
	if opts.Integration {
		args = append(args, "-tags=integration")
	}
	if opts.Timeout > 0 {
		args = append(args, "-timeout", opts.Timeout.String())
	}
	args = append(args, opts.Patterns...)

	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = opts.Dir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("testrunner: stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // `go test` écrit ses propres erreurs (ex. build) sur stderr aussi

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("testrunner: start go test: %w", err)
	}

	results, parseErr := parseEvents(stdout)

	waitErr := cmd.Wait()
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			// go test lui-même n'a pas pu tourner (ex. binaire "go"
			// introuvable) — une vraie erreur d'outillage, distincte d'un
			// simple échec de test (qui donne aussi un exit code non-nul,
			// mais via *exec.ExitError, attendu et déjà reflété dans
			// results).
			return results, fmt.Errorf("testrunner: run go test: %w", waitErr)
		}
	}

	if parseErr != nil {
		return results, fmt.Errorf("testrunner: parse output: %w", parseErr)
	}

	return results, nil
}

// event est la forme d'une ligne de `go test -json` (voir `go help
// testflag`).
type event struct {
	Action  string
	Package string
	Test    string
	Elapsed float64
	Output  string
}

// parseEvents décode le flux JSONL de `go test -json` et agrège les
// événements en PackageResult/TestResult. Fonction pure (hors lecture du
// flux) : testable avec des événements canned, sans jamais lancer de
// vraie commande.
func parseEvents(r io.Reader) ([]PackageResult, error) {
	pkgs := map[string]*PackageResult{}
	var order []string

	pkgOf := func(name string) *PackageResult {
		p, ok := pkgs[name]
		if !ok {
			p = &PackageResult{Package: name}
			pkgs[name] = p
			order = append(order, name)
		}
		return p
	}

	testIndex := map[[2]string]int{} // (package, test) -> index dans Tests

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e event
		if err := json.Unmarshal(line, &e); err != nil {
			return toSlice(pkgs, order), fmt.Errorf("decode event %q: %w", line, err)
		}
		if e.Package == "" {
			continue
		}
		p := pkgOf(e.Package)

		if e.Test == "" {
			switch e.Action {
			case "pass", "fail", "skip":
				p.Status = Status(e.Action)
				p.Elapsed = e.Elapsed
			case "output":
				p.BuildOutput += e.Output
			}
			continue
		}

		key := [2]string{e.Package, e.Test}
		idx, ok := testIndex[key]
		if !ok {
			p.Tests = append(p.Tests, TestResult{Name: e.Test})
			idx = len(p.Tests) - 1
			testIndex[key] = idx
		}

		switch e.Action {
		case "output":
			p.Tests[idx].Output += e.Output
		case "pass", "fail", "skip":
			p.Tests[idx].Status = Status(e.Action)
			p.Tests[idx].Elapsed = e.Elapsed
		}
	}
	if err := scanner.Err(); err != nil {
		return toSlice(pkgs, order), fmt.Errorf("scan output: %w", err)
	}

	results := toSlice(pkgs, order)
	for _, p := range results {
		sort.Slice(p.Tests, func(i, j int) bool { return p.Tests[i].Name < p.Tests[j].Name })
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Package < results[j].Package })
	return results, nil
}

func toSlice(pkgs map[string]*PackageResult, order []string) []PackageResult {
	out := make([]PackageResult, 0, len(order))
	for _, name := range order {
		out = append(out, *pkgs[name])
	}
	return out
}
