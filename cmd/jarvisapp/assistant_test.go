package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/assistant"
	"github.com/JulianAndrieux/Jarvis/internal/claudeapi"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Jalon 57 : l'onglet Assistant — fil de discussion, étapes d'outils
// visibles, mémoire, et rien d'actif tant qu'aucune clé n'est fournie.

func newAssistantServer(t *testing.T, model *claudeapi.FakeClient, tools ...assistant.Tool) (*Server, *assistant.Assistant, *assistant.FakeStore) {
	t.Helper()
	s, _ := newTestServer(t, &blockingRunner{})
	store := assistant.NewFakeStore()
	a := &assistant.Assistant{
		Model: model, Store: store, Tools: tools,
		Instructions: func() string { return "Tu es Jarvis." },
		Now:          func() time.Time { return time.Date(2026, 11, 11, 14, 30, 0, 0, time.Local) },
		DefaultScope: tenancy.LocalScope(), DailyTokens: 2_000_000,
	}
	t.Cleanup(a.Wait)
	s.Assistant = a
	s.AssistantInfo = AssistantInfo{Model: "claude-test", Sources: []string{"notes", "tâches"}}
	return s, a, store
}

func answer(text string) claudeapi.Response {
	return claudeapi.Response{Text: text, Raw: json.RawMessage(`{"role":"assistant"}`), Stop: claudeapi.StopEnd, Usage: claudeapi.Usage{Input: 1200, Output: 34}}
}

func TestAssistant_DisabledExplainsHowToEnable(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})
	s.AssistantInfo.Disabled = "Aucune clé d'API n'est configurée."
	rec := do(s, http.MethodGet, "/assistant", nil)
	page := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(page, "n&#39;est pas en service") && !strings.Contains(page, "n'est pas en service") {
		t.Fatalf("page = %d\n%s", rec.Code, page)
	}
	for _, want := range []string{"Aucune clé d", "assistant.json", `href="/assistant" class="active"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page sans %q", want)
		}
	}
	if strings.Contains(page, `action="/assistant"`) {
		t.Error("formulaire d'envoi affiché alors que l'assistant est désactivé")
	}
	// Rien n'est accepté tant qu'il est désactivé.
	if rec := do(s, http.MethodPost, "/assistant", url.Values{"text": {"bonjour"}}); rec.Code != http.StatusNotFound {
		t.Errorf("POST désactivé = %d, veut 404", rec.Code)
	}
}

func TestAssistant_NewConversationThenThread(t *testing.T) {
	model := &claudeapi.FakeClient{Responses: []claudeapi.Response{answer("Bonjour **Julian**.\n\n<script>alert(1)</script>")}}
	s, a, _ := newAssistantServer(t, model)

	home := do(s, http.MethodGet, "/assistant", nil).Body.String()
	for _, want := range []string{`action="/assistant"`, "notes, tâches", "claude-test", "0 jeton"} {
		if !strings.Contains(home, want) {
			t.Errorf("accueil sans %q", want)
		}
	}

	rec := do(s, http.MethodPost, "/assistant", url.Values{"text": {"Bonjour <b>toi</b>"}})
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/assistant/") {
		t.Fatalf("création = %d, Location %q", rec.Code, loc)
	}
	a.Wait()
	page := do(s, http.MethodGet, loc, nil).Body.String()
	for _, want := range []string{"Bonjour &lt;b&gt;toi&lt;/b&gt;", "<strong>Julian</strong>", "&lt;script&gt;", `hx-post="` + loc + `/messages"`, "14:30"} {
		if !strings.Contains(page, want) {
			t.Errorf("fil sans %q", want)
		}
	}
	if strings.Contains(page, "<script>alert(1)") || strings.Contains(page, "<b>toi</b>") {
		t.Error("du HTML d'un message a été interprété")
	}
	// Au repos, le fil ne se sonde plus : une saisie en cours n'est jamais effacée.
	if strings.Contains(page, `hx-trigger="every`) {
		t.Error("le fil au repos se sonde encore")
	}
	// La conversation est dans la liste, et la consommation est comptée.
	home = do(s, http.MethodGet, "/assistant", nil).Body.String()
	if !strings.Contains(home, "Bonjour &lt;b&gt;toi&lt;/b&gt;") || !strings.Contains(home, "1 234 jetons") {
		t.Errorf("accueil après un tour :\n%s", home)
	}
}

