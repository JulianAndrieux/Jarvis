package parsing

import (
	"context"
	"fmt"
)

// FakeRenderer est une implémentation de test de Renderer : elle retourne
// des PNG préconfigurés par numéro de page, sans exécuter de binaire
// externe. Une page présente dans PNG réussit toujours ; pour toute autre
// page, Err est retourné s'il est configuré, sinon une erreur générique
// "page non configurée".
type FakeRenderer struct {
	PNG map[int][]byte
	Err error
}

func (r FakeRenderer) RenderPage(ctx context.Context, path string, page int, dpi int) ([]byte, error) {
	if png, ok := r.PNG[page]; ok {
		return png, nil
	}
	if r.Err != nil {
		return nil, r.Err
	}
	return nil, fmt.Errorf("parsing: fake renderer has no PNG for page %d", page)
}
