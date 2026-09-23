package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Git : opérations git du déploiement — workspace.Manager en production.
type Git interface {
	Prepare(ctx context.Context, id string) (dir string, err error)
	BaseClean(ctx context.Context) error
	SyncWithBase(ctx context.Context, dir string) error
	BaseHead(ctx context.Context) (string, error)
	Promote(ctx context.Context, id string) (string, error)
	RestoreBase(ctx context.Context, prev, message string) (string, error)
}

// Verifier : vérification complète — workspace.Checker en production.
type Verifier interface {
	Checks(ctx context.Context, dir string) (string, bool)
	Tests(ctx context.Context, dir, pkg string) (string, bool)
}

// Deployer déploie la branche d'un ticket sur l'application en service.
type Deployer struct {
	Git      Git
	Verifier Verifier
	// Binary : chemin absolu du binaire en service (bin/jarvisapp).
	Binary     string
	MarkerPath string
	// Build compile l'application de src dans out (défaut : GoBuild).
	Build func(ctx context.Context, src, out string) (string, error)
	// Smoke essaie un binaire à blanc (cf. Smoke.Run).
	Smoke func(ctx context.Context, binary string) (string, error)
	// Restart remplace le processus courant par binary (défaut : Exec).
	// Ne revient qu'en cas d'échec.
	Restart func(binary string) error
}

// Deploy mène le déploiement du ticket id. main n'avance qu'une fois la
// branche fusionnée avec main, vérifiée, compilée et essayée à blanc ;
// tout échec avant laisse main, le binaire et le marqueur intacts. En cas
// de succès, Restart remplace le processus et Deploy ne revient pas (sauf
// avec un Restart factice). onStep reçoit chaque étape (texte, détail).
func (d Deployer) Deploy(ctx context.Context, id string, onStep func(text, detail string)) error {
	step := func(text, detail string) {
		if onStep != nil {
			onStep(text, detail)
		}
	}
	if err := d.Git.BaseClean(ctx); err != nil {
		return err
	}
	dir, err := d.Git.Prepare(ctx, id)
	if err != nil {
		return err
	}
	if err := d.Git.SyncWithBase(ctx, dir); err != nil {
		return err
	}
	step("Branche à jour avec main", "")

	checks, ok := d.Verifier.Checks(ctx, dir)
	if !ok {
		return fmt.Errorf("vérification en échec après intégration de main :\n%s", checks)
	}
	tests, ok := d.Verifier.Tests(ctx, dir, "./...")
	if !ok {
		return fmt.Errorf("tests en échec après intégration de main :\n%s", tests)
	}
	step("Vérification complète réussie", checks+"\n"+tests)

	next := d.Binary + ".next"
	defer os.Remove(next) // sans effet une fois renommé
	build := d.Build
	if build == nil {
		build = GoBuild
	}
	if out, err := build(ctx, dir, next); err != nil {
		return fmt.Errorf("compilation impossible : %v\n%s", err, out)
	}
	step("Nouvelle version compilée", "")
	if out, err := d.Smoke(ctx, next); err != nil {
		return fmt.Errorf("%v\n%s", err, tail(out, 3000))
	}
	step("Essai à blanc réussi : la nouvelle version démarre et ses pages répondent", "")

	prev, err := d.Git.BaseHead(ctx)
	if err != nil {
		return err
	}
	commit, err := d.Git.Promote(ctx, id)
	if err != nil {
		return err
	}
	step("main avance jusqu'au ticket ("+short(prev)+" → "+short(commit)+")", "")

	// À partir d'ici, main a bougé : tout échec défait ce qui précède.
	undo := func(cause error) error {
		d.restoreBinary()
		if _, err := d.Git.RestoreBase(ctx, prev, fmt.Sprintf("Retour arrière du déploiement du ticket %s\n\n%v", id, cause)); err != nil {
			cause = fmt.Errorf("%v ; retour arrière de main impossible : %v", cause, err)
		}
		RemoveMarker(d.MarkerPath)
		return cause
	}
	prevBinary := d.Binary + ".prev"
	if err := os.Rename(d.Binary, prevBinary); err != nil {
		return undo(fmt.Errorf("sauvegarde de l'ancienne version : %w", err))
	}
	if err := os.Rename(next, d.Binary); err != nil {
		return undo(fmt.Errorf("installation de la nouvelle version : %w", err))
	}
	m := Marker{TicketID: id, Commit: commit, PrevCommit: prev, Binary: d.Binary, PrevBinary: prevBinary, At: time.Now()}
	if err := WriteMarker(d.MarkerPath, m); err != nil {
		return undo(err)
	}
	step("Redémarrage sur la nouvelle version", "")
	restart := d.Restart
	if restart == nil {
		restart = Exec
	}
	if err := restart(d.Binary); err != nil {
		return undo(fmt.Errorf("redémarrage impossible : %w", err))
	}
	return nil
}

// restoreBinary remet l'ancienne version en place (échec après l'échange).
func (d Deployer) restoreBinary() {
	prev := d.Binary + ".prev"
	if _, err := os.Stat(prev); err != nil {
		return
	}
	os.Remove(d.Binary)
	os.Rename(prev, d.Binary)
}

// GoBuild compile cmd/jarvisapp de src dans out.
func GoBuild(ctx context.Context, src, out string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, "./cmd/jarvisapp")
	cmd.Dir = src
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// Exec remplace le processus courant par binary, mêmes arguments et même
// environnement : même PID, donc le lanceur continue de le surveiller.
func Exec(binary string) error {
	return syscall.Exec(binary, append([]string{binary}, os.Args[1:]...), os.Environ())
}

func short(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
