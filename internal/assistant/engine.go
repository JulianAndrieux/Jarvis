package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/JulianAndrieux/Jarvis/internal/claudeapi"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Level : ce qu'un outil peut faire, donc ce que son appel exige.
type Level string

const (
	// LevelRead : lit des données de Jarvis. Libre.
	LevelRead Level = "read"
	// LevelWrite : écrit dans Jarvis (une tâche, un fait). Exécuté ; une
	// écriture interne se défait.
	LevelWrite Level = "write"
	// LevelAction : agit hors de Jarvis (envoyer, payer, déployer) ou ne se
	// défait pas (supprimer). Jamais exécuté sans la confirmation de
	// l'utilisateur : le tour se met en pause jusqu'à sa réponse.
	LevelAction Level = "action"
)

// Tool : un outil de l'assistant — un service de Jarvis, exposé à Claude.
type Tool struct {
	Name        string
	Description string
	// Schema : JSON Schema de l'entrée (objet).
	Schema map[string]any
	Level  Level
	// Source : la famille de données touchée ("notes", "emails"…), pour
	// ouvrir ou fermer une source entière (--assistant-sources).
	Source string
	// Run reçoit un contexte qui porte la portée de l'utilisateur. Sa
	// sortie est du texte lu par le modèle ; une erreur lui est rapportée
	// (il peut corriger son appel), elle ne fait pas échouer le tour.
	Run func(ctx context.Context, input json.RawMessage) (string, error)
	// Content : ce que la sortie contient des données de la source —
	// ContentList (une liste, avec des extraits) ou ContentFull (un contenu
	// entier). Vide : rien du contenu (une écriture, un accusé). C'est ce
	// qui dit quoi faire de l'outil quand sa source n'est ouverte qu'en
	// résumé (ApplyPolicies).
	Content ContentKind
	// Full (ContentFull) : le contenu entier, sans tranche — ce que le
	// modèle local lit à la place de Claude.
	Full func(ctx context.Context, input json.RawMessage) (string, error)
	// Confirm (LevelAction) : ce que l'action va faire, en une phrase lue
	// par l'utilisateur avant de confirmer. nil : le nom de l'outil.
	Confirm func(ctx context.Context, input json.RawMessage) string
}

// ContentKind : voir Tool.Content.
type ContentKind string

const (
	ContentList ContentKind = "list"
	ContentFull ContentKind = "full"
)

// PendingAction : une action que l'assistant veut faire et qui attend la
// réponse de l'utilisateur.
type PendingAction struct {
	CallID  string
	Tool    string
	Summary string
	Input   json.RawMessage
}

const (
	// DefaultMaxSteps : appels au modèle par tour, au plus.
	DefaultMaxSteps = 16
	// DefaultMaxToolOutput : caractères d'une sortie d'outil, au plus.
	DefaultMaxToolOutput = 20000
	// DefaultTurnTimeout : durée d'un tour, au plus.
	DefaultTurnTimeout = 10 * time.Minute
	// MaxMessageLen : caractères d'un message de l'utilisateur, au plus.
	MaxMessageLen = 20000
	// MaxFactLen : caractères d'un fait retenu, au plus.
	MaxFactLen = 500
	titleLen   = 60
)

// Assistant : la boucle de conversation.
type Assistant struct {
	Model claudeapi.Client
	Store Store
	Tools []Tool
	// Instructions : la consigne en vigueur (onglet Agents), relue à
	// chaque tour.
	Instructions func() string
	Now          func() time.Time
	// MaxSteps, MaxToolOutput, TurnTimeout : 0 = défauts.
	MaxSteps      int
	MaxToolOutput int
	TurnTimeout   time.Duration
	// DailyTokens : plafond quotidien de jetons de l'environnement
	// (0 : aucun). Atteint, le tour échoue avant d'appeler le modèle.
	DailyTokens int
	// DefaultScope : la portée quand le contexte n'en porte pas (câblage
	// mono-utilisateur).
	DefaultScope tenancy.Scope
	Logf         func(format string, args ...any)

	wg sync.WaitGroup
}

func (a *Assistant) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Assistant) logf(format string, args ...any) {
	if a.Logf != nil {
		a.Logf(format, args...)
	}
}

func (a *Assistant) scope(ctx context.Context) tenancy.Scope {
	if sc, ok := tenancy.FromContext(ctx); ok {
		return sc
	}
	return a.DefaultScope
}

