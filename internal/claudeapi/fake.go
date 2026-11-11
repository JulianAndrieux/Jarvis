package claudeapi

import (
	"context"
	"errors"
	"sync"
)

// FakeClient rend des réponses préparées, dans l'ordre, et garde les
// requêtes reçues. Sûr en concurrence.
type FakeClient struct {
	mu sync.Mutex
	// Responses : une par appel ; épuisées, Complete rend une erreur.
	Responses []Response
	// Errs : erreur du n-ième appel (nil : pas d'erreur), prioritaire.
	Errs []error
	// Hook, s'il est posé, est appelé avant de répondre (pour bloquer un
	// test au milieu d'un échange).
	Hook  func(n int, req Request)
	Calls []Request
}

// ErrFakeExhausted : plus de réponse préparée.
var ErrFakeExhausted = errors.New("claudeapi: fake sans réponse préparée")

func (f *FakeClient) Complete(ctx context.Context, req Request) (Response, error) {
	f.mu.Lock()
	n := len(f.Calls)
	f.Calls = append(f.Calls, req)
	hook := f.Hook
	f.mu.Unlock()
	if hook != nil {
		hook(n, req)
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if n < len(f.Errs) && f.Errs[n] != nil {
		return Response{}, f.Errs[n]
	}
	if n >= len(f.Responses) {
		return Response{}, ErrFakeExhausted
	}
	return f.Responses[n], nil
}

// Requests : copie des requêtes reçues.
func (f *FakeClient) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.Calls...)
}
