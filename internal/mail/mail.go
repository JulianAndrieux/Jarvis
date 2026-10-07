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

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
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
	ID string `bson:"_id"`
	// Env : l'environnement propriétaire, renseigné par le Store. La
	// boîte est aujourd'hui celle de l'instance ; quand chaque user aura
	// la sienne (jalon 50), la portée se resserrera sur (env, user).
	Env         tenancy.EnvID `bson:"env_id"`
	Account     string        `bson:"account"`
	Mailbox     string        `bson:"mailbox"`
	UIDValidity uint32        `bson:"uid_validity"`
	UID         uint32        `bson:"uid"`
	MessageID   string        `bson:"message_id"`
	From        Address       `bson:"from"`
	To          []Address     `bson:"to"`
	Cc          []Address     `bson:"cc"`
	Subject     string        `bson:"subject"`
	Date        time.Time     `bson:"date"`
	// Text : le corps en texte (le HTML converti, jamais affiché tel quel).
	Text        string       `bson:"text"`
	Seen        bool         `bson:"seen"` // déjà lu dans la boîte à la relève
	Attachments []Attachment `bson:"attachments"`
	Triage      Triage       `bson:"triage"`
	FetchedAt   time.Time    `bson:"fetched_at"`
	// Archived : rangé dans Jarvis seulement, la boîte n'est jamais
	// modifiée (relève en lecture seule). Hors de Triage : un nouveau tri
	// ne le défait pas.
	Archived   bool      `bson:"archived"`
	ArchivedAt time.Time `bson:"archived_at"`
	// Payment : ce qu'il y a à payer, repéré par la passe dédiée après le
	// tri (PaymentAnalyzer).
	Payment Payment `bson:"payment"`
	// Paid : marqué payé par l'utilisateur. Hors de Payment, comme
	// Archived : une nouvelle analyse ne le défait pas.
	Paid   bool      `bson:"paid"`
	PaidAt time.Time `bson:"paid_at"`
}

// PaymentVersion : la version en vigueur de l'analyse des paiements. Un
// candidat analysé par une version plus ancienne (ou jamais) est relu.
const PaymentVersion = 1

// Payment : les informations pour payer, recopiées du texte de l'email et
// de ses PDF joints par le modèle local, puis contrôlées (Checks) — avec
// leur provenance (modèle, prompt).
type Payment struct {
	// ToPay : une somme que l'utilisateur doit régler lui-même (un
	// prélèvement automatique n'en est pas une : AutoDebit).
	ToPay     bool   `bson:"to_pay"`
	AutoDebit bool   `bson:"auto_debit"`
	Payee     string `bson:"payee"`
	Amount    string `bson:"amount"` // tel qu'écrit : "1 234,56"
	Currency  string `bson:"currency"`
	DueDate   string `bson:"due_date"` // "AAAA-MM-JJ" ou ""
	Reference string `bson:"reference"`
	IBAN      string `bson:"iban"` // normalisé, sans espaces
	IBANValid bool   `bson:"iban_valid"`
	BIC       string `bson:"bic"`
	Method    string `bson:"method"` // virement|prelevement|carte|lien|cheque|autre|""
	// Link : lien de paiement, affiché en texte (jamais cliquable :
	// il vient d'un email).
	Link        string  `bson:"link"`
	Evidence    string  `bson:"evidence"` // extrait source
	Confidence  float64 `bson:"confidence"`
	NeedsReview bool    `bson:"needs_review"`
	// Checks : les contrôles déterministes appliqués après le modèle,
	// lisibles ; Sources : "corps" et les noms des PDF lus.
	Checks  []string  `bson:"checks"`
	Sources []string  `bson:"sources"`
	Version int       `bson:"version"`
	Model   string    `bson:"model"`
	Prompt  string    `bson:"prompt"`
	Error   string    `bson:"error"` // analyse impossible : pas retentée seule
	At      time.Time `bson:"at"`
}

