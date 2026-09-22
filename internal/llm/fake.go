package llm

import (
	"context"
	"fmt"
	"sync"
)

// FakeClient est une implémentation de test de Client : elle retourne des
// résultats préconfigurés par numéro de page, sans appeler de service
// externe. Elle enregistre les requêtes reçues pour permettre aux tests
// d'asserter sur ce qui a été envoyé au LLM.
//
// Calls est protégé par un mutex : depuis que internal/extraction peut
// vraiment appeler Extract en parallèle (Extractor.Concurrency, jalon
// 21), un test exerçant ce chemin avec une FakeClient partagée déclenche
// sinon une vraie data race sur cet append — trouvé par `go test -race`
// en ajoutant ce jalon, pas en relecture.
type FakeClient struct {
	// Results associe un numéro de page à son résultat. Une page absente
	// de cette map fait échouer Extract.
	Results map[int]ExtractResult
	// Err, si non-nil, fait échouer tous les appels avec cette erreur,
	// quelle que soit la page.
	Err error

	mu    sync.Mutex
	Calls []ExtractRequest
}

func (f *FakeClient) Extract(ctx context.Context, req ExtractRequest) (ExtractResult, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, req)
	f.mu.Unlock()

	if f.Err != nil {
		return ExtractResult{}, f.Err
	}

	result, ok := f.Results[req.Page]
	if !ok {
		return ExtractResult{}, fmt.Errorf("llm: fake has no configured result for page %d", req.Page)
	}
	return result, nil
}
