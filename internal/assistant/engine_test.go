package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/JulianAndrieux/Jarvis/internal/claudeapi"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

var testNow = time.Date(2026, 11, 11, 14, 30, 0, 0, time.UTC)

func newAssistant(model claudeapi.Client, tools ...Tool) (*Assistant, *FakeStore) {
	store := NewFakeStore()
	return &Assistant{
		Model: model, Store: store, Tools: tools,
		Instructions: func() string { return "Tu es Jarvis." },
		Now:          func() time.Time { return testNow },
		DefaultScope: tenancy.LocalScope(),
	}, store
}

func call(id, name, input string) claudeapi.Call {
	return claudeapi.Call{ID: id, Name: name, Input: json.RawMessage(input)}
}

func toolTurn(text string, calls ...claudeapi.Call) claudeapi.Response {
	return claudeapi.Response{Text: text, Calls: calls, Raw: json.RawMessage(`{"role":"assistant","t":"` + text + `"}`),
		Stop: claudeapi.StopToolUse, Usage: claudeapi.Usage{Input: 100, Output: 10}, Model: "claude-test"}
}

func endTurn(text string) claudeapi.Response {
	return claudeapi.Response{Text: text, Raw: json.RawMessage(`{"role":"assistant","t":"` + text + `"}`),
		Stop: claudeapi.StopEnd, Usage: claudeapi.Usage{Input: 50, Output: 5}, Model: "claude-test"}
}

func echoTool(name string, level Level, out string, seen *[]string) Tool {
	return Tool{Name: name, Description: "d", Level: level, Schema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, in json.RawMessage) (string, error) {
			if seen != nil {
				sc, _ := tenancy.FromContext(ctx)
				*seen = append(*seen, string(sc.Env)+"/"+string(sc.User)+":"+string(in))
			}
			return out, nil
		}}
}

func ask(t *testing.T, a *Assistant, id, text string) Conversation {
	t.Helper()
	c, err := a.Ask(context.Background(), id, text)
	if err != nil {
		t.Fatalf("Ask : %v", err)
	}
	a.Wait()
	got, ok, err := a.Get(context.Background(), c.ID)
	if err != nil || !ok {
		t.Fatalf("Get : %v %v", ok, err)
	}
	return got
}