// PaymentCandidate : l'email est lu par l'analyse des paiements — trié par
// la version en vigueur, sans erreur, « à traiter » ou « document ».
func PaymentCandidate(m Mail) bool {
	return m.Triage.Version >= TriageVersion && m.Triage.Error == "" &&
		(m.Triage.Category == Action || m.Triage.Category == Document)
}

// ArchiveFilter : la place des emails archivés dans une liste.
type ArchiveFilter int

const (
	ArchiveHide ArchiveFilter = iota // défaut : sans les archivés
	ArchiveOnly                      // seulement les archivés
	ArchiveAny                       // tous (le tri, par exemple)
)

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
	// NoReply : triés par la version en vigueur, sans erreur, sans réponse
	// attendue (un email pas encore lu par le modèle n'y est jamais).
	NoReply bool
	// PaymentPending : candidats à l'analyse des paiements pas encore
	// analysés par la version en vigueur (une erreur est notée avec la
	// version : pas retentée seule, comme le tri).
	PaymentPending bool
	// ToPay : quelque chose à payer, pas encore marqué payé.
	ToPay   bool
	Archive ArchiveFilter
	Limit   int // 0 : DefaultLimit
}

// DefaultLimit : taille d'une liste.
const DefaultLimit = 200

// Store persiste les emails (MongoStore en production, FakeStore en test).
type Store interface {
	// For rend la même persistance vue depuis un environnement. Un store
	// sans portée refuse toute opération.
	For(scope tenancy.Scope) Store
	// Save enregistre un email et le contenu de ses pièces jointes (par
	// index) ; déjà présent (même ID) : rien ne change, created=false.
	Save(ctx context.Context, m Mail, files map[int][]byte) (created bool, err error)
	Get(ctx context.Context, id string) (Mail, bool, error)
	// List : du plus récent au plus ancien.
	List(ctx context.Context, q Query) ([]Mail, error)
	// Count : le nombre d'emails correspondant à q (Limit ignoré).
	Count(ctx context.Context, q Query) (int, error)
	SetTriage(ctx context.Context, id string, t Triage) error
	// SetArchived : écriture ciblée de l'archivage (archived=false remet la
	// date à zéro) ; email inconnu : erreur.
	SetArchived(ctx context.Context, id string, archived bool, at time.Time) error
	// ArchiveMatching archive les emails non archivés correspondant à q
	// (Limit ignoré) et rend leur nombre.
	ArchiveMatching(ctx context.Context, q Query, at time.Time) (int, error)
	// SetPayment : écriture ciblée de l'analyse des paiements ; email
	// inconnu : erreur.
	SetPayment(ctx context.Context, id string, p Payment) error
	// SetPaid : écriture ciblée de la marque « payé » (paid=false remet la
	// date à zéro) ; email inconnu : erreur.
	SetPaid(ctx context.Context, id string, paid bool, at time.Time) error
	SetAttachmentDoc(ctx context.Context, id string, index int, docID string) error
	Attachment(ctx context.Context, id string, index int) ([]byte, bool, error)
	// LastUID : le plus grand UID relevé pour ce compte, cette boîte et
	// cette UIDVALIDITY (0 : aucun).
	LastUID(ctx context.Context, account, mailbox string, uidValidity uint32) (uint32, error)
}

// scopedStore : la persistance vue depuis la portée du contexte, ou depuis
// fallback quand le contexte n'en porte pas (travail de fond démarré au
// lancement). fallback vide vaut l'unique environnement d'aujourd'hui —
// choix de câblage que le jalon 45 remplacera par la portée de la session.
func scopedStore(ctx context.Context, st Store, fallback tenancy.Scope) Store {
	if sc, ok := tenancy.FromContext(ctx); ok {
		return st.For(sc)
	}
	if fallback.Env == "" {
		fallback = tenancy.Scope{Env: tenancy.Local, Role: tenancy.RoleOwner}
	}
	return st.For(fallback)
}
