package bbox

import "context"

// FakeExtractor est une implémentation de test de Extractor : elle
// retourne des valeurs préconfigurées sans toucher au système de fichiers
// ni exécuter de binaire externe.
type FakeExtractor struct {
	Pages []PageWords
	Err   error
}

func (f FakeExtractor) ExtractWords(ctx context.Context, path string) ([]PageWords, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Pages, nil
}