func TestAsk_SimpleAnswer(t *testing.T) {
	model := &claudeapi.FakeClient{Responses: []claudeapi.Response{endTurn("Bonjour Julian.")}}
	a, store := newAssistant(model, echoTool("search_notes", LevelRead, "", nil))
	_ = store.AddFact(context.Background(), Fact{ID: "f1", Text: "Julian vit au Luxembourg", At: testNow})

	c := ask(t, a, "", "  Bonjour, qui es-tu ?  ")
	if c.Status != StatusIdle || c.Error != "" || c.Title != "Bonjour, qui es-tu ?" {
		t.Errorf("conversation = %+v", c)
	}
	if len(c.Entries) != 2 || c.Entries[0].Role != RoleUser || c.Entries[0].Text != "Bonjour, qui es-tu ?" ||
		c.Entries[1].Role != RoleAssistant || c.Entries[1].Text != "Bonjour Julian." || c.Entries[1].Model != "claude-test" ||
		len(c.Entries[1].Raw) == 0 {
		t.Fatalf("entrées = %+v", c.Entries)
	}
	req := model.Requests()[0]
	if !strings.HasPrefix(req.System, "Tu es Jarvis.") || !strings.Contains(req.System, "Julian vit au Luxembourg") ||
		!strings.Contains(req.System, "mercredi 11 novembre 2026") {
		t.Errorf("system = %q", req.System)
	}
	// L'heure n'y est pas : elle casserait le cache à chaque minute.
	if strings.Contains(req.System, "14:30") || strings.Contains(req.System, "14 h 30") {
		t.Errorf("l'heure est dans la consigne : %q", req.System)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "search_notes" {
		t.Errorf("tools = %+v", req.Tools)
	}
	if len(req.Messages) != 1 || req.Messages[0].User != "Bonjour, qui es-tu ?" {
		t.Errorf("messages = %+v", req.Messages)
	}
	if u, _ := store.UsageOn(context.Background(), "2026-11-11"); u != (claudeapi.Usage{Input: 50, Output: 5}) {
		t.Errorf("consommation = %+v", u)
	}
}

func TestAsk_ToolLoop(t *testing.T) {
	model := &claudeapi.FakeClient{Responses: []claudeapi.Response{
		toolTurn("Je cherche.", call("t1", "search_notes", `{"query":"impôts"}`), call("t2", "list_tasks", `{}`)),
		endTurn("Voilà."),
	}}
	var seen []string
	a, _ := newAssistant(model, echoTool("search_notes", LevelRead, "1 note", &seen), echoTool("list_tasks", LevelWrite, "2 tâches", &seen))
	c := ask(t, a, "", "Mes impôts ?")

	if c.Status != StatusIdle || len(c.Entries) != 4 {
		t.Fatalf("conversation = %+v", c)
	}
	// Les outils tournent avec la portée de l'utilisateur.
	if !reflect.DeepEqual(seen, []string{`local/local:{"query":"impôts"}`, `local/local:{}`}) {
		t.Errorf("outils appelés = %v", seen)
	}
	res := c.Entries[2]
	if res.Role != RoleTools || len(res.Results) != 2 || res.Results[0] != (claudeapi.Result{CallID: "t1", Content: "1 note"}) ||
		res.Results[1].Content != "2 tâches" {
		t.Errorf("résultats = %+v", res)
	}
	// Second appel : utilisateur, message de Claude rejoué tel quel, tous
	// les résultats dans un seul tour.
	msgs := model.Requests()[1].Messages
	if len(msgs) != 3 || msgs[0].User != "Mes impôts ?" || string(msgs[1].Assistant) != `{"role":"assistant","t":"Je cherche."}` ||
		len(msgs[2].Results) != 2 {
		t.Errorf("messages = %+v", msgs)
	}

	// Tour suivant : tout l'historique, en ajout seul.
	model.Responses = append(model.Responses, endTurn("De rien."))
	c = ask(t, a, c.ID, "Merci")
	if len(c.Entries) != 6 || c.Title != "Mes impôts ?" {
		t.Fatalf("après le second tour = %+v", c)
	}
	msgs = model.Requests()[2].Messages
	if len(msgs) != 5 || msgs[3].Assistant == nil || msgs[4].User != "Merci" {
		t.Errorf("messages du second tour = %+v", msgs)
	}
}

func TestAsk_ToolFailuresAreReportedToTheModel(t *testing.T) {
	model := &claudeapi.FakeClient{Responses: []claudeapi.Response{
		toolTurn("", call("a", "inconnu", `{}`), call("b", "casse", `{}`), call("c", "panique", `{}`), call("d", "casse", `{"x":`)),
		endTurn("Désolé."),
	}}
	a, _ := newAssistant(model,
		Tool{Name: "casse", Level: LevelRead, Run: func(context.Context, json.RawMessage) (string, error) {
			return "", errors.New("base illisible")
		}},
		Tool{Name: "panique", Level: LevelRead, Run: func(context.Context, json.RawMessage) (string, error) { panic("boum") }},
	)
	c := ask(t, a, "", "x")
	if c.Status != StatusIdle {
		t.Fatalf("un outil en échec ne fait pas échouer le tour : %+v", c)
	}
	r := c.Entries[2].Results
	if len(r) != 4 {
		t.Fatalf("résultats = %+v", r)
	}
	for i, want := range []string{"inconnu", "base illisible", "boum", "JSON"} {
		if !r[i].IsError || !strings.Contains(r[i].Content, want) {
			t.Errorf("résultat %d = %+v, veut une erreur contenant %q", i, r[i], want)
		}
	}
}

// Jalon 58 : une action attend la confirmation de l'utilisateur. Le tour
// se met en pause ; les lectures demandées avec elle sont faites.
func actionFixture(t *testing.T, responses ...claudeapi.Response) (*Assistant, *FakeStore, *claudeapi.FakeClient, *[]string) {
	t.Helper()
	model := &claudeapi.FakeClient{Responses: responses}
	seen := &[]string{}
	send := echoTool("send_email", LevelAction, "envoyé", seen)
	send.Confirm = func(_ context.Context, in json.RawMessage) string { return "Envoyer un email : " + string(in) }
	a, store := newAssistant(model, send, echoTool("delete_task", LevelAction, "supprimée", seen), echoTool("search_notes", LevelRead, "1 note", seen))
	return a, store, model, seen
}

func TestAction_PausesUntilConfirmed(t *testing.T) {
	a, _, model, seen := actionFixture(t,
		toolTurn("Je prépare l'envoi.", call("t1", "search_notes", `{"query":"x"}`), call("t2", "send_email", `{"to":"paul"}`)),
		endTurn("C'est envoyé."))
	c := ask(t, a, "", "envoie le devis à Paul")

	if c.Status != StatusWaiting || c.Error != "" {
		t.Fatalf("status = %q (%q), veut waiting", c.Status, c.Error)
	}
	if !reflect.DeepEqual(*seen, []string{`local/local:{"query":"x"}`}) {
		t.Fatalf("outils exécutés avant la confirmation = %v", *seen)
	}
	if len(model.Requests()) != 1 {
		t.Fatalf("le modèle a été rappelé pendant l'attente (%d requêtes)", len(model.Requests()))
	}
	pending := a.Pending(context.Background(), c)
	if len(pending) != 1 || pending[0].CallID != "t2" || pending[0].Tool != "send_email" || pending[0].Summary != `Envoyer un email : {"to":"paul"}` {
		t.Fatalf("pending = %+v", pending)
	}
	// Écrire autre chose ne passe pas devant la confirmation.
	if _, err := a.Ask(context.Background(), c.ID, "au fait…"); !errors.Is(err, ErrWaiting) {
		t.Errorf("Ask pendant une attente = %v, veut ErrWaiting", err)
	}

	if _, err := a.Decide(context.Background(), c.ID, "t2", true); err != nil {
		t.Fatal(err)
	}
	a.Wait()
	c, _, _ = a.Get(context.Background(), c.ID)
	if c.Status != StatusIdle || len(a.Pending(context.Background(), c)) != 0 {
		t.Fatalf("après confirmation = %q, pending %v", c.Status, a.Pending(context.Background(), c))
	}
	if !reflect.DeepEqual(*seen, []string{`local/local:{"query":"x"}`, `local/local:{"to":"paul"}`}) {
		t.Errorf("outils exécutés = %v", *seen)
	}
	// Le modèle reçoit les deux résultats dans un seul tour.
	msgs := model.Requests()[1].Messages
	if len(msgs) != 3 || len(msgs[2].Results) != 2 || msgs[2].Results[0].CallID != "t1" ||
		msgs[2].Results[1] != (claudeapi.Result{CallID: "t2", Content: "envoyé"}) {
		t.Errorf("messages après confirmation = %+v", msgs)
	}
	if last := c.Entries[len(c.Entries)-1]; last.Text != "C'est envoyé." {
		t.Errorf("dernière entrée = %+v", last)
	}
	// Une seconde réponse à la même demande ne refait rien.
	if _, err := a.Decide(context.Background(), c.ID, "t2", true); !errors.Is(err, ErrNotPending) {
		t.Errorf("seconde confirmation = %v, veut ErrNotPending", err)
	}
}

func TestAction_Refused(t *testing.T) {
	a, _, model, seen := actionFixture(t, toolTurn("", call("t2", "send_email", `{}`)), endTurn("Entendu, je n'envoie rien."))
	c := ask(t, a, "", "envoie")
	if _, err := a.Decide(context.Background(), c.ID, "t2", false); err != nil {
		t.Fatal(err)
	}
	a.Wait()
	c, _, _ = a.Get(context.Background(), c.ID)
	if len(*seen) != 0 {
		t.Fatalf("l'action refusée a été exécutée : %v", *seen)
	}
	r := model.Requests()[1].Messages[2].Results[0]
	if !r.IsError || !strings.Contains(r.Content, "refusé") || c.Status != StatusIdle {
		t.Errorf("résultat = %+v, status %q", r, c.Status)
	}
}

// Deux actions demandées ensemble : chacune a sa réponse, et le modèle
// n'est rappelé qu'une fois toutes décidées.
func TestAction_SeveralPending(t *testing.T) {
	a, _, model, seen := actionFixture(t,
		toolTurn("", call("t1", "send_email", `{"n":1}`), call("t2", "delete_task", `{"n":2}`)), endTurn("Fait."))
	c := ask(t, a, "", "x")
	if got := a.Pending(context.Background(), c); len(got) != 2 || got[1].Summary == "" {
		t.Fatalf("pending = %+v", got)
	}
	if _, err := a.Decide(context.Background(), c.ID, "t2", true); err != nil {
		t.Fatal(err)
	}
	a.Wait()
	c, _, _ = a.Get(context.Background(), c.ID)
	if c.Status != StatusWaiting || len(a.Pending(context.Background(), c)) != 1 || a.Pending(context.Background(), c)[0].CallID != "t1" || len(model.Requests()) != 1 {
		t.Fatalf("après la première décision : %q, pending %+v, %d requêtes", c.Status, a.Pending(context.Background(), c), len(model.Requests()))
	}
	if _, err := a.Decide(context.Background(), c.ID, "t1", false); err != nil {
		t.Fatal(err)
	}
	a.Wait()
	c, _, _ = a.Get(context.Background(), c.ID)
	if c.Status != StatusIdle || !reflect.DeepEqual(*seen, []string{`local/local:{"n":2}`}) {
		t.Errorf("fin : %q, exécutés %v", c.Status, *seen)
	}
	res := model.Requests()[1].Messages[2].Results
	if len(res) != 2 || res[0].CallID != "t1" || !res[0].IsError || res[1].Content != "supprimée" {
		t.Errorf("résultats dans l'ordre des appels = %+v", res)
	}
}

// L'attente survit à un redémarrage : aucun processus ne la porte.
func TestAction_SurvivesRestart(t *testing.T) {
	a, store, _, seen := actionFixture(t, toolTurn("", call("t2", "send_email", `{}`)))
	c := ask(t, a, "", "envoie")
	if n, _ := a.RecoverOrphaned(context.Background()); n != 0 {
		t.Fatalf("la reprise a touché %d conversation(s) en attente", n)
	}
	model2 := &claudeapi.FakeClient{Responses: []claudeapi.Response{endTurn("Envoyé après redémarrage.")}}
	b := &Assistant{Model: model2, Store: store, Tools: a.Tools, Now: a.Now, DefaultScope: a.DefaultScope}
	if _, err := b.Decide(context.Background(), c.ID, "t2", true); err != nil {
		t.Fatal(err)
	}
	b.Wait()
	got, _, _ := b.Get(context.Background(), c.ID)
	if got.Status != StatusIdle || len(*seen) != 1 {
		t.Errorf("après redémarrage : %q, exécutés %v", got.Status, *seen)
	}
}

func TestDecide_Errors(t *testing.T) {
	a, _, _, _ := actionFixture(t, toolTurn("", call("t2", "send_email", `{}`)))
	ctx := context.Background()
	if _, err := a.Decide(ctx, "absente", "t2", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("conversation inconnue = %v", err)
	}
	c := ask(t, a, "", "envoie")
	if _, err := a.Decide(ctx, c.ID, "autre", true); !errors.Is(err, ErrNotPending) {
		t.Errorf("appel inconnu = %v", err)
	}
	marie := tenancy.WithScope(ctx, userScope("famille", "marie"))
	if _, err := a.Decide(marie, c.ID, "t2", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("la confirmation d'un autre user = %v, veut ErrNotFound", err)
	}
}

// Ce que l'assistant écrit est écrit au nom du user, et le dit.
func TestTools_RunOnBehalfOfTheUserViaAssistant(t *testing.T) {
	var got tenancy.Scope
	tool := Tool{Name: "t", Level: LevelWrite, Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		got, _ = tenancy.FromContext(ctx)
		return "ok", nil
	}}
	model := &claudeapi.FakeClient{Responses: []claudeapi.Response{toolTurn("", call("a", "t", `{}`)), endTurn("ok")}}
	a, _ := newAssistant(model, tool)
	ask(t, a, "", "x")
	want := tenancy.LocalScope()
	want.Via = tenancy.ViaAssistant
	if got != want {
		t.Errorf("portée de l'outil = %+v, veut %+v", got, want)
	}
}

