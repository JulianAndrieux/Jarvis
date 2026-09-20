package classify

import "context"

// FakeClassifier est une implémentation de test de Classifier : retourne
// un résultat préconfiguré, enregistre le dernier appel reçu.
type FakeClassifier struct {
	Result Result
	Err    error

	GotText       string
	GotCandidates []Candidate
}

func (f *FakeClassifier) Classify(ctx context.Context, text string, candidates []Candidate) (Result, error) {
	f.GotText = text
	f.GotCandidates = candidates
	if f.Err != nil {
		return Result{}, f.Err
	}
	return f.Result, nil
}
