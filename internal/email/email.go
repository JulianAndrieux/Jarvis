// Package email analyse un e-mail au format .eml (RFC 5322 + MIME) pour
// que l'application puisse l'afficher, l'indexer et le convertir en PDF.
//
// Tout est écrit à partir de la bibliothèque standard : net/mail et
// mime/multipart refusent en bloc un message dont un seul en-tête ou une
// seule partie est malformé, alors qu'un e-mail réel (export d'un client,
// copier-coller, mbox) l'est souvent un peu. Ici, l'en-tête et le découpage
// MIME sont lus à la main, au mieux : ce qui est illisible est ignoré, le
// reste est conservé, et aucune entrée ne provoque de panic.
package email

import (
	"bytes"
	"errors"
	"net/mail"
	"strings"
	"time"
)

// Address est une adresse d'expéditeur ou de destinataire.
type Address struct {
	Name  string // nom affiché, décodé (RFC 2047), peut être vide
	Email string
}

// Attachment est une pièce jointe, ou une ressource inline (image cid:)
// d'un multipart/related : l'application doit pouvoir la lister et la
// restituer, qu'elle soit affichée dans le corps ou non.
type Attachment struct {
	Filename    string // décodé (RFC 2047 et RFC 2231 filename*=), "" si absent
	ContentType string // ex. "application/pdf"
	Size        int    // taille décodée en octets
	Data        []byte // contenu décodé (base64/quoted-printable)
}

// Message est le résultat de l'analyse d'un e-mail.
type Message struct {
	From        []Address
	To          []Address
	Cc          []Address
	Subject     string    // décodé
	Date        time.Time // zéro si absent ou illisible
	Text        string    // corps text/plain décodé en UTF-8 ("" si absent)
	HTML        string    // corps text/html décodé en UTF-8 ("" si absent)
	Attachments []Attachment
}

// maxDepth borne l'imbrication des multipart : un message réel dépasse
// rarement 4 niveaux, une entrée hostile pourrait en empiler des milliers.
const maxDepth = 32

// Parse analyse un e-mail brut. Erreur seulement si raw n'est pas du tout
// un e-mail lisible (vide, ou aucun en-tête valide) ; un en-tête ou une
// partie malformés sont ignorés au mieux, jamais de panic.
func Parse(raw []byte) (Message, error) {
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")) // BOM UTF-8 d'un export Windows
	if len(bytes.TrimSpace(raw)) == 0 {
		return Message{}, errors.New("email : entrée vide")
	}
	headerBlock, body := splitHeaderBody(raw)
	h := parseHeader(headerBlock)
	if len(h) == 0 {
		return Message{}, errors.New("email : aucun en-tête valide, ce n'est pas un e-mail")
	}

	m := Message{
		From:    parseAddressList(h.get("From")),
		To:      parseAddressList(h.get("To")),
		Cc:      parseAddressList(h.get("Cc")),
		Subject: decodeHeaderText(h.get("Subject")),
	}
	if d, err := mail.ParseDate(h.get("Date")); err == nil {
		m.Date = d
	}
	walkEntity(&m, h, body, 0)
	return m, nil
}

// walkEntity range une entité MIME (le message lui-même ou une de ses
// parties) dans Message : corps texte/HTML, ou pièce jointe.
func walkEntity(m *Message, h header, body []byte, depth int) {
	ct, params := parseMediaType(h.get("Content-Type"))
	if ct == "" {
		ct = "text/plain" // défaut RFC 2045
	}
	disp, dparams := parseMediaType(h.get("Content-Disposition"))

	if strings.HasPrefix(ct, "multipart/") {
		if depth >= maxDepth {
			return
		}
		boundary := params["boundary"]
		if boundary == "" {
			// Boundary absente de l'en-tête : le corps révèle souvent le
			// délimiteur réellement utilisé, autant le lire que tout perdre.
			boundary = guessBoundary(body)
		}
		if boundary != "" {
			if parts := splitMultipart(body, boundary); len(parts) > 0 {
				for _, p := range parts {
					ph, pb := splitHeaderBody(p)
					walkEntity(m, parseHeader(ph), pb, depth+1)
				}
				return
			}
		}
		// Aucune structure exploitable : mieux vaut montrer le corps brut
		// comme du texte que ne rien montrer.
		ct, disp = "text/plain", ""
	}

	data := decodeTransfer(h.get("Content-Transfer-Encoding"), body)
	filename := attachmentName(dparams, params)

	// Une partie texte nommée ou marquée attachment est un fichier joint
	// (ex. un .txt ou un .html envoyé comme pièce), pas le corps du message.
	if disp != "attachment" && filename == "" && (ct == "text/plain" || ct == "text/html") {
		text := normalizeNewlines(decodeCharset(params["charset"], data))
		if text == "" {
			return
		}
		if ct == "text/plain" {
			m.Text = appendBody(m.Text, text)
		} else {
			m.HTML = appendBody(m.HTML, text)
		}
		return
	}

	m.Attachments = append(m.Attachments, Attachment{
		Filename:    filename,
		ContentType: ct,
		Size:        len(data),
		Data:        data,
	})
}

// appendBody concatène plusieurs parties inline de même type (un
// multipart/mixed peut couper le texte autour d'une pièce jointe).
func appendBody(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "\n\n" + add
}

func normalizeNewlines(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

// attachmentName retrouve le nom de fichier : Content-Disposition
// filename= d'abord (le champ prévu pour ça), sinon Content-Type name=
// (l'usage historique, encore très répandu).
func attachmentName(dparams, ctparams map[string]string) string {
	name := dparams["filename"]
	if name == "" {
		name = ctparams["name"]
	}
	if name == "" {
		return ""
	}
	// RFC 2231 (filename*=) est déjà décodé par mime.ParseMediaType ; le
	// RFC 2047 dans un paramètre est hors norme mais c'est ce qu'envoient
	// Outlook et Gmail, il faut donc le décoder aussi.
	if strings.Contains(name, "=?") {
		name = decodeHeaderText(name)
	}
	// Seul le dernier composant est gardé : l'appelant peut ainsi utiliser
	// le nom comme nom de fichier sans risque de traversée de répertoires.
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSpace(name)
	if name == "." || name == ".." {
		return ""
	}
	return name
}
