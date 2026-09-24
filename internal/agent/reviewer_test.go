package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// Jalon 36 : le relecteur lit en lecture seule autour du diff, puis rend
// son verdict par submit_review.

func reviewRequest(dir string) tickets.ReviewRequest {
	return tickets.ReviewRequest{Title: "Déplacer le filtre date", Need: "Sous la barre de recherche", Plan: "## Étapes\n1. x", Diff: "+++ b/cmd/app/templates/page.templ\n+<div class=\"date-filters\">", Dir: dir}
}

func TestReview_ReadsThenSubmitsVerdict(t *testing.T) {
	model := &scriptedModel{replies: []Message{
		call("r", "read_file", `{"path": "internal/store/store.go"}`),
		call("v", "submit_review", `{"verdict": "a_reprendre", "summary": "Hors du formulaire.", "issues": [{"file": "cmd/app/templates/page.templ", "line": 12, "severity": "bloquant", "message": "remets les champs dans le <form>"}]}`),
	}}
	r := &Reviewer{Model: model}
	res, err := r.Review(context.Background(), reviewRequest(writeRepo(t)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Approved || res.Summary != "Hors du formulaire." || len(res.Issues) != 1 || res.Issues[0].Line != 12 || res.Issues[0].Severity != "bloquant" {
		t.Errorf("result = %+v", res)
	}
	for _, s := range model.toolSpecs[0] {
		if s.Name == "edit_file" || s.Name == "write_file" || s.Name == "propose_plan" {
			t.Errorf("reviewer offered %s", s.Name)
		}
	}
	user := model.calls[0][1].Content
	if !strings.Contains(user, "Sous la barre de recherche") || !strings.Contains(user, "date-filters") {
		t.Errorf("user prompt lacks the need or the diff:\n%s", user)
	}
}

// Un « acceptable » avec une remarque bloquante n'est pas une acceptation.
func TestReview_BlockingIssueIsNeverApproved(t *testing.T) {
	model := &scriptedModel{replies: []Message{
		call("v", "submit_review", `{"verdict": "acceptable", "summary": "ok", "issues": [{"file": "a", "line": 1, "severity": "bloquant", "message": "casse le filtre"}]}`),
	}}
	res, _ := (&Reviewer{Model: model}).Review(context.Background(), reviewRequest(writeRepo(t)), nil)
	if res.Approved {
		t.Error("approved despite a blocking issue")
	}
	model = &scriptedModel{replies: []Message{call("v", "submit_review", `{"verdict": "acceptable", "summary": "Répond au ticket."}`)}}
	if res, _ := (&Reviewer{Model: model}).Review(context.Background(), reviewRequest(writeRepo(t)), nil); !res.Approved {
		t.Error("clean acceptable not approved")
	}
}

// Verdict mal formé : renvoyé au modèle pour correction.
func TestReview_MalformedVerdictIsSentBack(t *testing.T) {
	model := &scriptedModel{replies: []Message{
		call("v1", "submit_review", `{"summary": "sans verdict"}`),
		call("v2", "submit_review", `{"verdict": "acceptable", "summary": "ok"}`),
	}}
	res, err := (&Reviewer{Model: model}).Review(context.Background(), reviewRequest(writeRepo(t)), nil)
	if err != nil || !res.Approved || len(model.calls) != 2 {
		t.Errorf("res = %+v, err = %v, calls = %d", res, err, len(model.calls))
	}
}

// Les standards sont les consignes du relecteur, modifiables (onglet
// Agents) ; par défaut, ceux du projet.
func TestReview_StandardsAreItsInstructions(t *testing.T) {
	model := &scriptedModel{replies: []Message{call("v", "submit_review", `{"verdict": "acceptable", "summary": "ok"}`)}}
	(&Reviewer{Model: model, Instructions: func() string { return "STANDARDS MAISON" }}).Review(context.Background(), reviewRequest(writeRepo(t)), nil)
	if sys := model.calls[0][0].Content; !strings.HasPrefix(sys, "STANDARDS MAISON") {
		t.Errorf("system prompt = %q", sys)
	}
	for _, want := range []string{"submit_review", "<form>", "TDD"} {
		if !strings.Contains(DefaultReviewPrompt, want) {
			t.Errorf("default standards lack %q", want)
		}
	}
}
