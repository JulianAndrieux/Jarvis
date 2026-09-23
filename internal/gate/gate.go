// Package gate borne l'accès aux modèles locaux (jalon 27) : le
// traitement des documents et l'agent des tickets partagent les mêmes
// serveurs llama.cpp, qui échouent sous charge concurrente sur ce
// matériel (cf. jalon 21 bis, "Context size has been exceeded").
package gate

// Gate est un sémaphore partagé.
type Gate struct{ ch chan struct{} }

// New crée une porte laissant passer n détenteurs à la fois (n <= 0 : 1).
func New(n int) *Gate {
	if n <= 0 {
		n = 1
	}
	return &Gate{ch: make(chan struct{}, n)}
}

// Acquire attend son tour et retourne la fonction qui libère la place.
func (g *Gate) Acquire() func() {
	g.ch <- struct{}{}
	return func() { <-g.ch }
}
