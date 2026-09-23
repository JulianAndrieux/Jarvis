// Package workspace gère les copies de travail isolées où l'agent
// développe un ticket (jalon 28) : une branche git ticket/<id> dans un
// worktree hors du dépôt principal. Toutes les commandes git sont
// lancées par Jarvis, jamais par l'agent — main n'est jamais modifié ici
// (le déploiement est une étape séparée, validée par l'utilisateur).
package workspace

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Manager crée et nettoie les copies de travail des tickets.
type Manager struct {
	// Repo est le dépôt principal (branche main).
	Repo string
	// Root accueille les copies de travail (ex. ~/.jarvis/worktrees).
	Root string
	// Base est la branche de départ (vide : "main").
	Base string
}

// safeID : un identifiant de ticket sert de nom de branche et de
// dossier — rien qui puisse sortir de Root ou passer pour une option git.
var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func (m Manager) base() string {
	if m.Base == "" {
		return "main"
	}
	return m.Base
}

func branchName(id string) string { return "ticket/" + id }

// Dir est l'emplacement de la copie de travail d'un ticket.
func (m Manager) Dir(id string) string { return filepath.Join(m.Root, "ticket-"+id) }

// Prepare crée (ou retrouve) la copie de travail du ticket id, sur la
// branche ticket/<id> partant de main.
func (m Manager) Prepare(ctx context.Context, id string) (string, error) {
	if !safeID.MatchString(id) {
		return "", fmt.Errorf("workspace: identifiant de ticket refusé : %q", id)
	}
	dir := m.Dir(id)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		// Copie reprise : si elle n'a aucun travail propre, la ramener au
		// main actuel. Avance rapide seulement — un échec (travail en
		// cours, branche divergente) laisse la copie telle quelle.
		if out, err := m.git(ctx, dir, "status", "--porcelain"); err == nil && strings.TrimSpace(out) == "" {
			m.git(ctx, dir, "merge", "--ff-only", "-q", m.base())
		}
		return dir, nil
	}
	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	args := []string{"worktree", "add", "-b", branchName(id), dir, m.base()}
	if m.branchExists(ctx, branchName(id)) {
		args = []string{"worktree", "add", dir, branchName(id)}
	}
	if _, err := m.git(ctx, m.Repo, args...); err != nil {
		return "", err
	}
	return dir, nil
}

func (m Manager) branchExists(ctx context.Context, branch string) bool {
	out, err := m.git(ctx, m.Repo, "branch", "--list", branch)
	return err == nil && strings.TrimSpace(out) != ""
}

// Diff retourne toutes les modifications de la copie de travail depuis
// son point de départ sur main — commitées ou non, fichiers nouveaux
// compris. Comparer à la base commune, pas à la pointe de main : main
// peut avoir avancé depuis, et ses commits apparaîtraient à l'envers.
func (m Manager) Diff(ctx context.Context, dir string) (string, error) {
	if _, err := m.git(ctx, dir, "add", "-A"); err != nil {
		return "", err
	}
	base, err := m.git(ctx, dir, "merge-base", "HEAD", m.base())
	if err != nil {
		return "", err
	}
	return m.git(ctx, dir, "diff", "--cached", strings.TrimSpace(base))
}

// Commit enregistre les modifications sur la branche du ticket.
func (m Manager) Commit(ctx context.Context, dir, message string) error {
	if _, err := m.git(ctx, dir, "add", "-A"); err != nil {
		return err
	}
	if out, _ := m.git(ctx, dir, "status", "--porcelain"); strings.TrimSpace(out) == "" {
		return nil // rien de nouveau depuis le dernier commit
	}
	_, err := m.git(ctx, dir, "commit", "-q", "-m", message)
	return err
}

// Discard supprime la copie de travail et la branche d'un ticket
// abandonné.
func (m Manager) Discard(ctx context.Context, id string) error {
	if !safeID.MatchString(id) {
		return fmt.Errorf("workspace: identifiant de ticket refusé : %q", id)
	}
	if _, err := os.Stat(m.Dir(id)); err == nil {
		if _, err := m.git(ctx, m.Repo, "worktree", "remove", "--force", m.Dir(id)); err != nil {
			return err
		}
	}
	if m.branchExists(ctx, branchName(id)) {
		if _, err := m.git(ctx, m.Repo, "branch", "-D", branchName(id)); err != nil {
			return err
		}
	}
	return nil
}

func (m Manager) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Auteur explicite : les commits de l'agent sont identifiables.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Agent Jarvis", "GIT_AUTHOR_EMAIL=agent@jarvis.local",
		"GIT_COMMITTER_NAME=Agent Jarvis", "GIT_COMMITTER_EMAIL=agent@jarvis.local",
		// Jamais d'invite interactive (identifiants) : l'application n'a
		// pas de terminal, une invite bloquerait sans fin.
		"GIT_TERMINAL_PROMPT=0")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("workspace: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}
