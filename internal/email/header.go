package email

import (
	"bytes"
	"io"
	"mime"
	"net/mail"
	"net/textproto"
	"strings"
	"unicode/utf8"
)

// header associe un nom d'en-tête canonique (textproto) à ses valeurs,
// dépliées (RFC 5322 §2.2.3) et débarrassées des blancs de bord.
type header map[string][]string

func (h header) get(key string) string {
	if v := h[textproto.CanonicalMIMEHeaderKey(key)]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// splitHeaderBody sépare le bloc d'en-têtes du corps à la première ligne
// vide, en CRLF comme en LF seul. Sans ligne vide, tout est en-tête.
func splitHeaderBody(b []byte) (headerBlock, body []byte) {
	pos := 0
	for pos < len(b) {
		end := bytes.IndexByte(b[pos:], '\n')
		line, next := b[pos:], len(b)
		if end >= 0 {
			line, next = b[pos:pos+end], pos+end+1
		}
		if len(bytes.TrimRight(line, "\r")) == 0 {
			return b[:pos], b[next:]
		}
		pos = next
	}
	return b, nil
}

// parseHeader lit un bloc d'en-têtes ligne à ligne plutôt que via
// textproto.ReadMIMEHeader, qui abandonne tout le bloc à la première ligne
// malformée : ici, une ligne sans nom de champ valide (ligne "From " de
// mbox, texte égaré sans deux-points) est simplement ignorée, avec ses
// éventuelles lignes de continuation.
func parseHeader(block []byte) header {
	h := header{}
	var key string // champ en cours, "" si la ligne précédente a été rejetée
	var val strings.Builder
	flush := func() {
		if key != "" {
			h[key] = append(h[key], ensureUTF8(strings.TrimSpace(val.String())))
		}
		key = ""
		val.Reset()
	}
	for _, rawLine := range strings.Split(string(block), "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if key != "" {
				val.WriteString(line) // dépliage : seul le saut de ligne disparaît
			}
			continue
		}
		flush()
		i := strings.IndexByte(line, ':')
		if i <= 0 {
			continue
		}
		name := strings.TrimRight(line[:i], " \t") // "Subject :" (syntaxe obsolète) accepté
		if !validFieldName(name) {
			continue
		}
		key = textproto.CanonicalMIMEHeaderKey(name)
		val.WriteString(line[i+1:])
	}
	flush()
	return h
}

// validFieldName applique RFC 5322 §3.6.8 : caractères imprimables sauf
// espace et deux-points. C'est ce qui écarte "From MAILER-DAEMON ... 09:00".
func validFieldName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c < 33 || c > 126 {
			return false
		}
	}
	return true
}

// ensureUTF8 rattrape les en-têtes en 8 bits bruts (hors norme mais
// fréquents, typiquement du latin-1 non encodé) : le reste du paquet et
// l'appelant peuvent alors toujours supposer de l'UTF-8 valide.
func ensureUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return decodeSingleByte([]byte(s), &windows1252)
}

// wordDecoder décode les mots encodés RFC 2047. Il délègue les charsets
// que mime.WordDecoder ne connaît pas (tout sauf utf-8, iso-8859-1 et
// us-ascii) à nos propres tables, sans jamais échouer : un charset inconnu
// suit la même règle que pour les corps.
var wordDecoder = &mime.WordDecoder{
	CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		b, err := io.ReadAll(input)
		if err != nil {
			return nil, err
		}
		return strings.NewReader(decodeCharset(charset, b)), nil
	},
}

// decodeHeaderText décode un en-tête texte (Subject, nom de fichier). Un
// mot encodé invalide est laissé tel quel par mime.WordDecoder, ce qui
// reste lisible ; en cas d'erreur globale on garde la valeur brute.
func decodeHeaderText(s string) string {
	if !strings.Contains(s, "=?") {
		return s
	}
	d, err := wordDecoder.DecodeHeader(s)
	if err != nil {
		return s
	}
	return d
}

// parseAddressList lit From/To/Cc. net/mail rejette toute la liste pour
// une seule adresse malformée ; on retombe alors sur une lecture adresse
// par adresse, puis sur une extraction minimale de ce qui ressemble à une
// adresse, pour ne pas perdre les destinataires valides.
func parseAddressList(v string) []Address {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	p := mail.AddressParser{WordDecoder: wordDecoder}
	if list, err := p.ParseList(v); err == nil {
		out := make([]Address, 0, len(list))
		for _, a := range list {
			out = append(out, Address{Name: a.Name, Email: a.Address})
		}
		return out
	}
	var out []Address
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if a, err := p.Parse(part); err == nil {
			out = append(out, Address{Name: a.Name, Email: a.Address})
			continue
		}
		if a, ok := looseAddress(part); ok {
			out = append(out, a)
		}
	}
	return out
}

// looseAddress extrait "Nom <adresse" ou "adresse" d'une adresse que
// net/mail refuse (chevron fermant manquant, caractères interdits...).
func looseAddress(s string) (Address, bool) {
	if !strings.Contains(s, "@") {
		return Address{}, false
	}
	lt := strings.IndexByte(s, '<')
	if lt < 0 {
		return Address{Email: strings.Trim(s, " \t\"<>")}, true
	}
	email := s[lt+1:]
	if gt := strings.IndexByte(email, '>'); gt >= 0 {
		email = email[:gt]
	}
	email = strings.TrimSpace(email)
	if !strings.Contains(email, "@") {
		return Address{}, false
	}
	name := strings.Trim(strings.TrimSpace(s[:lt]), `"`)
	return Address{Name: decodeHeaderText(name), Email: email}, true
}

// parseMediaType lit Content-Type / Content-Disposition. mime.ParseMediaType
// gère RFC 2231 (filename*=, continuations) mais échoue sur des paramètres
// hors norme courants (nom de fichier non quoté avec espaces, paramètre
// dupliqué) : on garde alors le type et on relit les paramètres à la main.
// Le type retourné est en minuscules, "" si absent.
func parseMediaType(v string) (string, map[string]string) {
	if strings.TrimSpace(v) == "" {
		return "", nil
	}
	mt, params, err := mime.ParseMediaType(v)
	if err == nil {
		return mt, params
	}
	if mt == "" {
		mt, _, _ = strings.Cut(v, ";")
		mt = strings.ToLower(strings.TrimSpace(mt))
	}
	return mt, looseParams(v)
}

// looseParams découpe naïvement "type; clé=valeur; clé="valeur"" : pas de
// RFC 2231, mais suffisant pour récupérer boundary, charset ou name d'un
// en-tête que la lecture stricte a refusé.
func looseParams(v string) map[string]string {
	params := map[string]string{}
	fields := strings.Split(v, ";")
	for _, f := range fields[1:] {
		k, val, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		val = strings.TrimSpace(val)
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		if k != "" {
			if _, seen := params[k]; !seen {
				params[k] = val
			}
		}
	}
	return params
}
