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
	// LevelAction : agit hors de Jarvis (envoyer, payer, déployer). Jamais
	// exécuté sans la confirmation de l'utilisateur.
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
	// Le départ d'abord : un message refusé (tour déjà en cours) ne doit
	// pas se retrouver dans le fil.
	started, err := db.Start(ctx, id, now)
	if err != nil {
		return Conversation{}, err
	}
	if !started {
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

	timeout := a.TurnTimeout
	if timeout <= 0 {
		timeout = DefaultTurnTimeout
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		// Détaché de la requête, mais avec sa portée : le tour survit à la
		// page qui l'a lancé, et les outils agissent au nom du même user.
		run, cancel := context.WithTimeout(tenancy.WithScope(context.Background(), sc), timeout)
		defer cancel()
		msg := ""
		if err := a.turn(run, db, id); err != nil {
			msg = err.Error()
			a.logf("assistant: conversation %s : %v", id, err)
		}
		// La clôture ne dépend pas du délai du tour : c'est elle qui rend
		// la conversation utilisable de nouveau.
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelClose()
		if err := db.Finish(closeCtx, id, msg, a.now()); err != nil {
			a.logf("assistant: clôture de la conversation %s : %v", id, err)
		}
	}()
	return c, nil
}

func title(text string) string {
	line := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	if utf8.RuneCountInString(line) <= titleLen {
		return line
	}
	return string([]rune(line)[:titleLen-1]) + "…"
}

// turn : un tour complet — appeler le modèle, exécuter ses outils,
// recommencer jusqu'à sa réponse.
func (a *Assistant) turn(ctx context.Context, db Store, id string) error {
	maxSteps := a.MaxSteps
	if maxSteps <= 0 {
		maxSteps = DefaultMaxSteps
	}
	system, err := a.system(ctx, db)
	if err != nil {
		return err
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
				return err
			}
			if used.Total() >= a.DailyTokens {
				return fmt.Errorf("plafond quotidien atteint (%d jetons sur %d) — il se remet à zéro demain, ou se règle avec --assistant-daily-tokens", used.Total(), a.DailyTokens)
			}
		}
		conv, ok, err := db.Get(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w : conversation supprimée pendant la réponse", ErrNotFound)
		}
		resp, err := a.Model.Complete(ctx, claudeapi.Request{System: system, Tools: tools, Messages: messages(conv.Entries)})
		if err != nil {
			return explain(err)
		}
		if err := db.AddUsage(ctx, day, resp.Usage); err != nil {
			a.logf("assistant: consommation non comptée : %v", err)
		}
		if resp.Stop == claudeapi.StopRefusal {
			cat := resp.Refusal
			if cat == "" {
				cat = "non précisée"
			}
			return fmt.Errorf("refus de Claude (catégorie : %s) — reformule, ou ouvre une nouvelle conversation", cat)
		}
		if resp.Stop == claudeapi.StopMaxTokens && len(resp.Calls) > 0 {
			// Un appel d'outil coupé n'est ni exécutable ni rejouable.
			return errors.New("réponse coupée au milieu d'un appel d'outil (limite de jetons de la réponse)")
		}
		now := a.now()
		entry := Entry{Role: RoleAssistant, At: now, Text: resp.Text, Raw: resp.Raw, Calls: resp.Calls, Usage: resp.Usage, Model: resp.Model}
		if len(resp.Calls) == 0 {
			if resp.Stop == claudeapi.StopMaxTokens {
				entry.Text += "\n\n[réponse coupée : limite de jetons atteinte]"
			}
			return db.Append(ctx, id, now, entry)
		}
		results := make([]claudeapi.Result, 0, len(resp.Calls))
		for _, c := range resp.Calls {
			results = append(results, a.runTool(ctx, c))
		}
		// L'appel et ses réponses s'écrivent ensemble : le fil ne contient
		// jamais une demande d'outil sans réponse.
		if err := db.Append(ctx, id, now, entry, Entry{Role: RoleTools, At: a.now(), Results: results}); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("réponse interrompue : %w", err)
		}
	}
	return fmt.Errorf("pas de réponse après %d étapes — la demande est peut-être trop large, précise-la", maxSteps)
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
func (a *Assistant) runTool(ctx context.Context, c claudeapi.Call) (res claudeapi.Result) {
	res.CallID = c.ID
	fail := func(msg string) claudeapi.Result {
		return claudeapi.Result{CallID: c.ID, Content: msg, IsError: true}
	}
	var tool *Tool
	for i := range a.Tools {
		if a.Tools[i].Name == c.Name {
			tool = &a.Tools[i]
		}
	}
	if tool == nil {
		return fail("outil inconnu : " + c.Name)
	}
	if tool.Level == LevelAction {
		return fail("action non exécutée : elle demande la confirmation de l'utilisateur, qui n'est pas encore disponible dans Jarvis. Dis-lui ce que tu aurais fait.")
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
			var given []claudeapi.Result
			if i+1 < len(entries) && entries[i+1].Role == RoleTools {
				given = entries[i+1].Results
				i++
			}
			results := make([]claudeapi.Result, 0, len(e.Calls))
			for _, c := range e.Calls {
				r := claudeapi.Result{CallID: c.ID, Content: "appel interrompu, aucun résultat", IsError: true}
				for _, g := range given {
					if g.CallID == c.ID {
						r = g
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
