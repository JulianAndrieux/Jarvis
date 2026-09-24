package visual

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// Capturer photographie une page.
type Capturer interface {
	Capture(ctx context.Context, url, out string) error
}

// Judger juge des captures (VisionJudge en production).
type Judger interface {
	Judge(ctx context.Context, req JudgeRequest) (tickets.ReviewResult, error)
}

// Reviewer : la relecture visuelle d'un ticket (tickets.Reviewer).
type Reviewer struct {
	// Build compile l'application de src dans out (deploy.GoBuild).
	Build func(ctx context.Context, src, out string) (string, error)
	// Launch démarre un binaire sur un port libre, isolé (collections
	// jetables, sans modèles ni dossier surveillé), et retourne son adresse
	// et de quoi l'arrêter.
	Launch func(ctx context.Context, binary string) (baseURL string, stop func(), err error)
	// BeforeBinary : la version en service (bin/jarvisapp).
	BeforeBinary string
	Capturer     Capturer
	Judge        Judger
	WorkDir      string // "" : répertoire temporaire du système
}

// Review compile la version du ticket, lance les deux versions côte à
// côte, capture les pages dont un gabarit change, et les fait juger.
func (r *Reviewer) Review(ctx context.Context, req tickets.ReviewRequest, onStep func(tickets.AgentStep)) (tickets.ReviewResult, error) {
	step := func(s string) {
		if onStep != nil {
			onStep(tickets.AgentStep{Summary: s})
		}
	}
	pages := PagesFor(req.Diff)
	if len(pages) == 0 {
		return tickets.ReviewResult{Approved: true, Summary: "Aucune page modifiée : pas de relecture visuelle."}, nil
	}
	dir, err := os.MkdirTemp(r.WorkDir, "jarvis-visual-")
	if err != nil {
		return tickets.ReviewResult{}, err
	}
	defer os.RemoveAll(dir)

	step("Compilation de la version du ticket")
	after := filepath.Join(dir, "jarvisapp-ticket")
	if out, err := r.Build(ctx, req.Dir, after); err != nil {
		return tickets.ReviewResult{}, fmt.Errorf("visual: compilation de la version du ticket : %v\n%s", err, out)
	}
	beforeURL, stopBefore, err := r.Launch(ctx, r.BeforeBinary)
	if err != nil {
		return tickets.ReviewResult{}, fmt.Errorf("visual: lancement de la version en service : %w", err)
	}
	defer stopBefore()
	afterURL, stopAfter, err := r.Launch(ctx, after)
	if err != nil {
		return tickets.ReviewResult{}, fmt.Errorf("visual: lancement de la version du ticket : %w", err)
	}
	defer stopAfter()

	var captures []Capture
	for i, page := range pages {
		step("Capture de " + page)
		var c Capture
		c.Page = page
		for _, v := range []struct {
			base string
			dst  *[]byte
			name string
		}{{beforeURL, &c.Before, "avant"}, {afterURL, &c.After, "apres"}} {
			out := filepath.Join(dir, fmt.Sprintf("%d-%s.png", i, v.name))
			if err := r.Capturer.Capture(ctx, strings.TrimRight(v.base, "/")+page, out); err != nil {
				return tickets.ReviewResult{}, fmt.Errorf("visual: capture de %s (%s) : %w", page, v.name, err)
			}
			if *v.dst, err = os.ReadFile(out); err != nil {
				return tickets.ReviewResult{}, err
			}
		}
		captures = append(captures, c)
	}

	step(fmt.Sprintf("Jugement de %d page(s) par le modèle de vision", len(captures)))
	res, err := r.Judge.Judge(ctx, JudgeRequest{Title: req.Title, Need: req.Need, Acceptance: req.Acceptance, Captures: captures})
	if err != nil {
		return tickets.ReviewResult{}, err
	}
	for _, c := range captures {
		res.Captures = append(res.Captures, tickets.ReviewCapture{Page: c.Page, Before: c.Before, After: c.After})
	}
	return res, nil
}

// Chrome capture une page avec Chrome sans interface, dans un profil
// temporaire (jamais celui de l'utilisateur).
type Chrome struct {
	Path          string // binaire de Chrome
	Width, Height int    // 0 : 1400 × 900
}

func (c Chrome) Capture(ctx context.Context, url, out string) error {
	w, h := c.Width, c.Height
	if w <= 0 || h <= 0 {
		w, h = 1400, 900
	}
	profile, err := os.MkdirTemp("", "jarvis-chrome-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(profile)
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var logs strings.Builder
	cmd := exec.CommandContext(ctx, c.Path, "--headless=new", "--disable-gpu", "--hide-scrollbars", "--no-first-run",
		"--no-default-browser-check", "--disable-extensions", "--disable-component-update", "--disable-background-networking",
		"--user-data-dir="+profile, fmt.Sprintf("--window-size=%d,%d", w, h), "--virtual-time-budget=3000",
		"--screenshot="+out, url)
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("chrome : %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// Vu en réel : avec un profil neuf, Chrome écrit la capture en une
	// seconde puis ne se termine pas. On attend la capture (taille stable),
	// puis on l'arrête.
	last := int64(-1)
	for {
		select {
		case err := <-done:
			if info, statErr := os.Stat(out); statErr == nil && info.Size() > 0 {
				return nil
			}
			return fmt.Errorf("chrome : aucune capture pour %s (%v) : %s", url, err, tail(logs.String(), 500))
		case <-ctx.Done():
			return fmt.Errorf("chrome : délai dépassé pour %s : %s", url, tail(logs.String(), 500))
		case <-time.After(300 * time.Millisecond):
		}
		if info, err := os.Stat(out); err == nil && info.Size() > 0 {
			if info.Size() == last {
				cmd.Process.Kill()
				<-done
				return nil
			}
			last = info.Size()
		}
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