func TestAsk_ToolOutputIsCapped(t *testing.T) {
	model := &claudeapi.FakeClient{Responses: []claudeapi.Response{toolTurn("", call("a", "gros", `{}`)), endTurn("ok")}}
	a, _ := newAssistant(model, echoTool("gros", LevelRead, strings.Repeat("é", 500), nil))
	a.MaxToolOutput = 100
	c := ask(t, a, "", "x")
	out := c.Entries[2].Results[0].Content
	if !strings.HasPrefix(out, strings.Repeat("é", 100)) || strings.HasPrefix(out, strings.Repeat("é", 101)) ||
		!strings.Contains(out, "coupée") || !strings.Contains(out, "500") {
		t.Errorf("sortie = %q", out)
	}
}

func TestAsk_StepLimit(t *testing.T) {
	model := &claudeapi.FakeClient{}
	for i := 0; i < 10; i++ {
		model.Responses = append(model.Responses, toolTurn("", call("a", "t", `{}`)))
	}
	a, _ := newAssistant(model, echoTool("t", LevelRead, "x", nil))
	a.MaxSteps = 3
	c := ask(t, a, "", "x")
	if c.Status != StatusFailed || !strings.Contains(c.Error, "3 étapes") || len(model.Requests()) != 3 {
		t.Errorf("conversation = %q %q, %d requêtes", c.Status, c.Error, len(model.Requests()))
	}
}

