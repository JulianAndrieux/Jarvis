package doctype

import "github.com/JulianAndrieux/Jarvis/internal/schema"

// Devis est une proposition de prix avant achat, pas encore payée — se
// distingue de Facture (demande de paiement pour des biens/services déjà
// livrés/rendus) par ce statut "avant achat", précisé dans sa
// description pour aider la classification automatique (internal/
// classify) à ne pas confondre les deux.
type Devis struct {
	Numero       schema.Field[string]  `json:"numero" desc:"Numéro du devis"`
	Fournisseur  schema.Field[string]  `json:"fournisseur" desc:"Nom du fournisseur"`
	MontantTotal schema.Field[float64] `json:"montant_total" desc:"Montant total proposé, en euros"`
	DateValidite schema.Field[string]  `json:"date_validite" desc:"Date jusqu'à laquelle le devis est valide"`
}
