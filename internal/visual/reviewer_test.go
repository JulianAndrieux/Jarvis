package visual

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

type fakeCapturer struct {
	mu   sync.Mutex
	urls []string
}

// Capture écrit un faux PNG qui dit quelle adresse a été capturée.
func (f *fakeCapturer) Capture(ctx context.Context, url, out string) error {
	f.mu.Lock()
	f.urls = append(f.urls, url)
	f.mu.Unlock()
	return os.WriteFile(out, []byte("PNG "+url), 0o644)
}

type fakeJudge struct{ got JudgeRequest }

func (f *fakeJudge) Judge(ctx context.Context, req JudgeRequest) (tickets.ReviewResult, error) {
	f.got = req
	return tickets.ReviewResult{Approved: false, Summary: "À droite au lieu d'en dessous."}, nil
}

func newReviewer(t *testing.T) (*Reviewer, *fakeCapturer, *fakeJudge, *[]string) {
	var events []string
	cap, judge := &fakeCapturer{}, &fakeJudge{}
	r := &Reviewer{
		Build: func(ctx context.Context, src, out string) (string, error) {
			events = append(events, "build "+src)
			return "", os.WriteFile(out, []byte("bin"), 0o755)
		},
		Launch: func(ctx context.Context, binary string) (string, func(), error) {
			name := "after"
			if binary == "/bin/en-service" {
				name = "before"
			}
			events = append(events, "launch "+name)
			return "http://" + name, func() { events = append(events, "stop "+name) }, nil
		},
		BeforeBinary: "/bin/en-service",
		Capturer:     cap,
		Judge:        judge,
		WorkDir:      t.TempDir(),
	}
	return r, cap, judge, &events
}

func TestReviewer_CapturesBeforeAfterAndJudges(t *testing.T) {
	r, cap, judge, events := newReviewer(t)
	req := tickets.ReviewRequest{Title: "Déplacer", Need: "Sous la barre", Dir: "/wt/ticket", Diff: "+++ b/cmd/jarvisapp/templates/documents.templ\n+<div>"}
	res, err := r.Review(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cap.urls, ","); got != "http://before/documents,http://after/documents" {
		t.Errorf("captured = %s", got)
	}
	if len(judge.got.Captures) != 1 || string(judge.got.Captures[0].Before) != "PNG http://before/documents" || string(judge.got.Captures[0].After) != "PNG http://after/documents" || judge.got.Need != "Sous la barre" {
		t.Errorf("judge request = %+v", judge.got)
	}
	if res.Approved || len(res.Captures) != 1 || res.Captures[0].Page != "/documents" || len(res.Captures[0].After) == 0 {
		t.Errorf("result = %+v", res)
	}
	joined := strings.Join(*events, ",")
	if !strings.HasPrefix(joined, "build /wt/ticket") || !strings.Contains(joined, "stop before") || !strings.Contains(joined, "stop after") {
		t.Errorf("events = %s, want a build from the ticket copy and both instances stopped", joined)
	}
}

// Aucun gabarit modifié : rien à voir, acceptable sans rien lancer.
func TestReviewer_NoPageChanged(t *testing.T) {
	r, _, _, events := newReviewer(t)
	res, err := r.Review(context.Background(), tickets.ReviewRequest{Diff: "+++ b/internal/webapp/jobs.go\n+x"}, nil)
	if err != nil || !res.Approved || !strings.Contains(res.Summary, "pas de relecture visuelle") || len(*events) != 0 {
		t.Errorf("res = %+v, err = %v, events = %v", res, err, *events)
	}
}

// Compilation impossible : l'erreur remonte, rien n'est lancé.
func TestReviewer_BuildFailure(t *testing.T) {
	r, _, _, events := newReviewer(t)
	r.Build = func(ctx context.Context, src, out string) (string, error) {
		return "undefined: x", errors.New("exit 1")
	}
	_, err := r.Review(context.Background(), tickets.ReviewRequest{Diff: "+++ b/cmd/jarvisapp/templates/notes.templ\n"}, nil)
	if err == nil || !strings.Contains(err.Error(), "undefined: x") || len(*events) != 0 {
		t.Errorf("err = %v, events = %v", err, *events)
	}
}