func TestAsk_ModelErrorThenRetry(t *testing.T) {
	model := &claudeapi.FakeClient{Errs: []error{claudeapi.ErrAuth}, Responses: []claudeapi.Response{{}, endTurn("Me revoilà.")}}
	a, _ := newAssistant(model)
	c := ask(t, a, "", "Bonjour")
	if c.Status != StatusFailed || !strings.Contains(c.Error, "clé") || len(c.Entries) != 1 {
		t.Fatalf("conversation = %+v", c)
	}
	c = ask(t, a, c.ID, "Tu es là ?")
	if c.Status != StatusIdle || c.Error != "" || len(c.Entries) != 3 {
		t.Fatalf("après relance = %+v", c)
	}
	msgs := model.Requests()[1].Messages
	if len(msgs) != 2 || msgs[0].User != "Bonjour" || msgs[1].User != "Tu es là ?" {
		t.Errorf("messages = %+v", msgs)
	}
}

func TestAsk_Refusal(t *testing.T) {
	model := &claudeapi.FakeClient{Responses: []claudeapi.Response{{Stop: claudeapi.StopRefusal, Refusal: "cyber", Raw: json.RawMessage(`{}`)}}}
	a, _ := newAssistant(model)
	c := ask(t, a, "", "x")
	if c.Status != StatusFailed || !strings.Contains(c.Error, "refus") || len(c.Entries) != 1 {
		t.Errorf("conversation = %+v", c)
	}
}

