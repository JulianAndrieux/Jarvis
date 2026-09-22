package vlm

import (
	"context"
	"fmt"
	"sync"
)

// FakeClient est une implémentation de test de Client : elle retourne des
// résultats préconfigurés par numéro de page, sans appeler de service
// externe. Elle enregistre les images reçues pour permettre aux tests
// d'asserter sur ce qui a été envoyé au VLM.
//
// Calls est protégé par un mutex : depuis que internal/parsing peut
// vraiment appeler ParsePage en parallèle (Parser.Concurrency, jalon 21),
// un test exerçant ce chemin avec une FakeClient partagée déclenche sinon
// une vraie data race sur cet append.
type FakeClient struct {
	// Results associe un numéro de page à son résultat. Une page absente
	// de cette map fait échouer ParsePage.
	Results map[int]ParseResult
	// Err, si non-nil, fait échouer tous les appels avec cette erreur,
	// quelle que soit la page.
	Err error

	mu    sync.Mutex
	Calls []PageImage
}

func (f *FakeClient) ParsePage(ctx context.Context, img PageImage) (ParseResult, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, img)
	f.mu.Unlock()

	if f.Err != nil {
		return ParseResult{}, f.Err
	}

	result, ok := f.Results[img.Page]
	if !ok {
		return ParseResult{}, fmt.Errorf("vlm: fake has no configured result for page %d", img.Page)
	}
	return result, nil
}
