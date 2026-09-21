package doctype

import "github.com/JulianAndrieux/Jarvis/internal/schema"

// PieceIdentite est un document d'identité officiel (carte d'identité,
// passeport).
type PieceIdentite struct {
	Nom            schema.Field[string] `json:"nom" desc:"Nom de famille"`
	Prenom         schema.Field[string] `json:"prenom" desc:"Prénom"`
	DateNaissance  schema.Field[string] `json:"date_naissance" desc:"Date de naissance"`
	NumeroDocument schema.Field[string] `json:"numero_document" desc:"Numéro du document d'identité"`
	DateExpiration schema.Field[string] `json:"date_expiration" desc:"Date d'expiration du document"`
}