func TestAsk_TruncatedToolCallFails(t *testing.T) {
	r := toolTurn("", call("a", "t", `{"x":`))
	r.Stop = claudeapi.StopMaxTokens
	model := &claudeapi.FakeClient{Responses: []claudeapi.Response{r}}
	var seen []string
	a, _ := newAssistant(model, echoTool("t", LevelRead, "x", &seen))
	c := ask(t, a, "", "x")
	if c.Status != StatusFailed || !strings.Contains(c.Error, "coupée") || len(seen) != 0 {
		t.Errorf("conversation = %+v, outils = %v", c, seen)
	}
}

func TestAsk_BusyConversationRefusesASecondTurn(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	model := &claudeapi.FakeClient{Responses: []claudeapi.Response{endTurn("ok")},
		Hook: func(int, claudeapi.Request) { close(started); <-release }}
	a, _ := newAssistant(model)
	c, err := a.Ask(context.Background(), "", "un")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if got, _, _ := a.Get(context.Background(), c.ID); got.Status != StatusRunning {
		t.Errorf("status pendant le tour = %q", got.Status)
	}
	if _, err := a.Ask(context.Background(), c.ID, "deux"); !errors.Is(err, ErrBusy) {
		t.Errorf("second tour = %v, veut ErrBusy", err)
	}
	close(release)
	a.Wait()
	if got, _, _ := a.Get(context.Background(), c.ID); len(got.Entries) != 2 {
		t.Errorf("le message refusé a été ajouté au fil : %+v", got.Entries)
	}
}

