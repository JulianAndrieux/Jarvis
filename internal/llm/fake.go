package llm

import (
	"context"
	"fmt"
)

// FakeClient est une implémentation de test de Client : elle retourne des
// résultats préconfigurés par numéro de page, sans appeler de service
// externe. Elle enregistre les requêtes reçues pour permettre aux tests
// d'asserter sur ce qui a été envoyé au LLM.
type FakeClient struct {
	// Results associe un numéro de page à son résultat. Une page absente
	// de cette map fait échouer Extract.
	Results map[int]ExtractResult
	// Err, si non-nil, fait échouer tous les appels avec cette erreur,
	// quelle que soit la page.
	Err error

	Calls []ExtractRequest
}

func (f *FakeClient) Extract(ctx context.Context, req ExtractRequest) (ExtractResult, error) {
	f.Calls = append(f.Calls, req)

	if f.Err != nil {
		return ExtractResult{}, f.Err
	}

	result, ok := f.Results[req.Page]
	if !ok {
		return ExtractResult{}, fmt.Errorf("llm: fake has no configured result for page %d", req.Page)
	}
	return result, nil
}
