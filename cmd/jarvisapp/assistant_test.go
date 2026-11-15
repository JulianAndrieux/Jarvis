package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/assistant"
	"github.com/JulianAndrieux/Jarvis/internal/notes"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Jalon 55 : l'assistant — poser une question, suivre la réponse, voir ce
// qu'il a fait.

// scriptedModel joue Claude : script reçoit la demande et peut appeler les
// outils.
type scriptedModel struct {
	script func(ctx context.Context, req assistant.Request) (assistant.Reply, error)
}

func (m *scriptedModel) Run(ctx context.Context, req assistant.Request) (assistant.Reply, error) {
	return m.script(ctx, req)
}

func modelTool(ctx context.Context, req assistant.Request, name, args string) error {
	for _, tool := range req.Tools {
		if tool.Name == name {
			_, err := tool.Call(ctx, json.RawMessage(args))
			return err
		}
	}
	return errors.New("outil absent : " + name)
}

func newAssistantServer(t *testing.T, script func(ctx context.Context, req assistant.Request) (assistant.Reply, error)) (*Server, *assistant.Service, *notes.FakeStore) {
	t.Helper()
	s, _ := newTestServer(t, &blockingRunner{})
	noteStore := notes.NewFakeStore()
	s.Notes = &notes.Service{Store: noteStore}
	svc := &assistant.Service{
		Store: assistant.NewFakeStore(), Runner: &scriptedModel{script: script},
		Tools: &assistant.Toolbox{Notes: s.Notes, Jobs: s.Jobs}, DefaultScope: tenancy.LocalScope(),
	}
	s.Assistant = svc
	t.Cleanup(svc.Wait)
	return s, svc, noteStore
}

func TestAssistant_DisabledWithoutService(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})
	for _, path := range []string{"/assistant", "/assistant/abc", "/assistant/abc/thread"} {
		if rec := do(s, http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s sans assistant = %d, veut 404", path, rec.Code)
		}
	}
	if rec := do(s, http.MethodPost, "/assistant", url.Values{"text": {"q"}}); rec.Code != http.StatusNotFound {
		t.Errorf("POST /assistant sans assistant = %d", rec.Code)
	}
}

func TestAssistant_PageAndNavTab(t *testing.T) {
	s, _, _ := newAssistantServer(t, nil)
	rec := do(s, http.MethodGet, "/assistant", nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("/assistant = %d", rec.Code)
	}
	for _, want := range []string{`action="/assistant"`, `name="text"`, "Aucune conversation", "API d'Anthropic", `href="/assistant" class="active"`, "theme-user"} {
		if !strings.Contains(body, want) {
			t.Errorf("page sans %q", want)
		}
	}
	// L'onglet est proposé depuis les autres pages de l'application.
	if !strings.Contains(do(s, http.MethodGet, "/tasks", nil).Body.String(), `href="/assistant"`) {
		t.Error("onglet Assistant absent de la navigation")
	}
}