func TestAsk_DailyBudget(t *testing.T) {
	model := &claudeapi.FakeClient{Responses: []claudeapi.Response{endTurn("ok")}}
	a, store := newAssistant(model)
	a.DailyTokens = 1000
	_ = store.AddUsage(context.Background(), "2026-11-11", claudeapi.Usage{Input: 900, Output: 100})
	c := ask(t, a, "", "x")
	if c.Status != StatusFailed || !strings.Contains(c.Error, "plafond") || len(model.Requests()) != 0 {
		t.Errorf("conversation = %+v, requêtes = %d", c, len(model.Requests()))
	}
	// Hier ne compte pas.
	a2, store2 := newAssistant(model)
	a2.DailyTokens = 1000
	_ = store2.AddUsage(context.Background(), "2026-11-10", claudeapi.Usage{Input: 5000})
	if c := ask(t, a2, "", "x"); c.Status != StatusIdle {
		t.Errorf("la consommation d'hier a bloqué : %+v", c)
	}
}

func TestAsk_Validation(t *testing.T) {
	a, _ := newAssistant(&claudeapi.FakeClient{})
	if _, err := a.Ask(context.Background(), "", "   "); err == nil {
		t.Error("message vide accepté")
	}
	if _, err := a.Ask(context.Background(), "absente", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("conversation inconnue = %v", err)
	}
	if _, err := a.Ask(context.Background(), "", strings.Repeat("x", MaxMessageLen+1)); err == nil {
		t.Error("message démesuré accepté")
	}
}

// Les conversations d'un autre user sont introuvables, pas interdites.
func TestAsk_ScopeComesFromContext(t *testing.T) {
	model := &claudeapi.FakeClient{Responses: []claudeapi.Response{toolTurn("", call("a", "t", `{}`)), endTurn("ok")}}
	var seen []string
	a, _ := newAssistant(model, echoTool("t", LevelRead, "x", &seen))
	marie := tenancy.WithScope(context.Background(), userScope("famille", "marie"))
	c, err := a.Ask(marie, "", "x")
	if err != nil {
		t.Fatal(err)
	}
	a.Wait()
	if !reflect.DeepEqual(seen, []string{"famille/marie:{}"}) {
		t.Errorf("portée des outils = %v", seen)
	}
	if _, ok, _ := a.Get(context.Background(), c.ID); ok {
		t.Error("la conversation de Marie est visible de l'installation locale")
	}
	if got, ok, _ := a.Get(marie, c.ID); !ok || got.Status != StatusIdle {
		t.Errorf("Marie ne retrouve pas sa conversation : %+v", got)
	}
}