func TestAssistant_RunningThreadPollsAndShowsSteps(t *testing.T) {
	release := make(chan struct{})
	model := &claudeapi.FakeClient{
		Responses: []claudeapi.Response{
			{Text: "Je cherche.", Raw: json.RawMessage(`{"role":"assistant"}`), Stop: claudeapi.StopToolUse,
				Calls: []claudeapi.Call{{ID: "t1", Name: "search_notes", Input: json.RawMessage(`{"query":"impôts"}`)}, {ID: "t2", Name: "casse", Input: json.RawMessage(`{}`)}}},
			answer("Voilà."),
		},
		Hook: func(n int, _ claudeapi.Request) {
			if n == 1 {
				<-release
			}
		},
	}
	s, a, store := newAssistantServer(t, model,
		assistant.Tool{Name: "search_notes", Level: assistant.LevelRead, Run: func(context.Context, json.RawMessage) (string, error) {
			return "1 note : <i>Déclaration</i>", nil
		}},
		assistant.Tool{Name: "casse", Level: assistant.LevelRead, Run: func(context.Context, json.RawMessage) (string, error) {
			return "", context.DeadlineExceeded
		}},
	)
	loc := do(s, http.MethodPost, "/assistant", url.Values{"text": {"Mes impôts ?"}}).Header().Get("Location")
	id := strings.TrimPrefix(loc, "/assistant/")
	// Attendre que le premier tour d'outils soit écrit.
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, _, _ := store.Get(context.Background(), id)
		if len(c.Entries) >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("les étapes d'outils n'arrivent pas")
		}
		time.Sleep(5 * time.Millisecond)
	}
	frag := do(s, http.MethodGet, loc+"/thread", nil).Body.String()
	for _, want := range []string{`hx-get="` + loc + `/thread"`, `hx-trigger="every 1500ms"`, "Jarvis réfléchit", "Je cherche.",
		"search_notes", "impôts", "1 note : &lt;i&gt;Déclaration&lt;/i&gt;", "tool-error", "casse"} {
		if !strings.Contains(frag, want) {
			t.Errorf("fragment en cours sans %q :\n%s", want, frag)
		}
	}
	if strings.Contains(frag, "<html") || strings.Contains(frag, `hx-post=`) {
		t.Error("le fragment en cours ne doit être ni une page ni proposer la saisie")
	}
	// Un envoi pendant le tour est refusé, et le texte est rendu.
	busy := do(s, http.MethodPost, loc+"/messages", url.Values{"text": {"et la TVA ?"}})
	if busy.Code != http.StatusConflict {
		t.Errorf("envoi pendant un tour = %d, veut 409", busy.Code)
	}
	close(release)
	a.Wait()
	done := do(s, http.MethodGet, loc+"/thread", nil).Body.String()
	if !strings.Contains(done, "Voilà.") || strings.Contains(done, "hx-trigger") || !strings.Contains(done, `hx-post="`+loc+`/messages"`) {
		t.Errorf("fragment terminé :\n%s", done)
	}
}

func TestAssistant_FollowUpMessageAndFailure(t *testing.T) {
	model := &claudeapi.FakeClient{Responses: []claudeapi.Response{answer("Un.")}, Errs: []error{nil, claudeapi.ErrAuth}}
	s, a, _ := newAssistantServer(t, model)
	loc := do(s, http.MethodPost, "/assistant", url.Values{"text": {"premier"}}).Header().Get("Location")
	a.Wait()
	rec := do(s, http.MethodPost, loc+"/messages", url.Values{"text": {"second"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "second") || !strings.Contains(rec.Body.String(), `id="thread"`) {
		t.Fatalf("suite = %d\n%s", rec.Code, rec.Body.String())
	}
	a.Wait()
	frag := do(s, http.MethodGet, loc+"/thread", nil).Body.String()
	if !strings.Contains(frag, "clé d") || !strings.Contains(frag, "thread-error") || !strings.Contains(frag, `hx-post=`) {
		t.Errorf("échec non affiché, ou saisie absente après l'échec :\n%s", frag)
	}
	if rec := do(s, http.MethodPost, loc+"/messages", url.Values{"text": {"  "}}); rec.Code != http.StatusBadRequest {
		t.Errorf("message vide = %d, veut 400", rec.Code)
	}
}

func TestAssistant_UnknownConversationIs404(t *testing.T) {
	s, _, _ := newAssistantServer(t, &claudeapi.FakeClient{})
	for _, target := range []string{"/assistant/absente", "/assistant/absente/thread"} {
		if rec := do(s, http.MethodGet, target, nil); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, veut 404", target, rec.Code)
		}
	}
	for _, target := range []string{"/assistant/absente/messages", "/assistant/absente/delete", "/assistant/memory/absent/forget"} {
		if rec := do(s, http.MethodPost, target, url.Values{"text": {"x"}}); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d, veut 404", target, rec.Code)
		}
	}
}

