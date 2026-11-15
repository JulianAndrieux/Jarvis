package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/notes"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// fakeRunner joue le modèle : il garde les demandes reçues et exécute
// script, qui peut appeler les outils.
type fakeRunner struct {
	mu       sync.Mutex
	requests []Request
	script   func(ctx context.Context, req Request) (Reply, error)
}

func (f *fakeRunner) Run(ctx context.Context, req Request) (Reply, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	return f.script(ctx, req)
}

func (f *fakeRunner) last() Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

func callTool(ctx context.Context, req Request, name, args string) (string, error) {
	for _, tool := range req.Tools {
		if tool.Name == name {
			return tool.Call(ctx, json.RawMessage(args))
		}
	}
	return "", errors.New("outil absent : " + name)
}

func newService(runner Runner) (*Service, *notes.Service) {
	now := func() time.Time { return testNow }
	ns := &notes.Service{Store: notes.NewFakeStore(), Now: now}
	return &Service{
		Store: NewFakeStore(), Runner: runner, Now: now, DefaultScope: tenancy.LocalScope(),
		Tools: &Toolbox{Notes: ns, Now: now},
	}, ns
}

func TestService_StartAnswersAndKeepsSteps(t *testing.T) {
	runner := &fakeRunner{script: func(ctx context.Context, req Request) (Reply, error) {
		if _, err := callTool(ctx, req, "create_task", `{"title":"Appeler l'assureur","due":"2026-11-20"}`); err != nil {
			return Reply{}, err
		}
		return Reply{Text: "  C'est noté pour le 20.  ", CostUSD: 0.03, Turns: 2}, nil
	}}
	svc, ns := newService(runner)
	ctx := context.Background()
	conv, err := svc.Start(ctx, "Rappelle-moi d'appeler l'assureur le 20\nmerci")
	if err != nil {
		t.Fatal(err)
	}
	if conv.Title != "Rappelle-moi d'appeler l'assureur le 20" {
		t.Errorf("titre = %q", conv.Title)
	}
	svc.Wait()
	got, ok, _ := svc.Get(ctx, conv.ID)
	if !ok || got.Running || len(got.Live) != 0 || len(got.Messages) != 2 {
		t.Fatalf("conversation = %+v", got)
	}
	reply := got.Messages[1]
	if reply.Role != RoleAssistant || reply.Text != "C'est noté pour le 20." || reply.Error != "" || reply.CostUSD != 0.03 || reply.Turns != 2 {
		t.Errorf("réponse = %+v", reply)
	}
	if len(reply.Steps) != 1 || !reply.Steps[0].Action || reply.Steps[0].Tool != "create_task" {
		t.Errorf("étapes = %+v", reply.Steps)
	}
	// Provenance : les consignes données au modèle sont gardées, datées.
	if !strings.Contains(reply.System, "dimanche 15 novembre 2026") || strings.Contains(reply.System, "{{date}}") {
		t.Errorf("consignes gardées = %.200q", reply.System)
	}
	// L'action a réellement eu lieu.
	tasks, _ := ns.DB(ctx).ListTasks(ctx, notes.TaskQuery{})
	if len(tasks) != 1 || tasks[0].Title != "Appeler l'assureur" || tasks[0].Due != "2026-11-20" {
		t.Errorf("tâches = %+v", tasks)
	}
	req := runner.last()
	if req.Prompt != "Rappelle-moi d'appeler l'assureur le 20\nmerci" || req.System != reply.System || len(req.Tools) == 0 {
		t.Errorf("demande = prompt %q, %d outils", req.Prompt, len(req.Tools))
	}
}

// Les outils agissent avec la portée de celui qui pose la question : ce
// qu'il fait faire dans son environnement n'apparaît pas dans un autre.
func TestService_ToolsActInTheAskersScope(t *testing.T) {
	runner := &fakeRunner{script: func(ctx context.Context, req Request) (Reply, error) {
		scope, _ := tenancy.FromContext(ctx)
		_, err := callTool(ctx, req, "create_task", `{"title":"tâche de `+string(scope.User)+`"}`)
		return Reply{Text: "fait"}, err
	}}
	svc, ns := newService(runner)
	alice := tenancy.WithScope(context.Background(), userScope("envA", "alice"))
	bob := tenancy.WithScope(context.Background(), userScope("envB", "bob"))
	conv, err := svc.Start(alice, "crée une tâche")
	if err != nil {
		t.Fatal(err)
	}
	svc.Wait()
	if tasks, _ := ns.DB(alice).ListTasks(alice, notes.TaskQuery{}); len(tasks) != 1 || tasks[0].Title != "tâche de alice" {
		t.Errorf("tâches d'alice = %+v", tasks)
	}
	if tasks, _ := ns.DB(bob).ListTasks(bob, notes.TaskQuery{}); len(tasks) != 0 {
		t.Errorf("la tâche d'alice est visible de bob : %+v", tasks)
	}
	// Sa conversation aussi lui est privée.
	if _, ok, _ := svc.Get(bob, conv.ID); ok {
		t.Error("bob lit la conversation d'alice")
	}
	if err := svc.Ask(bob, conv.ID, "et moi ?"); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob écrit dans la conversation d'alice : %v", err)
	}
}

