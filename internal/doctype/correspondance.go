package doctype

import "github.com/JulianAndrieux/Jarvis/internal/schema"

// Correspondance est un courrier écrit (lettre, email imprimé) entre un
// expéditeur et un destinataire.
type Correspondance struct {
	Expediteur   schema.Field[string] `json:"expediteur" desc:"Nom ou organisation de l'expéditeur"`
	Destinataire schema.Field[string] `json:"destinataire" desc:"Nom ou organisation du destinataire"`
	Date         schema.Field[string] `json:"date" desc:"Date du courrier"`
	Objet        schema.Field[string] `json:"objet" desc:"Objet ou sujet du courrier"`
}
