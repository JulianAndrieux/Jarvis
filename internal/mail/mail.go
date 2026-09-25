// Package mail relève la boîte de réception dans Jarvis (jalon 39) : les
// emails sont copiés dans MongoDB Atlas (décision de l'utilisateur, cf.
// CLAUDE.md), triés et résumés par le modèle local ; leurs pièces jointes
// peuvent rejoindre la bibliothèque de documents, et un email peut donner
// une tâche ou une note. Relève en lecture seule (IMAP, internal/imap),
// lecture du message brut par internal/email.
package mail

import (
	"context"
	"time"
)

// Mailbox : la seule boîte relevée pour l'instant.
const Mailbox = "INBOX"

// Address : un correspondant.
type Address struct {
	Name  string `bson:"name"`
	Email string `bson:"email"`
}

// String : « Nom <adresse> », ou l'adresse seule.
func (a Address) String() string {
	switch {
	case a.Name != "" && a.Email != "":
		return a.Name + " <" + a.Email + ">"
	case a.Name != "":
		return a.Name
	}
	return a.Email
}

// Attachment : une pièce jointe. Son contenu est stocké à part (Store.
// Attachment), s'il n'est pas trop gros (Stored).
type Attachment struct {
	Index       int    `bson:"index"`
	Filename    string `bson:"filename"`
	ContentType string `bson:"content_type"`
	Size        int    `bson:"size"`
	// Stored : contenu copié (au-delà de MaxStoredAttachment, seule la
	// fiche est gardée : la limite d'un document MongoDB est de 16 Mo).
	Stored bool `bson:"stored"`
	// DocID : document de la bibliothèque créé depuis cette pièce jointe.
	DocID string `bson:"doc_id"`
}

// Category : le tri proposé par le modèle local.
type Category string

const (
	Action       Category = "a_traiter"
	Info         Category = "information"
	Document     Category = "document"
	Newsletter   Category = "newsletter"
	Notification Category = "notification"
)

// Categories : dans l'ordre d'affichage.
var Categories = []Category{Action, Document, Info, Notification, Newsletter}

// CategoryLabel : libellé affiché ("" : pas encore trié).
func CategoryLabel(c Category) string {
	switch c {
	case Action:
		return "À traiter"
	case Document:
		return "Document / facture"
	case Info:
		return "Information"
	case Notification:
		return "Notification"
	case Newsletter:
		return "Newsletter / promo"
	case "":
		return "À trier"
	}
	return string(c)
}

// TriageVersion : la version en vigueur du tri. Un email trié par une
// version plus ancienne est retrié (la 2 ajoute les emails à répondre).
const TriageVersion = 2

// Triage : catégorie, résumé et action suggérée, avec leur provenance
// (modèle, prompt) — cf. la contrainte de reproductibilité.
type Triage struct {
	Category Category `bson:"category"`
	Summary  string   `bson:"summary"`
	// Action : la tâche suggérée ("" : rien à faire).
	Action string `bson:"action"`
	// Reply : l'expéditeur attend une réponse de l'utilisateur ; Question :
	// ce qu'il lui demande, en une phrase. Les autres emails sont masqués
	// de la vue par défaut.
	Reply    bool   `bson:"reply"`
	Question string `bson:"question"`
	// Version : TriageVersion au moment du tri (0 : pas trié, ou trié avant
	// la version 2).
	Version int       `bson:"version"`
	Model   string    `bson:"model"`
	Prompt  string    `bson:"prompt"`
	Error   string    `bson:"error"` // tri impossible : pas retenté seul
	At      time.Time `bson:"at"`
}

// Mail : un email relevé.
type Mail struct {
	ID          string    `bson:"_id"`
	Account     string    `bson:"account"`
	Mailbox     string    `bson:"mailbox"`
	UIDValidity uint32    `bson:"uid_validity"`
	UID         uint32    `bson:"uid"`
	MessageID   string    `bson:"message_id"`
	From        Address   `bson:"from"`
	To          []Address `bson:"to"`
	Cc          []Address `bson:"cc"`
	Subject     string    `bson:"subject"`
	Date        time.Time `bson:"date"`
	// Text : le corps en texte (le HTML converti, jamais affiché tel quel).
	Text        string       `bson:"text"`
	Seen        bool         `bson:"seen"` // déjà lu dans la boîte à la relève
	Attachments []Attachment `bson:"attachments"`
	Triage      Triage       `bson:"triage"`
	FetchedAt   time.Time    `bson:"fetched_at"`
}

// Query filtre une liste d'emails.
type Query struct {
	// Search : sous-chaîne (insensible à la casse) de l'objet, de
	// l'expéditeur, du texte ou du résumé.
	Search   string
	Category Category
	// Reply : seulement ceux qui attendent une réponse de l'utilisateur.
	Reply bool
	// Untriaged : seulement ceux que le modèle n'a pas encore triés, ou
	// triés par une version plus ancienne que TriageVersion (un tri en
	// erreur de la version en vigueur n'y est plus).
	Untriaged bool
	Limit     int // 0 : DefaultLimit
}

// DefaultLimit : taille d'une liste.
const DefaultLimit = 200

// Store persiste les emails (MongoStore en production, FakeStore en test).
type Store interface {
	// Save enregistre un email et le contenu de ses pièces jointes (par
	// index) ; déjà présent (même ID) : rien ne change, created=false.
	Save(ctx context.Context, m Mail, files map[int][]byte) (created bool, err error)
	Get(ctx context.Context, id string) (Mail, bool, error)
	// List : du plus récent au plus ancien.
	List(ctx context.Context, q Query) ([]Mail, error)
	// Count : le nombre d'emails correspondant à q (Limit ignoré).
	Count(ctx context.Context, q Query) (int, error)
	SetTriage(ctx context.Context, id string, t Triage) error
	SetAttachmentDoc(ctx context.Context, id string, index int, docID string) error
	Attachment(ctx context.Context, id string, index int) ([]byte, bool, error)
	// LastUID : le plus grand UID relevé pour ce compte, cette boîte et
	// cette UIDVALIDITY (0 : aucun).
	LastUID(ctx context.Context, account, mailbox string, uidValidity uint32) (uint32, error)
}