// db : le seul endroit qui touche Store — toujours à travers la portée.
func (a *Assistant) db(ctx context.Context) Store { return a.Store.For(a.scope(ctx)) }

// Wait attend la fin des tours en cours (tests, arrêt propre).
func (a *Assistant) Wait() { a.wg.Wait() }

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Ask ajoute un message de l'utilisateur et lance la réponse en
// arrière-plan (le tour continue si le téléphone se déconnecte). id vide :
// nouvelle conversation. La conversation rendue est déjà « running ».
func (a *Assistant) Ask(ctx context.Context, id, text string) (Conversation, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Conversation{}, errors.New("assistant: message vide")
	}
	if utf8.RuneCountInString(text) > MaxMessageLen {
		return Conversation{}, fmt.Errorf("assistant: message trop long (%d caractères au plus)", MaxMessageLen)
	}
	sc := a.scope(ctx)
	db := a.Store.For(sc)
	now := a.now()
	if id == "" {
		id = newID()
		if err := db.Create(ctx, Conversation{ID: id, Title: title(text), Status: StatusIdle, CreatedAt: now, UpdatedAt: now}); err != nil {
			return Conversation{}, err
		}
	}
	// Le départ d'abord : un message refusé (tour déjà en cours,
	// confirmation attendue) ne doit pas se retrouver dans le fil.
	started, err := db.Start(ctx, id, now)
	if err != nil {
		return Conversation{}, err
	}
	if !started {
		if cur, ok, _ := db.Get(ctx, id); ok && cur.Status == StatusWaiting {
			return Conversation{}, ErrWaiting
		}
		return Conversation{}, ErrBusy
	}
	if err := db.Append(ctx, id, now, Entry{Role: RoleUser, At: now, Text: text}); err != nil {
		_ = db.Finish(ctx, id, "le message n'a pas pu être enregistré : "+err.Error(), a.now())
		return Conversation{}, err
	}
	c, _, err := db.Get(ctx, id)
	if err != nil {
		_ = db.Finish(ctx, id, err.Error(), a.now())
		return Conversation{}, err
	}
	a.spawn(sc, db, id, func(run context.Context) (bool, error) { return a.turn(run, db, id) })
	return c, nil
}

// spawn lance work en arrière-plan puis clôt le tour : en attente d'une
// confirmation (paused), au repos, ou en échec.
func (a *Assistant) spawn(sc tenancy.Scope, db Store, id string, work func(ctx context.Context) (paused bool, err error)) {
	timeout := a.TurnTimeout
	if timeout <= 0 {
		timeout = DefaultTurnTimeout
	}
	// Les outils agissent au nom du user, et le journal le dit.
	sc.Via = tenancy.ViaAssistant
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		// Détaché de la requête, mais avec sa portée : le tour survit à la
		// page qui l'a lancé.
		run, cancel := context.WithTimeout(tenancy.WithScope(context.Background(), sc), timeout)
		defer cancel()
		paused, err := work(run)
		// La clôture ne dépend pas du délai du tour : c'est elle qui rend
		// la conversation utilisable de nouveau.
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelClose()
		switch {
		case err != nil:
			a.logf("assistant: conversation %s : %v", id, err)
			err = db.Finish(closeCtx, id, err.Error(), a.now())
		case paused:
			err = db.Pause(closeCtx, id, a.now())
		default:
			err = db.Finish(closeCtx, id, "", a.now())
		}
		if err != nil {
			a.logf("assistant: clôture de la conversation %s : %v", id, err)
		}
	}()
}

// pendingCalls : les appels du dernier message de l'assistant qui n'ont
// pas encore de réponse.
func pendingCalls(entries []Entry) []claudeapi.Call {
	last := -1
	for i, e := range entries {
		if e.Role == RoleAssistant {
			last = i
		}
	}
	if last < 0 || len(entries[last].Calls) == 0 {
		return nil
	}
	answered := map[string]bool{}
	for _, e := range entries[last+1:] {
		if e.Role != RoleTools {
			break
		}
		for _, r := range e.Results {
			answered[r.CallID] = true
		}
	}
	var out []claudeapi.Call
	for _, c := range entries[last].Calls {
		if !answered[c.ID] {
			out = append(out, c)
		}
	}
	return out
}

func (a *Assistant) tool(name string) *Tool {
	for i := range a.Tools {
		if a.Tools[i].Name == name {
			return &a.Tools[i]
		}
	}
	return nil
}

