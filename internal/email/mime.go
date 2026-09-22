package email

import (
	"bytes"
	"encoding/base64"
	"strings"
)

// splitMultipart découpe un corps multipart selon sa boundary (RFC 2046
// §5.1.1) : préambule et épilogue ignorés, saut de ligne précédant chaque
// délimiteur rattaché au délimiteur (donc retiré de la partie). Écrit à la
// main plutôt qu'avec mime/multipart pour tolérer un délimiteur final
// manquant (message tronqué : la dernière partie va jusqu'à la fin) et
// des fins de ligne LF seules, sans jamais abandonner les parties déjà lues.
func splitMultipart(body []byte, boundary string) [][]byte {
	delim := []byte("--" + boundary)
	var parts [][]byte
	inPart := false
	start, pos := 0, 0
	for {
		end := bytes.IndexByte(body[pos:], '\n')
		line, next := body[pos:], len(body)
		if end >= 0 {
			line, next = body[pos:pos+end], pos+end+1
		}
		// Les blancs après le délimiteur sont autorisés (transport padding).
		trimmed := bytes.TrimRight(line, " \t\r")
		if rest, ok := bytes.CutPrefix(trimmed, delim); ok && (len(rest) == 0 || string(rest) == "--") {
			if inPart {
				parts = append(parts, trimFinalNewline(body[start:pos]))
			}
			if len(rest) > 0 { // délimiteur final
				return parts
			}
			inPart, start = true, next
		}
		if end < 0 {
			break
		}
		pos = next
	}
	if inPart && start <= len(body) {
		parts = append(parts, body[start:])
	}
	return parts
}

// trimFinalNewline retire le saut de ligne qui appartient au délimiteur
// suivant (RFC 2046 : "CRLF--boundary").
func trimFinalNewline(b []byte) []byte {
	b = bytes.TrimSuffix(b, []byte("\n"))
	return bytes.TrimSuffix(b, []byte("\r"))
}

// guessBoundary retrouve le délimiteur d'un multipart dont l'en-tête a
// perdu son paramètre boundary : la première ligne "--xxx" du corps.
func guessBoundary(body []byte) string {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimRight(line, " \t\r")
		if strings.HasPrefix(line, "--") {
			if b := strings.TrimSuffix(line[2:], "--"); strings.TrimSpace(b) != "" {
				return b
			}
		}
	}
	return ""
}

// decodeTransfer applique le Content-Transfer-Encoding. 7bit, 8bit, binary
// et les valeurs inconnues laissent les octets tels quels : c'est ce que
// recommande RFC 2045 pour un encodage non reconnu qu'on choisit de lire.
func decodeTransfer(encoding string, body []byte) []byte {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		return decodeBase64(body)
	case "quoted-printable":
		return decodeQuotedPrintable(body)
	default:
		return body
	}
}

// decodeBase64 est volontairement tolérant : RFC 2045 §6.8 impose
// d'ignorer tout caractère hors alphabet (sauts de ligne, espaces, déchets),
// le padding manquant est accepté, et des blocs concaténés après un "="
// intermédiaire sont décodés séparément. Des données vraiment invalides
// donnent donc des octets approximatifs plutôt qu'une erreur qui ferait
// perdre la partie — et jamais les autres parties.
func decodeBase64(body []byte) []byte {
	clean := make([]byte, 0, len(body))
	for _, c := range body {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=' {
			clean = append(clean, c)
		}
	}
	var out []byte
	for _, chunk := range bytes.FieldsFunc(clean, func(r rune) bool { return r == '=' }) {
		if len(chunk)%4 == 1 {
			chunk = chunk[:len(chunk)-1] // un caractère isolé ne code aucun octet
		}
		buf := make([]byte, base64.RawStdEncoding.DecodedLen(len(chunk)))
		n, _ := base64.RawStdEncoding.Decode(buf, chunk)
		out = append(out, buf[:n]...)
	}
	return out
}

// decodeQuotedPrintable décode RFC 2045 §6.7 sans jamais échouer
// (mime/quotedprintable renvoie une erreur sur "=ZZ", fréquent dans les
// messages produits par des outils approximatifs) : "=XX" hexadécimal est
// décodé (minuscules acceptées), "=" en fin de ligne est un saut doux, tout
// autre "=" est gardé littéralement. Les blancs de fin de ligne sont du
// bourrage de transport, retirés comme le demande la RFC.
func decodeQuotedPrintable(body []byte) []byte {
	out := make([]byte, 0, len(body))
	pos := 0
	for pos < len(body) {
		end := bytes.IndexByte(body[pos:], '\n')
		line, eol, next := body[pos:], "", len(body)
		if end >= 0 {
			line, next = body[pos:pos+end], pos+end+1
			eol = "\n"
			if bytes.HasSuffix(line, []byte("\r")) {
				line, eol = line[:len(line)-1], "\r\n"
			}
		}
		line = bytes.TrimRight(line, " \t")
		soft := false
		if bytes.HasSuffix(line, []byte("=")) {
			line, soft = line[:len(line)-1], true
		}
		for i := 0; i < len(line); i++ {
			if line[i] == '=' && i+2 < len(line) && isHex(line[i+1]) && isHex(line[i+2]) {
				out = append(out, unhex(line[i+1])<<4|unhex(line[i+2]))
				i += 2
				continue
			}
			out = append(out, line[i])
		}
		if !soft {
			out = append(out, eol...)
		}
		pos = next
	}
	return out
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}