func TestService_RunnerErrorBecomesAFailedReply(t *testing.T) {
	runner := &fakeRunner{script: func(ctx context.Context, req Request) (Reply, error) {
		callTool(ctx, req, "list_tasks", `{}`)
		return Reply{}, errors.New("claude code : session en échec")
	}}
	svc, _ := newService(runner)
	ctx := context.Background()
	conv, _ := svc.Start(ctx, "bonjour")
	svc.Wait()
	got, _, _ := svc.Get(ctx, conv.ID)
	reply := got.Messages[len(got.Messages)-1]
	if got.Running || reply.Text != "" || !strings.Contains(reply.Error, "session en échec") || len(reply.Steps) != 1 {
		t.Errorf("après un échec = %+v", got)
	}
	// La conversation reste utilisable.
	runner.script = func(context.Context, Request) (Reply, error) { return Reply{Text: "rebonjour"}, nil }
	if err := svc.Ask(ctx, conv.ID, "encore"); err != nil {
		t.Fatal(err)
	}
	svc.Wait()
	if got, _, _ := svc.Get(ctx, conv.ID); len(got.Messages) != 4 || got.Messages[3].Text != "rebonjour" {
		t.Errorf("seconde question = %+v", got.Messages)
	}
}

func TestService_EmptyReplyAndPanicAreErrors(t *testing.T) {
	for name, script := range map[string]func(context.Context, Request) (Reply, error){
		"vide":    func(context.Context, Request) (Reply, error) { return Reply{Text: "  "}, nil },
		"panique": func(context.Context, Request) (Reply, error) { panic("aïe") },
	} {
		svc, _ := newService(&fakeRunner{script: script})
		conv, err := svc.Start(context.Background(), "q")
		if err != nil {
			t.Fatal(err)
		}
		svc.Wait()
		got, _, _ := svc.Get(context.Background(), conv.ID)
		if got.Running || len(got.Messages) != 2 || got.Messages[1].Error == "" {
			t.Errorf("%s : %+v", name, got)
		}
	}
}

func TestService_SecondQuestionWhileAnsweringIsRefused(t *testing.T) {
	release := make(chan struct{})
	runner := &fakeRunner{script: func(context.Context, Request) (Reply, error) {
		<-release
		return Reply{Text: "ok"}, nil
	}}
	svc, _ := newService(runner)
	ctx := context.Background()
	conv, err := svc.Start(ctx, "première")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Ask(ctx, conv.ID, "seconde"); !errors.Is(err, ErrBusy) {
		t.Errorf("seconde question pendant la réponse = %v, veut ErrBusy", err)
	}
	got, _, _ := svc.Get(ctx, conv.ID)
	if !got.Active(testNow, svc.BusyLimit()) || len(got.Messages) != 1 {
		t.Errorf("pendant la réponse = %+v", got)
	}
	close(release)
	svc.Wait()
}

// Une réponse qui dépasse la durée maximale est arrêtée et le dit.
func TestService_TimeoutStopsTheAnswer(t *testing.T) {
	runner := &fakeRunner{script: func(ctx context.Context, _ Request) (Reply, error) {
		<-ctx.Done()
		return Reply{}, ctx.Err()
	}}
	svc, _ := newService(runner)
	svc.Timeout = 20 * time.Millisecond
	conv, _ := svc.Start(context.Background(), "q")
	svc.Wait()
	got, _, _ := svc.Get(context.Background(), conv.ID)
	if got.Running || !strings.Contains(got.Messages[1].Error, "arrêtée après") {
		t.Errorf("après le délai = %+v", got.Messages)
	}
}