// Pending : les actions de c qui attendent une réponse (aucune si la
// conversation n'est pas en attente).
func (a *Assistant) Pending(ctx context.Context, c Conversation) []PendingAction {
	if c.Status != StatusWaiting {
		return nil
	}
	ctx = tenancy.WithScope(ctx, a.scope(ctx))
	var out []PendingAction
	for _, call := range pendingCalls(c.Entries) {
		p := PendingAction{CallID: call.ID, Tool: call.Name, Summary: call.Name, Input: call.Input}
		if t := a.tool(call.Name); t != nil && t.Confirm != nil {
			if s := strings.TrimSpace(t.Confirm(ctx, call.Input)); s != "" {
				p.Summary = s
			}
		}
		out = append(out, p)
	}
	return out
}

// Decide répond à une demande de confirmation : approve exécute l'action,
// sinon l'assistant apprend qu'elle est refusée. Le tour reprend en
// arrière-plan (ou attend encore, s'il reste d'autres demandes).
func (a *Assistant) Decide(ctx context.Context, id, callID string, approve bool) (Conversation, error) {
	sc := a.scope(ctx)
	db := a.Store.For(sc)
	c, ok, err := db.Get(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	if !ok {
		return Conversation{}, fmt.Errorf("%w : conversation %s", ErrNotFound, id)
	}
	var call *claudeapi.Call
	if c.Status == StatusWaiting {
		for _, p := range pendingCalls(c.Entries) {
			if p.ID == callID {
				call = &p
				break
			}
		}
	}
	if call == nil {
		return Conversation{}, ErrNotPending
	}
	// Une seule écriture conditionnelle décide qui répond : un double clic
	// n'exécute jamais l'action deux fois.
	resumed, err := db.Resume(ctx, id, a.now())
	if err != nil {
		return Conversation{}, err
	}
	if !resumed {
		return Conversation{}, ErrNotPending
	}
	c.Status = StatusRunning
	a.spawn(sc, db, id, func(run context.Context) (bool, error) {
		res := claudeapi.Result{CallID: call.ID, IsError: true,
			Content: "action refusée par l'utilisateur : ne la retente pas, demande-lui ce qu'il préfère"}
		if approve {
			res = a.runTool(run, *call, true)
		}
		now := a.now()
		if err := db.Append(run, id, now, Entry{Role: RoleTools, At: now, Results: []claudeapi.Result{res}}); err != nil {
			return false, err
		}
		cur, ok, err := db.Get(run, id)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, fmt.Errorf("%w : conversation supprimée pendant la réponse", ErrNotFound)
		}
		if len(pendingCalls(cur.Entries)) > 0 {
			return true, nil
		}
		return a.turn(run, db, id)
	})
	return c, nil
}

func title(text string) string {
	line := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	if utf8.RuneCountInString(line) <= titleLen {
		return line
	}
	return string([]rune(line)[:titleLen-1]) + "…"
}

