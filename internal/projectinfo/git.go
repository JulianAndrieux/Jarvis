// Package projectinfo lit, à chaque demande, ce que le projet sait de
// lui-même (jalon 29) : l'historique git, les décisions consignées dans
// CLAUDE.md et l'état des services. Rien n'est recopié ni mis en cache :
// la page Architecture reflète toujours le dépôt tel qu'il est.
package projectinfo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Commit est un commit du dépôt, message complet compris.
type Commit struct {
	Hash, Short string
	Author      string
	Date        time.Time
	Subject     string
	Body        string
	// Milestone est le jalon cité en tête du sujet ("Jalon 12",
	// "Jalons 26-27"), vide sinon.
	Milestone string
}

// Séparateurs de champs et d'enregistrements improbables dans un message.
const (
	fieldSep  = "\x1f"
	recordSep = "\x1e"
)

var (
	milestoneRe = regexp.MustCompile(`^(Jalons? \d+(?:[-–]\d+)?(?: bis)?)\b`)
	hashRe      = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

const logFormat = "%H" + fieldSep + "%h" + fieldSep + "%an" + fieldSep + "%cI" + fieldSep + "%s" + fieldSep + "%b" + recordSep

// Commits retourne l'historique de la branche courante de dir, du plus
// récent au plus ancien ; limit <= 0 : tout l'historique.
func Commits(ctx context.Context, dir string, limit int) ([]Commit, error) {
	args := []string{"log", "--format=" + logFormat}
	if limit > 0 {
		args = append(args, "-n", strconv.Itoa(limit))
	}
	out, err := git(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for _, rec := range strings.Split(out, recordSep) {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		c, err := parseCommit(rec)
		if err != nil {
			return nil, err
		}
		commits = append(commits, c)
	}
	return commits, nil
}

func parseCommit(rec string) (Commit, error) {
	f := strings.SplitN(rec, fieldSep, 6)
	if len(f) != 6 {
		return Commit{}, fmt.Errorf("projectinfo: sortie de git log illisible : %q", rec)
	}
	date, err := time.Parse(time.RFC3339, f[3])
	if err != nil {
		return Commit{}, fmt.Errorf("projectinfo: date de commit illisible %q : %w", f[3], err)
	}
	c := Commit{Hash: f[0], Short: f[1], Author: f[2], Date: date, Subject: f[4], Body: strings.TrimSpace(f[5])}
	if m := milestoneRe.FindStringSubmatch(c.Subject); m != nil {
		c.Milestone = m[1]
	}
	return c, nil
}

// Show retourne un commit et son diff. hash vient de l'URL : il doit être
// un hash hexadécimal, jamais une révision arbitraire ni une option.
func Show(ctx context.Context, dir, hash string) (Commit, string, error) {
	if !hashRe.MatchString(hash) {
		return Commit{}, "", fmt.Errorf("projectinfo: hash de commit refusé : %q", hash)
	}
	out, err := git(ctx, dir, "show", "--format="+logFormat, "--end-of-options", hash)
	if err != nil {
		return Commit{}, "", err
	}
	head, diff, _ := strings.Cut(out, recordSep)
	c, err := parseCommit(head)
	if err != nil {
		return Commit{}, "", err
	}
	return c, strings.TrimLeft(diff, "\n"), nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("projectinfo: git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}

// Change est un fichier modifié et pas encore commité.
type Change struct {
	// Status : code de `git status --porcelain` ("M", "A", "D", "??"...).
	Status string
	Path   string
}

// WorkingChanges liste le travail en cours, non commité, de dir.
func WorkingChanges(ctx context.Context, dir string) ([]Change, error) {
	out, err := git(ctx, dir, "status", "--porcelain=v1", "-z", "-uall")
	if err != nil {
		return nil, err
	}
	var changes []Change
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		xy := e[:2]
		changes = append(changes, Change{Status: strings.TrimSpace(xy), Path: e[3:]})
		if xy[0] == 'R' || xy[0] == 'C' {
			i++ // chemin d'origine d'un renommage
		}
	}
	return changes, nil
}

// Fingerprint résume l'état du projet visible sur la page : commit
// courant, travail en cours (fichiers et leur date) et CLAUDE.md. Il
// change dès que l'un d'eux change.
func Fingerprint(ctx context.Context, dir string) (string, error) {
	head, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	changes, err := WorkingChanges(ctx, dir)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	io.WriteString(h, head)
	for _, c := range append(changes, Change{Path: "CLAUDE.md"}) {
		fmt.Fprintf(h, "%s %s", c.Status, c.Path)
		if info, err := os.Stat(filepath.Join(dir, c.Path)); err == nil {
			fmt.Fprintf(h, " %d %d", info.Size(), info.ModTime().UnixNano())
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}
