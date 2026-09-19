package doctype

import "github.com/JulianAndrieux/Jarvis/internal/schema"

// Facture est un exemple de type de document enregistré, pour amorcer le
// registre. D'autres types (pièce d'identité, correspondance, document
// technique, ...) s'ajoutent de la même façon, via Register, sans changer
// internal/schema ni internal/extraction.
type Facture struct {
	Numero      schema.Field[string]  `json:"numero" desc:"Numéro de la facture"`
	Fournisseur schema.Field[string]  `json:"fournisseur" desc:"Nom du fournisseur"`
	TotalTTC    schema.Field[float64] `json:"total_ttc" desc:"Montant total TTC, en euros"`
}

// NewDefaultRegistry crée un registre avec les types de documents fournis
// par jarvis. Un appelant peut aussi construire son propre *Registry et
// y enregistrer des types supplémentaires.
func NewDefaultRegistry() *Registry {
	r := NewRegistry()
	// Erreur ignorée volontairement : le nom est fixe et unique par
	// construction ici, une collision indiquerait un bug de ce fichier,
	// pas une condition d'exécution à gérer.
	_ = Register[Facture](r, "facture", "Facture commerciale : numéro, fournisseur, montant total TTC")
	return r
}