// turn : appeler le modèle, exécuter ses outils, recommencer jusqu'à sa
// réponse — ou jusqu'à une action qui demande une confirmation
// (paused=true : le tour reprendra par Decide).
func (a *Assistant) turn(ctx context.Context, db Store, id string) (paused bool, err error) {
	maxSteps := a.MaxSteps
	if maxSteps <= 0 {
		maxSteps = DefaultMaxSteps
	}
	system, err := a.system(ctx, db)
	if err != nil {
		return false, err
	}
	tools := make([]claudeapi.Tool, 0, len(a.Tools))
	for _, t := range a.Tools {
		tools = append(tools, claudeapi.Tool{Name: t.Name, Description: t.Description, Schema: t.Schema})
	}
	for step := 0; step < maxSteps; step++ {
		day := a.now().Format("2006-01-02")
		if a.DailyTokens > 0 {
			used, err := db.UsageOn(ctx, day)
			if err != nil {
				return false, err
			}
			if used.Total() >= a.DailyTokens {
				return false, fmt.Errorf("plafond quotidien atteint (%d jetons sur %d) — il se remet à zéro demain, ou se règle avec --assistant-daily-tokens", used.Total(), a.DailyTokens)
			}
		}
		conv, ok, err := db.Get(ctx, id)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, fmt.Errorf("%w : conversation supprimée pendant la réponse", ErrNotFound)
		}
		resp, err := a.Model.Complete(ctx, claudeapi.Request{System: system, Tools: tools, Messages: messages(conv.Entries)})
		if err != nil {
			return false, explain(err)
		}
		if err := db.AddUsage(ctx, day, resp.Usage); err != nil {
			a.logf("assistant: consommation non comptée : %v", err)
		}
		if resp.Stop == claudeapi.StopRefusal {
			cat := resp.Refusal
			if cat == "" {
				cat = "non précisée"
			}
			return false, fmt.Errorf("refus de Claude (catégorie : %s) — reformule, ou ouvre une nouvelle conversation", cat)
		}
		if resp.Stop == claudeapi.StopMaxTokens && len(resp.Calls) > 0 {
			// Un appel d'outil coupé n'est ni exécutable ni rejouable.
			return false, errors.New("réponse coupée au milieu d'un appel d'outil (limite de jetons de la réponse)")
		}
		now := a.now()
		entry := Entry{Role: RoleAssistant, At: now, Text: resp.Text, Raw: resp.Raw, Calls: resp.Calls, Usage: resp.Usage, Model: resp.Model}
		if len(resp.Calls) == 0 {
			if resp.Stop == claudeapi.StopMaxTokens {
				entry.Text += "\n\n[réponse coupée : limite de jetons atteinte]"
			}
			return false, db.Append(ctx, id, now, entry)
		}
		// Les lectures et écritures internes sont faites tout de suite ;
		// une action attend la confirmation de l'utilisateur.
		results := make([]claudeapi.Result, 0, len(resp.Calls))
		waiting := 0
		for _, c := range resp.Calls {
			if t := a.tool(c.Name); t != nil && t.Level == LevelAction {
				waiting++
				continue
			}
			results = append(results, a.runTool(ctx, c, false))
		}
		// L'appel et ses réponses s'écrivent ensemble.
		entries := []Entry{entry}
		if len(results) > 0 {
			entries = append(entries, Entry{Role: RoleTools, At: a.now(), Results: results})
		}
		if err := db.Append(ctx, id, now, entries...); err != nil {
			return false, err
		}
		if waiting > 0 {
			return true, nil
		}
		if err := ctx.Err(); err != nil {
			return false, fmt.Errorf("réponse interrompue : %w", err)
		}
	}
	return false, fmt.Errorf("pas de réponse après %d étapes — la demande est peut-être trop large, précise-la", maxSteps)
}

// explain rend une erreur du modèle lisible par l'utilisateur.
func explain(err error) error {
	switch {
	case errors.Is(err, claudeapi.ErrAuth):
		return fmt.Errorf("clé d'API Anthropic refusée — vérifie ~/.jarvis/assistant.json : %w", err)
	case errors.Is(err, claudeapi.ErrRateLimited):
		return fmt.Errorf("limite de débit de l'API atteinte, réessaie dans un instant : %w", err)
	case errors.Is(err, claudeapi.ErrOverloaded):
		return fmt.Errorf("API d'Anthropic indisponible, réessaie dans un instant : %w", err)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("réponse trop longue, interrompue : %w", err)
	}
	return err
}

// runTool exécute un appel. Tout échec devient une réponse en erreur : le
// modèle peut corriger son appel ou le dire à l'utilisateur.
func (a *Assistant) runTool(ctx context.Context, c claudeapi.Call, confirmed bool) (res claudeapi.Result) {
	res.CallID = c.ID
	fail := func(msg string) claudeapi.Result {
		return claudeapi.Result{CallID: c.ID, Content: msg, IsError: true}
	}
	tool := a.tool(c.Name)
	if tool == nil {
		return fail("outil inconnu : " + c.Name)
	}
	if tool.Level == LevelAction && !confirmed {
		// Garde-fou : turn ne passe jamais ici pour une action.
		return fail("action non exécutée : elle demande la confirmation de l'utilisateur")
	}
	input := c.Input
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	if !json.Valid(input) {
		return fail("entrée de l'outil illisible (JSON invalide)")
	}
	defer func() {
		if r := recover(); r != nil {
			a.logf("assistant: l'outil %s a paniqué : %v", c.Name, r)
			res = fail(fmt.Sprintf("erreur interne de l'outil %s : %v", c.Name, r))
		}
	}()
	out, err := tool.Run(ctx, input)
	if err != nil {
		return fail(err.Error())
	}
	max := a.MaxToolOutput
	if max <= 0 {
		max = DefaultMaxToolOutput
	}
	if n := utf8.RuneCountInString(out); n > max {
		out = string([]rune(out)[:max]) + fmt.Sprintf("\n[sortie coupée à %d caractères sur %d]", max, n)
	}
	if out == "" {
		out = "(aucun résultat)"
	}
	res.Content = out
	return res
}