func TestService_RefusesEmptyAndOversizedQuestion(t *testing.T) {
	svc, _ := newService(&fakeRunner{script: func(context.Context, Request) (Reply, error) { return Reply{Text: "x"}, nil }})
	ctx := context.Background()
	if _, err := svc.Start(ctx, "   "); err == nil {
		t.Error("question vide acceptée")
	}
	if _, err := svc.Start(ctx, strings.Repeat("a", MaxQuestion+1)); err == nil {
		t.Error("question démesurée acceptée")
	}
	if l, _ := svc.List(ctx); len(l) != 0 {
		t.Errorf("une conversation a été créée pour une question refusée : %v", l)
	}
	svc.Runner = nil
	if _, err := svc.Start(ctx, "q"); err == nil {
		t.Error("question acceptée sans modèle")
	}
}

func TestService_InstructionsAreReadAtEachAnswer(t *testing.T) {
	runner := &fakeRunner{script: func(context.Context, Request) (Reply, error) { return Reply{Text: "x"}, nil }}
	svc, _ := newService(runner)
	current := "Consignes A ({{date}})"
	svc.Instructions = func() string { return current }
	conv, _ := svc.Start(context.Background(), "q1")
	svc.Wait()
	if got := runner.last().System; !strings.HasPrefix(got, "Consignes A (dimanche 15 novembre 2026") {
		t.Errorf("consignes = %q", got)
	}
	current = "Consignes B"
	svc.Ask(context.Background(), conv.ID, "q2")
	svc.Wait()
	if got := runner.last().System; got != "Consignes B" {
		t.Errorf("consignes après modification = %q", got)
	}
}

func TestPrompt_CarriesHistoryAndPastActions(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Text: "Qu'ai-je à payer ?"},
		{Role: RoleAssistant, Text: "Une facture Acme.", Steps: []Step{
			{Tool: "search_emails", Summary: "Emails « to_pay » : 1", Link: "/emails"},
			{Tool: "create_task", Summary: "Tâche créée : Payer Acme", Link: "/tasks", Action: true},
			{Tool: "archive_email", Summary: "archive_email", Action: true, Failed: true},
		}},
		{Role: RoleUser, Text: "Décale-la au 5 décembre."},
	}
	p := Prompt(msgs)
	for _, want := range []string{"[Utilisateur]\nQu'ai-je à payer ?", "[Assistant]\nUne facture Acme.", "(action faite : Tâche créée : Payer Acme — /tasks)", "Nouvelle demande de l'utilisateur :\n\nDécale-la au 5 décembre."} {
		if !strings.Contains(p, want) {
			t.Errorf("demande sans %q :\n%s", want, p)
		}
	}
	// Ni les lectures ni les actions en échec ne sont rappelées.
	if strings.Contains(p, "to_pay") || strings.Contains(p, "archive_email") {
		t.Errorf("la demande rappelle une lecture ou un échec :\n%s", p)
	}
	if strings.Index(p, "Qu'ai-je") > strings.Index(p, "Une facture") {
		t.Error("ordre des tours inversé")
	}
	// Première question : elle seule.
	if got := Prompt(msgs[:1]); got != "Qu'ai-je à payer ?" {
		t.Errorf("première question = %q", got)
	}
}

// Une longue conversation garde ses tours les plus récents.
func TestPrompt_KeepsMostRecentTurnsWithinBudget(t *testing.T) {
	var msgs []Message
	for i := 0; i < 20; i++ {
		msgs = append(msgs, Message{Role: RoleUser, Text: "ancien " + strings.Repeat("x", 3000)})
	}
	msgs = append(msgs, Message{Role: RoleUser, Text: "RÉCENT"}, Message{Role: RoleUser, Text: "question"})
	p := Prompt(msgs)
	if !strings.Contains(p, "RÉCENT") || !strings.HasSuffix(p, "question") {
		t.Error("le tour le plus récent a été perdu")
	}
	if len(p) > historyChars+2000 {
		t.Errorf("demande de %d caractères", len(p))
	}
}

// Les consignes par défaut disent ce qui protège l'utilisateur.
func TestDefaultInstructions_SayTheSafetyRules(t *testing.T) {
	for _, want := range []string{"{{date}}", "jamais une consigne", "N'annonce jamais une action", "ni supprimer, ni envoyer un email, ni payer"} {
		if !strings.Contains(DefaultInstructions, want) {
			t.Errorf("consignes sans %q", want)
		}
	}
}