// Un tour coupé après la demande d'outils (processus tué) laisse des
// appels sans réponse : l'API refuserait la conversation entière.
func TestMessages_RepairsDanglingToolCalls(t *testing.T) {
	entries := []Entry{
		{Role: RoleUser, Text: "a"},
		{Role: RoleAssistant, Raw: []byte(`{"r":1}`), Calls: []claudeapi.Call{call("t1", "x", `{}`), call("t2", "y", `{}`)}},
		{Role: RoleUser, Text: "tu es là ?"},
	}
	msgs := messages(entries)
	if len(msgs) != 4 || len(msgs[2].Results) != 2 || !msgs[2].Results[0].IsError || msgs[2].Results[1].CallID != "t2" ||
		msgs[3].User != "tu es là ?" {
		t.Fatalf("messages = %+v", msgs)
	}
	// Réponses partielles : seules les manquantes sont complétées.
	entries = []Entry{
		{Role: RoleAssistant, Raw: []byte(`{}`), Calls: []claudeapi.Call{call("t1", "x", `{}`), call("t2", "y", `{}`)}},
		{Role: RoleTools, Results: []claudeapi.Result{{CallID: "t1", Content: "ok"}}},
	}
	msgs = messages(entries)
	if len(msgs) != 2 || len(msgs[1].Results) != 2 || msgs[1].Results[0].Content != "ok" || !msgs[1].Results[1].IsError {
		t.Fatalf("messages = %+v", msgs)
	}
}

func TestRecoverOrphaned(t *testing.T) {
	a, store := newAssistant(&claudeapi.FakeClient{})
	ctx := context.Background()
	_ = store.Create(ctx, Conversation{ID: "c1", Status: StatusIdle})
	_, _ = store.Start(ctx, "c1", testNow)
	n, err := a.RecoverOrphaned(ctx)
	if err != nil || n != 1 {
		t.Fatalf("RecoverOrphaned = %d, %v", n, err)
	}
	if c, _, _ := store.Get(ctx, "c1"); c.Status != StatusFailed || !strings.Contains(c.Error, "redémarrage") {
		t.Errorf("conversation = %+v", c)
	}
}

func TestMemory(t *testing.T) {
	a, _ := newAssistant(&claudeapi.FakeClient{})
	ctx := context.Background()
	f, err := a.Remember(ctx, "  Le propriétaire s'appelle M. Weber  ")
	if err != nil || f.ID == "" || f.Text != "Le propriétaire s'appelle M. Weber" {
		t.Fatalf("Remember = %+v, %v", f, err)
	}
	if _, err := a.Remember(ctx, " "); err == nil {
		t.Error("fait vide accepté")
	}
	if _, err := a.Remember(ctx, strings.Repeat("x", MaxFactLen+1)); err == nil {
		t.Error("fait démesuré accepté")
	}
	if facts, _ := a.Facts(ctx); len(facts) != 1 {
		t.Fatalf("Facts = %+v", facts)
	}
	if err := a.Forget(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if facts, _ := a.Facts(ctx); len(facts) != 0 {
		t.Errorf("Facts après oubli = %+v", facts)
	}
}

// Ce qui part dans MongoDB en revient à l'identique — sans base : c'est
// l'encodage BSON qui est vérifié (octets du message de Claude compris).
func TestConversation_BSONRoundTrip(t *testing.T) {
	in := Conversation{ID: "c", Env: "e", User: "u", Title: "t", Status: StatusIdle, CreatedAt: testNow, UpdatedAt: testNow,
		Entries: []Entry{{Role: RoleAssistant, At: testNow, Text: "x", Raw: []byte(`{"a":[1,"é"]}`), Model: "m",
			Calls: []claudeapi.Call{call("t1", "n", `{"q":"é"}`)}, Usage: claudeapi.Usage{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4}},
			{Role: RoleTools, At: testNow, Results: []claudeapi.Result{{CallID: "t1", Content: "r", IsError: true}}}}}
	raw, err := bson.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Conversation
	if err := bson.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("aller-retour BSON :\n  in  %+v\n  out %+v", in, out)
	}
}
