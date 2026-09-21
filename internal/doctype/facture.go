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
//
// Les descriptions de Facture/Devis/BonCommande sont volontairement
// écrites pour se distinguer explicitement les unes des autres (déjà
// payée vs proposition vs émise par le client) : ce texte est ce que
// voit le LLM de classification (internal/classify) pour choisir entre
// des types dont les champs se ressemblent beaucoup.
func NewDefaultRegistry() *Registry {
	r := NewRegistry()
	// Erreurs ignorées volontairement : les noms sont fixes et uniques par
	// construction ici, une collision indiquerait un bug de ce fichier,
	// pas une condition d'exécution à gérer.
	_ = Register[Facture](r, "facture", "Facture commerciale : demande de paiement pour des biens ou services déjà livrés ou rendus. Numéro, fournisseur, montant total TTC")
	_ = Register[Devis](r, "devis", "Devis commercial : proposition de prix avant achat, pas encore payée. Numéro, fournisseur, montant total, date de validité")
	_ = Register[BonCommande](r, "bon_commande", "Bon de commande : document émis par le client pour commander des biens ou services à un fournisseur, avant facturation. Numéro, fournisseur, client, montant total")
	_ = Register[PieceIdentite](r, "piece_identite", "Pièce d'identité officielle (carte d'identité, passeport) : nom, prénom, date de naissance, numéro de document, date d'expiration")
	_ = Register[Correspondance](r, "correspondance", "Correspondance écrite (lettre, courrier, email imprimé) entre un expéditeur et un destinataire, avec date et objet")
	return r
}
