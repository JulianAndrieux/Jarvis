package imap

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// response : une réponse du serveur. Réponse d'état (OK, NO, BAD, BYE,
// PREAUTH) : status et text. Sinon : fields, les éléments de la réponse
// après l'étiquette — atome ou chaîne (string), littéral ([]byte), liste
// ([]any).
type response struct {
	tag    string
	status string
	text   string
	fields []any
}

// maxLiteral borne un littéral annoncé (un message Gmail fait 25 Mo au
// plus) : un serveur défaillant n'épuise pas la mémoire.
const maxLiteral = 64 << 20

func (c *Client) readResponse() (response, error) {
	var r response
	tag, err := c.atom()
	if err != nil {
		return r, err
	}
	r.tag = tag
	if tag == "+" { // demande de suite : non utilisée (pas de littéral envoyé)
		r.text, err = c.restOfLine()
		return r, err
	}
	if err := c.skipSpace(); err != nil {
		return r, err
	}
	// Réponse d'état : « * OK [UIDVALIDITY 594] ... », « A1 NO ... ».
	if first, err := c.r.Peek(1); err == nil && first[0] != '(' {
		word, err := c.peekWord()
		if err != nil {
			return r, err
		}
		switch strings.ToUpper(word) {
		case "OK", "NO", "BAD", "BYE", "PREAUTH":
			c.r.Discard(len(word))
			r.status = strings.ToUpper(word)
			text, err := c.restOfLine()
			r.text = strings.TrimSpace(text)
			return r, err
		}
	}
	for {
		b, err := c.r.ReadByte()
		if err != nil {
			return r, err
		}
		switch b {
		case ' ':
			continue
		case '\r':
			if nb, err := c.r.ReadByte(); err != nil || nb != '\n' {
				return r, errors.New("imap: fin de ligne invalide")
			}
			return r, nil
		case '\n':
			return r, nil
		}
		c.r.UnreadByte()
		v, err := c.value()
		if err != nil {
			return r, err
		}
		r.fields = append(r.fields, v)
	}
}

// value lit un élément : liste, chaîne entre guillemets, littéral ou atome.
func (c *Client) value() (any, error) {
	b, err := c.r.ReadByte()
	if err != nil {
		return nil, err
	}
	switch b {
	case '(':
		var list []any
		for {
			nb, err := c.r.ReadByte()
			if err != nil {
				return nil, err
			}
			if nb == ')' {
				return list, nil
			}
			if nb == ' ' {
				continue
			}
			c.r.UnreadByte()
			v, err := c.value()
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
	case '"':
		var sb strings.Builder
		for {
			nb, err := c.r.ReadByte()
			if err != nil {
				return nil, err
			}
			switch nb {
			case '"':
				return sb.String(), nil
			case '\\':
				if nb, err = c.r.ReadByte(); err != nil {
					return nil, err
				}
			case '\r', '\n':
				return nil, errors.New("imap: chaîne non terminée")
			}
			sb.WriteByte(nb)
		}
	case '{':
		size, err := c.r.ReadString('}')
		if err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(strings.TrimSuffix(size, "}"))
		if err != nil || n < 0 || n > maxLiteral {
			return nil, fmt.Errorf("imap: littéral invalide {%s", size)
		}
		if crlf, err := c.r.ReadString('\n'); err != nil || strings.TrimRight(crlf, "\r\n") != "" {
			return nil, errors.New("imap: littéral mal annoncé")
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(c.r, data); err != nil {
			return nil, err
		}
		return data, nil
	}
	c.r.UnreadByte()
	return c.atom()
}

// atom lit un atome ; les crochets en font partie (« BODY[] »,
// « BODY[HEADER.FIELDS (FROM)] »), parenthèses comprises à l'intérieur.
func (c *Client) atom() (string, error) {
	var sb strings.Builder
	depth := 0
	for {
		b, err := c.r.ReadByte()
		if err != nil {
			return "", err
		}
		if depth == 0 && (b == ' ' || b == '(' || b == ')' || b == '\r' || b == '\n') {
			c.r.UnreadByte()
			if sb.Len() == 0 {
				return "", fmt.Errorf("imap: atome attendu, reçu %q", b)
			}
			return sb.String(), nil
		}
		switch b {
		case '[':
			depth++
		case ']':
			depth--
		}
		sb.WriteByte(b)
	}
}

func (c *Client) peekWord() (string, error) {
	for n := 1; n <= 8; n++ {
		p, err := c.r.Peek(n)
		if err != nil {
			return string(p), nil
		}
		last := p[n-1]
		if last == ' ' || last == '\r' || last == '\n' {
			return string(p[:n-1]), nil
		}
	}
	return "", nil
}

func (c *Client) skipSpace() error {
	for {
		b, err := c.r.ReadByte()
		if err != nil {
			return err
		}
		if b != ' ' {
			return c.r.UnreadByte()
		}
	}
}

func (c *Client) restOfLine() (string, error) {
	line, err := c.r.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}