func TestAssistant_AskShowsAnswerAndWhatWasDone(t *testing.T) {
	s, svc, noteStore := newAssistantServer(t, func(ctx context.Context, req assistant.Request) (assistant.Reply, error) {
		if err := modelTool(ctx, req, "list_tasks", `{}`); err != nil {
			return assistant.Reply{}, err
		}
		if err := modelTool(ctx, req, "create_task", `{"title":"Rappeler le plombier","due":"2026-12-01"}`); err != nil {
			return assistant.Reply{}, err
		}
		modelTool(ctx, req, "read_note", `{"id":"absente"}`)
		return assistant.Reply{Text: "C'est fait : **tâche créée** pour le 1er décembre.\n\n<script>alert(1)</script>", CostUSD: 0.081}, nil
	})
	rec := do(s, http.MethodPost, "/assistant", url.Values{"text": {"Rappelle-moi le plombier <b>lundi</b>"}})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/assistant/") {
		t.Fatalf("POST /assistant = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	path := rec.Header().Get("Location")
	svc.Wait()

	body := do(s, http.MethodGet, path, nil).Body.String()
	for _, want := range []string{
		"Rappelle-moi le plombier &lt;b&gt;lundi&lt;/b&gt;", // la question, comme du texte
		"<strong>tâche créée</strong>",                      // la réponse, en Markdown
		"&lt;script&gt;alert(1)&lt;/script&gt;",             // … échappé
		`<a href="/tasks">Tâche créée : Rappeler le plombier (2026-12-01)</a>`,
		"1 lecture(s) pour répondre",
		"read_note", "introuvable", // l'échec d'un outil est montré
		"≈ 0,08 $",
		`action="` + path + `/messages"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("conversation sans %q", want)
		}
	}
	if strings.Contains(body, "<script>alert(1)") {
		t.Error("la réponse du modèle est interprétée comme du HTML")
	}
	// La réponse est là : la page ne se recharge plus.
	if strings.Contains(body, "hx-trigger") && strings.Contains(body, `hx-get="`+path+`/thread"`) {
		t.Error("le fil se recharge encore après la réponse")
	}
	// L'action a eu lieu dans les données de l'utilisateur.
	tasks, _ := noteStore.ListTasks(context.Background(), notes.TaskQuery{})
	if len(tasks) != 1 || tasks[0].Title != "Rappeler le plombier" {
		t.Errorf("tâches = %+v", tasks)
	}
	// Et la conversation figure dans la liste.
	if list := do(s, http.MethodGet, "/assistant", nil).Body.String(); !strings.Contains(list, `href="`+path+`"`) || !strings.Contains(list, "Rappelle-moi le plombier") {
		t.Error("conversation absente de la liste")
	}
}

// Pendant la réponse : le fil se recharge, montre ce qui est déjà fait, et
// refuse une seconde question.
func TestAssistant_WhileAnswering(t *testing.T) {
	stepDone, release := make(chan struct{}), make(chan struct{})
	s, svc, _ := newAssistantServer(t, func(ctx context.Context, req assistant.Request) (assistant.Reply, error) {
		modelTool(ctx, req, "list_tasks", `{}`)
		close(stepDone)
		<-release
		return assistant.Reply{Text: "voilà"}, nil
	})
	path := do(s, http.MethodPost, "/assistant", url.Values{"text": {"mes tâches ?"}}).Header().Get("Location")
	<-stepDone

	frag := do(s, http.MethodGet, path+"/thread", nil).Body.String()
	for _, want := range []string{`hx-get="` + path + `/thread"`, `hx-trigger="load delay:2s"`, "assistant cherche", "Tâches : 0"} {
		if !strings.Contains(frag, want) {
			t.Errorf("fil en cours sans %q :\n%s", want, frag)
		}
	}
	if strings.Contains(frag, "<html") || strings.Contains(frag, "<textarea") {
		t.Error("le fragment en cours porte la page entière ou une zone de saisie")
	}
	if list := do(s, http.MethodGet, "/assistant", nil).Body.String(); !strings.Contains(list, "réponse en cours") {
		t.Error("la liste ne dit pas qu'une réponse est en cours")
	}
	rec := do(s, http.MethodPost, path+"/messages", url.Values{"text": {"et demain ?"}})
	if rec.Code != http.StatusConflict {
		t.Errorf("seconde question pendant la réponse = %d, veut 409", rec.Code)
	}
	close(release)
	svc.Wait()

	frag = do(s, http.MethodGet, path+"/thread", nil).Body.String()
	if strings.Contains(frag, "hx-get") || !strings.Contains(frag, "voilà") || !strings.Contains(frag, "<textarea") {
		t.Errorf("fil terminé :\n%s", frag)
	}
	// La conversation se poursuit.
	if rec := do(s, http.MethodPost, path+"/messages", url.Values{"text": {"et demain ?"}}); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != path {
		t.Errorf("question suivante = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAssistant_FailedAnswerIsShown(t *testing.T) {
	s, svc, _ := newAssistantServer(t, func(context.Context, assistant.Request) (assistant.Reply, error) {
		return assistant.Reply{}, errors.New("claude code : lancement impossible")
	})
	path := do(s, http.MethodPost, "/assistant", url.Values{"text": {"q"}}).Header().Get("Location")
	svc.Wait()
	body := do(s, http.MethodGet, path, nil).Body.String()
	if !strings.Contains(body, "a pas abouti") || !strings.Contains(body, "lancement impossible") || !strings.Contains(body, "<textarea") {
		t.Errorf("échec mal affiché :\n%s", body)
	}
}

func TestAssistant_RefusedQuestionKeepsTheDraft(t *testing.T) {
	s, svc, _ := newAssistantServer(t, func(context.Context, assistant.Request) (assistant.Reply, error) {
		return assistant.Reply{Text: "ok"}, nil
	})
	rec := do(s, http.MethodPost, "/assistant", url.Values{"text": {"   "}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "la question est vide") {
		t.Errorf("question vide = %d", rec.Code)
	}
	long := strings.Repeat("a", assistant.MaxQuestion+1)
	rec = do(s, http.MethodPost, "/assistant", url.Values{"text": {long}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), long[:200]) {
		t.Errorf("question trop longue = %d, saisie rendue : %v", rec.Code, strings.Contains(rec.Body.String(), long[:200]))
	}
	path := do(s, http.MethodPost, "/assistant", url.Values{"text": {"q"}}).Header().Get("Location")
	svc.Wait()
	if rec := do(s, http.MethodPost, path+"/messages", url.Values{"text": {""}}); rec.Code != http.StatusBadRequest {
		t.Errorf("suite vide = %d", rec.Code)
	}
}

func TestAssistant_UnknownConversationIs404(t *testing.T) {
	s, _, _ := newAssistantServer(t, nil)
	for _, path := range []string{"/assistant/inconnue", "/assistant/inconnue/thread"} {
		if rec := do(s, http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d", path, rec.Code)
		}
	}
	if rec := do(s, http.MethodPost, "/assistant/inconnue/messages", url.Values{"text": {"q"}}); rec.Code != http.StatusNotFound {
		t.Errorf("question dans une conversation inconnue = %d", rec.Code)
	}
}

func TestAssistant_Delete(t *testing.T) {
	s, svc, _ := newAssistantServer(t, func(context.Context, assistant.Request) (assistant.Reply, error) {
		return assistant.Reply{Text: "ok"}, nil
	})
	path := do(s, http.MethodPost, "/assistant", url.Values{"text": {"à supprimer"}}).Header().Get("Location")
	svc.Wait()
	rec := do(s, http.MethodPost, path+"/delete", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/assistant" {
		t.Errorf("suppression = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := do(s, http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
		t.Errorf("conversation supprimée = %d", rec.Code)
	}
	// Supprimer deux fois ne casse rien.
	if rec := do(s, http.MethodPost, path+"/delete", nil); rec.Code != http.StatusSeeOther {
		t.Errorf("seconde suppression = %d", rec.Code)
	}
}

// Une réponse restée « en cours » trop longtemps (Jarvis a redémarré) est
// dite interrompue, et la saisie redevient possible.
func TestThreadView_StaleRunIsShownInterrupted(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	c := assistant.Conversation{ID: "c1", Title: "t", Running: true, RunStartedAt: now.Add(-3 * assistant.DefaultTimeout),
		Messages: []assistant.Message{{Role: assistant.RoleUser, Text: "q", At: now.Add(-3 * assistant.DefaultTimeout)}},
		Live:     []assistant.Step{{Tool: "list_tasks", Summary: "Tâches : 2", Link: "//evil.example/x"}}}
	v := threadView(c, now, assistant.DefaultTimeout)
	if v.Running || len(v.Messages) != 2 || !strings.Contains(v.Messages[1].Error, "interrompue par un redémarrage") {
		t.Fatalf("vue = %+v", v)
	}
	// Un lien qui n'est pas un chemin local n'est pas rendu cliquable.
	if got := v.Messages[1].Steps[0]; got.Link != "" || got.Summary != "Tâches : 2" {
		t.Errorf("étape = %+v", got)
	}
}
