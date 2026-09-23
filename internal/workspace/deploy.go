package workspace

import (
	"context"
	"fmt"
	"strings"
)

// Opérations git du déploiement (jalon 30). main ne bouge que par avance
// rapide jusqu'à une branche déjà vérifiée, ou par un commit de retour
// arrière : jamais de réécriture d'historique.

// SyncWithBase intègre main dans la copie du ticket (commit de fusion sur
// la branche du ticket). En cas de conflit, la fusion est annulée et
// l'erreur nomme les fichiers en conflit.
func (m Manager) SyncWithBase(ctx context.Context, dir string) error {
	if _, err := m.git(ctx, dir, "merge", "--no-edit", "-q", m.base()); err != nil {
		conflicts, _ := m.git(ctx, dir, "diff", "--name-only", "--diff-filter=U")
		m.git(ctx, dir, "merge", "--abort")
		if files := strings.Fields(conflicts); len(files) > 0 {
			return fmt.Errorf("workspace: conflit avec %s dans %s", m.base(), strings.Join(files, ", "))
		}
		return err
	}
	return nil
}

// BaseClean vérifie que le dépôt principal n'a pas de modification
// suivie non commitée — l'avance de main la toucherait. Les fichiers non
// suivis ne comptent pas.
func (m Manager) BaseClean(ctx context.Context) error {
	out, err := m.git(ctx, m.Repo, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return err
	}
	// Pas de TrimSpace : la première colonne du format porcelain peut
	// être une espace (" M a.go").
	if s := strings.TrimRight(out, "\n"); strings.TrimSpace(s) != "" {
		var files []string
		for _, l := range strings.Split(s, "\n") {
			if len(l) > 3 {
				files = append(files, l[3:])
			}
		}
		return fmt.Errorf("workspace: le dépôt principal a du travail non commité (%s) : commite-le ou mets-le de côté avant de déployer", strings.Join(files, ", "))
	}
	return nil
}

// BaseHead est le commit courant de main.
func (m Manager) BaseHead(ctx context.Context) (string, error) {
	out, err := m.git(ctx, m.Repo, "rev-parse", m.base())
	return strings.TrimSpace(out), err
}

// Promote fait avancer main jusqu'à la branche du ticket, par avance
// rapide uniquement (la branche doit avoir intégré main : SyncWithBase),
// et retourne le nouveau commit de main.
func (m Manager) Promote(ctx context.Context, id string) (string, error) {
	if !safeID.MatchString(id) {
		return "", fmt.Errorf("workspace: identifiant de ticket refusé : %q", id)
	}
	if err := m.onBase(ctx); err != nil {
		return "", err
	}
	if _, err := m.git(ctx, m.Repo, "merge", "--ff-only", "-q", branchName(id)); err != nil {
		return "", fmt.Errorf("workspace: %s ne peut pas avancer jusqu'au ticket (a-t-il avancé entre-temps ?) : %w", m.base(), err)
	}
	return m.BaseHead(ctx)
}

// RestoreBase ajoute sur main un commit qui rétablit l'arbre de prev
// (retour arrière d'un déploiement) et retourne ce commit.
func (m Manager) RestoreBase(ctx context.Context, prev, message string) (string, error) {
	if err := m.onBase(ctx); err != nil {
		return "", err
	}
	tree, err := m.git(ctx, m.Repo, "rev-parse", prev+"^{tree}")
	if err != nil {
		return "", err
	}
	head, err := m.BaseHead(ctx)
	if err != nil {
		return "", err
	}
	commit, err := m.git(ctx, m.Repo, "commit-tree", strings.TrimSpace(tree), "-p", head, "-m", message)
	if err != nil {
		return "", err
	}
	if _, err := m.git(ctx, m.Repo, "merge", "--ff-only", "-q", strings.TrimSpace(commit)); err != nil {
		return "", err
	}
	return strings.TrimSpace(commit), nil
}

// onBase : le dépôt principal doit être sur main — avancer une autre
// branche par erreur serait pire qu'échouer.
func (m Manager) onBase(ctx context.Context) error {
	out, err := m.git(ctx, m.Repo, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return err
	}
	if cur := strings.TrimSpace(out); cur != m.base() {
		return fmt.Errorf("workspace: le dépôt principal est sur %q, pas sur %q", cur, m.base())
	}
	return nil
}
