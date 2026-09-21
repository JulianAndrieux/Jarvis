package doctype

import "github.com/JulianAndrieux/Jarvis/internal/schema"

// BonCommande est émis par un client pour commander des biens/services à
// un fournisseur, avant toute facturation — se distingue de Facture
// (paiement) et Devis (proposition côté fournisseur) par le fait que
// c'est le client qui l'émet, précisé dans sa description pour la
// classification automatique.
type BonCommande struct {
	Numero       schema.Field[string]  `json:"numero" desc:"Numéro du bon de commande"`
	Fournisseur  schema.Field[string]  `json:"fournisseur" desc:"Nom du fournisseur destinataire de la commande"`
	Client       schema.Field[string]  `json:"client" desc:"Nom du client qui passe la commande"`
	MontantTotal schema.Field[float64] `json:"montant_total" desc:"Montant total commandé, en euros"`
}