func TestAssistant_DeleteAndMemory(t *testing.T) {
	model := &claudeapi.FakeClient{Responses: []claudeapi.Response{answer("ok")}}
	s, a, _ := newAssistantServer(t, model)
	loc := do(s, http.MethodPost, "/assistant", url.Values{"text": {"à supprimer"}}).Header().Get("Location")
	a.Wait()
	f, err := a.Remember(context.Background(), "Le propriétaire s'appelle <M. Weber>")
	if err != nil {
		t.Fatal(err)
	}
	home := do(s, http.MethodGet, "/assistant", nil).Body.String()
	if !strings.Contains(home, "&lt;M. Weber&gt;") || !strings.Contains(home, "/assistant/memory/"+f.ID+"/forget") {
		t.Errorf("mémoire non affichée :\n%s", home)
	}
	if rec := do(s, http.MethodPost, "/assistant/memory/"+f.ID+"/forget", nil); rec.Code != http.StatusSeeOther {
		t.Errorf("oubli = %d", rec.Code)
	}
	if facts, _ := a.Facts(context.Background()); len(facts) != 0 {
		t.Errorf("fait non oublié : %+v", facts)
	}
	if rec := do(s, http.MethodPost, loc+"/delete", nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/assistant" {
		t.Errorf("suppression = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := do(s, http.MethodGet, loc, nil); rec.Code != http.StatusNotFound {
		t.Errorf("conversation supprimée = %d, veut 404", rec.Code)
	}
}

func TestSpaced(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1234: "1 234", 2000000: "2 000 000"} {
		if got := spaced(n); got != want {
			t.Errorf("spaced(%d) = %q, veut %q", n, got, want)
		}
	}
}

func TestLoadAssistantKey(t *testing.T) {
	dir := t.TempDir()
	noEnv := func(string) string { return "" }
	env := func(k string) string {
		if k == "ANTHROPIC_API_KEY" {
			return "sk-env"
		}
		return ""
	}
	write := func(name, content string, perm os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), perm); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// Ni fichier ni variable : désactivé, sans erreur.
	if cfg, err := loadAssistantKey(filepath.Join(dir, "absent.json"), noEnv); err != nil || cfg.APIKey != "" {
		t.Errorf("sans rien = %+v, %v", cfg, err)
	}
	if cfg, err := loadAssistantKey("", noEnv); err != nil || cfg.APIKey != "" {
		t.Errorf("chemin vide = %+v, %v", cfg, err)
	}
	// Le fichier l'emporte sur l'environnement ; son modèle est facultatif.
	good := write("ok.json", `{"api_key":" sk-file ","model":"claude-sonnet-5-5"}`, 0o600)
	if cfg, err := loadAssistantKey(good, env); err != nil || cfg.APIKey != "sk-file" || cfg.Model != "claude-sonnet-5-5" || cfg.Source != good {
		t.Errorf("fichier = %+v, %v", cfg, err)
	}
	if cfg, err := loadAssistantKey(filepath.Join(dir, "absent.json"), env); err != nil || cfg.APIKey != "sk-env" || !strings.Contains(cfg.Source, "ANTHROPIC_API_KEY") {
		t.Errorf("environnement = %+v, %v", cfg, err)
	}
	// Une clé lisible par d'autres comptes de la machine est refusée.
	open := write("open.json", `{"api_key":"sk-file"}`, 0o644)
	if _, err := loadAssistantKey(open, noEnv); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("fichier 0644 = %v", err)
	}
	if _, err := loadAssistantKey(write("bad.json", `{"api_key":`, 0o600), noEnv); err == nil {
		t.Error("JSON invalide accepté")
	}
	if _, err := loadAssistantKey(write("empty.json", `{"model":"x"}`, 0o600), noEnv); err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Errorf("fichier sans clé = %v", err)
	}
}

func TestAssistantTools_SourcesOpenAndClose(t *testing.T) {
	all, err := assistantTools(strings.Join(assistant.AllSources, ","), nil, nil, nil, nil, nil)
	if err != nil || len(all) != 19 {
		t.Fatalf("toutes les sources = %d outils, %v", len(all), err)
	}
	some, err := assistantTools("notes,memory", nil, nil, nil, nil, nil)
	if err != nil || len(some) != 6 {
		t.Fatalf("notes+mémoire = %d outils, %v", len(some), err)
	}
	for _, tool := range some {
		if tool.Source != assistant.SourceNotes && tool.Source != assistant.SourceMemory {
			t.Errorf("outil %s d'une source fermée (%s)", tool.Name, tool.Source)
		}
	}
	if _, err := assistantTools("notes,agenda", nil, nil, nil, nil, nil); err == nil {
		t.Error("source inconnue acceptée")
	}
	if got := sourceLabels("notes,tasks,emails,memory"); strings.Join(got, ", ") != "notes, tâches, emails" {
		t.Errorf("sourceLabels = %v", got)
	}
}

