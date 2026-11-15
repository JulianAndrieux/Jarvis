package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/mcp"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// Request : une réponse à préparer — les consignes, la conversation et les
// outils offerts au modèle.
type Request struct {
	System string
	Prompt string
	Tools  []mcp.Tool
}

// Reply : la réponse du modèle.
type Reply struct {
	Text    string
	CostUSD float64
	Turns   int
}

// Runner fait répondre le modèle (Claude Code en production, une fake dans
// les tests), en lui laissant appeler les outils autant qu'il le faut.
type Runner interface {
	Run(ctx context.Context, req Request) (Reply, error)
}

// DefaultTimeout : durée maximale d'une réponse.
const DefaultTimeout = 10 * time.Minute

// Service : les conversations avec l'assistant.
type Service struct {
	Store  Store
	Runner Runner
	Tools  *Toolbox
	// Instructions : les consignes en vigueur (nil : DefaultInstructions) —
	// relues à chaque réponse.
	Instructions func() string
	// Timeout : durée maximale d'une réponse (0 : DefaultTimeout).
	Timeout time.Duration
	Now     func() time.Time
	Log     func(format string, args ...any)
	// DefaultScope : la portée quand le contexte n'en porte pas (choix de
	// câblage, comme pour les autres services).
	DefaultScope tenancy.Scope

	wg sync.WaitGroup
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return DefaultTimeout
}

// BusyLimit : au-delà, une réponse « en cours » est tenue pour abandonnée
// (cf. Conversation.Active).
func (s *Service) BusyLimit() time.Duration { return s.timeout() + time.Minute }

func (s *Service) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}

func (s *Service) scope(ctx context.Context) tenancy.Scope {
	if scope, ok := tenancy.FromContext(ctx); ok {
		return scope
	}
	return s.DefaultScope
}

func (s *Service) db(ctx context.Context) Store { return s.Store.For(s.scope(ctx)) }

func (s *Service) Get(ctx context.Context, id string) (Conversation, bool, error) {
	return s.db(ctx).Get(ctx, id)
}

func (s *Service) List(ctx context.Context) ([]Conversation, error) { return s.db(ctx).List(ctx) }

func (s *Service) Delete(ctx context.Context, id string) error { return s.db(ctx).Delete(ctx, id) }

// MaxQuestion : longueur maximale d'une question.
const MaxQuestion = 20000

func cleanQuestion(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("la question est vide")
	}
	if len([]rune(text)) > MaxQuestion {
		return "", fmt.Errorf("la question dépasse %d caractères", MaxQuestion)
	}
	return text, nil
}

// Start ouvre une conversation sur une première question.
func (s *Service) Start(ctx context.Context, text string) (Conversation, error) {
	text, err := cleanQuestion(text)
	if err != nil {
		return Conversation{}, err
	}
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return Conversation{}, fmt.Errorf("assistant: identifiant : %w", err)
	}
	now := s.now()
	first, _, _ := strings.Cut(text, "\n")
	c := Conversation{ID: hex.EncodeToString(raw), Title: clip(strings.TrimSpace(first), 70), CreatedAt: now, UpdatedAt: now}
	if err := s.db(ctx).Create(ctx, c); err != nil {
		return Conversation{}, err
	}
	if err := s.Ask(ctx, c.ID, text); err != nil {
		return Conversation{}, err
	}
	return c, nil
}

// Ask enregistre une question et prépare la réponse en arrière-plan.
// ErrBusy si une réponse est déjà en préparation dans cette conversation.
func (s *Service) Ask(ctx context.Context, id, text string) error {
	text, err := cleanQuestion(text)
	if err != nil {
		return err
	}
	if s.Runner == nil {
		return errors.New("assistant: aucun modèle configuré")
	}
	scope := s.scope(ctx)
	now := s.now()
	if err := s.Store.For(scope).Begin(ctx, id, Message{Role: RoleUser, Text: text, At: now}, now.Add(-s.BusyLimit())); err != nil {
		return err
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.answer(scope, id)
	}()
	return nil
}

// Wait attend la fin des réponses en préparation (tests, arrêt).
func (s *Service) Wait() { s.wg.Wait() }

// answer prépare la réponse : les outils agissent avec la portée de celui
// qui a posé la question — sa session comprise, donc ses actions sont
// journalisées et passent par son changeset comme s'il les avait faites.
func (s *Service) answer(scope tenancy.Scope, id string) {
	base := tenancy.WithScope(context.Background(), scope)
	store := s.Store.For(scope)
	system := DefaultInstructions
	if s.Instructions != nil {
		system = s.Instructions()
	}
	system = Instructions(system, s.now())

	var mu sync.Mutex
	var steps []Step
	record := func(st Step) {
		mu.Lock()
		steps = append(steps, st)
		mu.Unlock()
		ctx, cancel := context.WithTimeout(base, 15*time.Second)
		defer cancel()
		if err := store.AddStep(ctx, id, st); err != nil {
			s.logf("assistant: étape de %s non enregistrée : %v", id, err)
		}
	}

	reply, err := s.run(base, store, id, system, record)

	mu.Lock()
	msg := Message{Role: RoleAssistant, Steps: append([]Step(nil), steps...), At: s.now(), System: system}
	mu.Unlock()
	switch {
	case err != nil:
		msg.Error = err.Error()
	case strings.TrimSpace(reply.Text) == "":
		msg.Error = "le modèle n'a rien répondu"
	default:
		msg.Text, msg.CostUSD, msg.Turns = strings.TrimSpace(reply.Text), reply.CostUSD, reply.Turns
	}
	ctx, cancel := context.WithTimeout(base, 30*time.Second)
	defer cancel()
	if err := store.Finish(ctx, id, msg); err != nil {
		s.logf("assistant: réponse de %s non enregistrée : %v", id, err)
	}
}

// run fait répondre le modèle ; une panique devient une erreur de la
// réponse, elle n'emporte pas le processus.
func (s *Service) run(base context.Context, store Store, id, system string, record func(Step)) (reply Reply, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("assistant: arrêt inattendu : %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(base, s.timeout())
	defer cancel()
	conv, ok, err := store.Get(ctx, id)
	if err != nil {
		return Reply{}, err
	}
	if !ok || len(conv.Messages) == 0 {
		return Reply{}, fmt.Errorf("%w : %s", ErrNotFound, id)
	}
	var tools []mcp.Tool
	if s.Tools != nil {
		tools = s.Tools.Tools(record)
	}
	reply, err = s.Runner.Run(ctx, Request{System: system, Prompt: Prompt(conv.Messages), Tools: tools})
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		return Reply{}, fmt.Errorf("réponse arrêtée après %s", s.timeout())
	}
	return reply, err
}