var (
	frDays   = [...]string{"dimanche", "lundi", "mardi", "mercredi", "jeudi", "vendredi", "samedi"}
	frMonths = [...]string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"}
)

// system : la consigne, la date du jour et la mémoire. Pas l'heure : la
// consigne est en tête de chaque requête, et tout ce qui y change remet le
// cache à zéro.
func (a *Assistant) system(ctx context.Context, db Store) (string, error) {
	var b strings.Builder
	if a.Instructions != nil {
		b.WriteString(strings.TrimSpace(a.Instructions()))
	}
	now := a.now()
	fmt.Fprintf(&b, "\n\nAujourd'hui : %s %d %s %d.", frDays[now.Weekday()], now.Day(), frMonths[now.Month()-1], now.Year())
	facts, err := db.Facts(ctx)
	if err != nil {
		return "", fmt.Errorf("mémoire illisible : %w", err)
	}
	if len(facts) > 0 {
		b.WriteString("\n\nCe que tu as retenu des conversations précédentes :")
		for _, f := range facts {
			fmt.Fprintf(&b, "\n- %s [fait %s]", f.Text, f.ID)
		}
	}
	return b.String(), nil
}

// messages : le fil, au format du modèle. Un appel d'outil resté sans
// réponse (tour interrompu) reçoit une réponse en erreur : l'API refuse
// une conversation où il en manque une.
func messages(entries []Entry) []claudeapi.Message {
	var out []claudeapi.Message
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		switch e.Role {
		case RoleUser:
			out = append(out, claudeapi.Message{User: e.Text})
		case RoleAssistant:
			out = append(out, claudeapi.Message{Assistant: json.RawMessage(e.Raw)})
			if len(e.Calls) == 0 {
				continue
			}
			// Les réponses peuvent s'être écrites en plusieurs fois (une
			// action confirmée après coup) : elles partent en un seul tour.
			var given []claudeapi.Result
			for i+1 < len(entries) && entries[i+1].Role == RoleTools {
				given = append(given, entries[i+1].Results...)
				i++
			}
			results := make([]claudeapi.Result, 0, len(e.Calls))
			for _, c := range e.Calls {
				r := claudeapi.Result{CallID: c.ID, Content: "appel interrompu, aucun résultat", IsError: true}
				for _, g := range given {
					if g.CallID == c.ID {
						r = g
						break
					}
				}
				results = append(results, r)
			}
			out = append(out, claudeapi.Message{Results: results})
		}
	}
	return out
}

// Get, List, Delete : les conversations de l'utilisateur.
func (a *Assistant) Get(ctx context.Context, id string) (Conversation, bool, error) {
	return a.db(ctx).Get(ctx, id)
}

func (a *Assistant) List(ctx context.Context) ([]Conversation, error) {
	return a.db(ctx).List(ctx, 0)
}

func (a *Assistant) Delete(ctx context.Context, id string) error { return a.db(ctx).Delete(ctx, id) }

// Remember retient un fait (outil remember, ou l'utilisateur lui-même).
func (a *Assistant) Remember(ctx context.Context, text string) (Fact, error) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return Fact{}, errors.New("assistant: rien à retenir")
	}
	if utf8.RuneCountInString(text) > MaxFactLen {
		return Fact{}, fmt.Errorf("assistant: fait trop long (%d caractères au plus) — un fait est une phrase, pas un document", MaxFactLen)
	}
	f := Fact{ID: newID(), Text: text, At: a.now()}
	return f, a.db(ctx).AddFact(ctx, f)
}

func (a *Assistant) Facts(ctx context.Context) ([]Fact, error) { return a.db(ctx).Facts(ctx) }

func (a *Assistant) Forget(ctx context.Context, id string) error {
	return a.db(ctx).DeleteFact(ctx, id)
}

// UsageToday : la consommation du jour et le plafond (0 : aucun).
func (a *Assistant) UsageToday(ctx context.Context) (claudeapi.Usage, int, error) {
	u, err := a.db(ctx).UsageOn(ctx, a.now().Format("2006-01-02"))
	return u, a.DailyTokens, err
}

// RecoverOrphaned : au démarrage, les tours restés en cours sont marqués
// en échec (leur goroutine a disparu avec le processus précédent).
func (a *Assistant) RecoverOrphaned(ctx context.Context) (int, error) {
	r, ok := a.Store.(Recoverer)
	if !ok {
		return 0, nil
	}
	return r.FailRunning(ctx, "interrompu par un redémarrage de Jarvis — renvoie ton message", a.now())
}