// Jalon 58 : une action attend la réponse de l'utilisateur, dans le fil.
func newConfirmServer(t *testing.T, ran *int, responses ...claudeapi.Response) (*Server, *assistant.Assistant, string) {
	t.Helper()
	model := &claudeapi.FakeClient{Responses: responses}
	s, a, _ := newAssistantServer(t, model, assistant.Tool{
		Name: "delete_task", Level: assistant.LevelAction,
		Confirm: func(context.Context, json.RawMessage) string { return "Supprimer la tâche « Payer <le> loyer »" },
		Run: func(context.Context, json.RawMessage) (string, error) {
			*ran++
			return "Tâche supprimée.", nil
		},
	})
	loc := do(s, http.MethodPost, "/assistant", url.Values{"text": {"supprime la tâche du loyer"}}).Header().Get("Location")
	a.Wait()
	return s, a, loc
}

func actionTurn() claudeapi.Response {
	return claudeapi.Response{Text: "Je la supprime.", Raw: json.RawMessage(`{"role":"assistant"}`), Stop: claudeapi.StopToolUse,
		Calls: []claudeapi.Call{{ID: "t9", Name: "delete_task", Input: json.RawMessage(`{"id":"a1"}`)}}}
}

func TestAssistant_ConfirmationCard(t *testing.T) {
	ran := 0
	s, a, loc := newConfirmServer(t, &ran, actionTurn(), answer("C'est supprimé."))
	page := do(s, http.MethodGet, loc, nil).Body.String()
	for _, want := range []string{"Supprimer la tâche « Payer &lt;le&gt; loyer »", `hx-post="` + loc + `/actions/t9/confirm"`,
		`hx-post="` + loc + `/actions/t9/refuse"`, "Je la supprime.", "à confirmer"} {
		if !strings.Contains(page, want) {
			t.Errorf("page en attente sans %q", want)
		}
	}
	// Ni saisie libre ni sonde : on répond d'abord à la question.
	if strings.Contains(page, `hx-post="`+loc+`/messages"`) || strings.Contains(page, `hx-trigger="every`) || ran != 0 {
		t.Errorf("page en attente : saisie ou sonde présente, ou action déjà faite (%d)", ran)
	}
	if rec := do(s, http.MethodPost, loc+"/messages", url.Values{"text": {"autre chose"}}); rec.Code != http.StatusConflict {
		t.Errorf("message pendant une attente = %d, veut 409", rec.Code)
	}

	rec := do(s, http.MethodPost, loc+"/actions/t9/confirm", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="thread"`) {
		t.Fatalf("confirmation = %d\n%s", rec.Code, rec.Body.String())
	}
	a.Wait()
	done := do(s, http.MethodGet, loc+"/thread", nil).Body.String()
	if ran != 1 || !strings.Contains(done, "C&#39;est supprimé.") && !strings.Contains(done, "C'est supprimé.") || !strings.Contains(done, "Tâche supprimée.") {
		t.Errorf("après confirmation : exécutions %d\n%s", ran, done)
	}
	// Un second clic ne refait rien.
	if rec := do(s, http.MethodPost, loc+"/actions/t9/confirm", nil); rec.Code != http.StatusConflict || ran != 1 {
		t.Errorf("second clic = %d, exécutions %d", rec.Code, ran)
	}
}

func TestAssistant_RefuseAction(t *testing.T) {
	ran := 0
	s, a, loc := newConfirmServer(t, &ran, actionTurn(), answer("Entendu, je la garde."))
	if rec := do(s, http.MethodPost, loc+"/actions/t9/refuse", nil); rec.Code != http.StatusOK {
		t.Fatalf("refus = %d", rec.Code)
	}
	a.Wait()
	done := do(s, http.MethodGet, loc+"/thread", nil).Body.String()
	if ran != 0 || !strings.Contains(done, "Entendu, je la garde.") || !strings.Contains(done, "refusée") {
		t.Errorf("après refus : exécutions %d\n%s", ran, done)
	}
	for _, target := range []string{"/assistant/absente/actions/t9/confirm", "/assistant/absente/actions/t9/refuse"} {
		if rec := do(s, http.MethodPost, target, nil); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d, veut 404", target, rec.Code)
		}
	}
}
