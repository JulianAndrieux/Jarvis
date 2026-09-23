package workspace

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// remote : le dépôt distant (GitHub).
const remote = "origin"

// pushTimeout borne un push : sans réseau ou sans identifiants, échouer
// vite plutôt que bloquer l'interface.
const pushTimeout = 2 * time.Minute

// Unpushed liste les commits de main pas encore sur GitHub (selon la
// dernière connaissance locale d'origin/main), du plus récent au plus
// ancien, sous la forme "abc1234 sujet".
func (m Manager) Unpushed(ctx context.Context) ([]string, error) {
	out, err := m.git(ctx, m.Repo, "log", "--format=%h %s", remote+"/"+m.base()+".."+m.base())
	if err != nil {
		return nil, err
	}
	var commits []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" {
			commits = append(commits, l)
		}
	}
	return commits, nil
}

// Push envoie main sur GitHub et retourne le commit poussé. Jamais de
// push forcé : si GitHub a des commits que main n'a pas, le push est
// refusé et l'erreur dit quoi faire. Aucune invite interactive
// (GIT_TERMINAL_PROMPT=0) : les identifiants viennent du trousseau.
func (m Manager) Push(ctx context.Context) (string, error) {
	if err := m.onBase(ctx); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	if _, err := m.git(ctx, m.Repo, "push", remote, m.base()+":"+m.base()); err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "rejected") || strings.Contains(msg, "non-fast-forward") || strings.Contains(msg, "fetch first"):
			return "", fmt.Errorf("GitHub a des commits que %s n'a pas : push refusé (jamais de push forcé). Intègre-les d'abord (git pull) dans %s. Détail : %v", m.base(), m.Repo, err)
		case ctx.Err() != nil:
			return "", fmt.Errorf("push vers GitHub trop long (%s) : réseau ou identifiants ? %v", pushTimeout, err)
		}
		return "", fmt.Errorf("push vers GitHub impossible : %v", err)
	}
	return m.BaseHead(ctx)
}
