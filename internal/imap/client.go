// Package imap est un client IMAP4rev1 minimal (RFC 3501), écrit sur la
// bibliothèque standard (jalon 39) : juste ce qu'il faut pour relever une
// boîte de réception en lecture seule — connexion TLS, LOGIN (mot de
// passe d'application), EXAMINE, UID SEARCH, UID FETCH du message brut.
// Le message brut est ensuite lu par internal/email.
package imap

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Client : une session IMAP. Pas sûr pour un usage concurrent.
type Client struct {
	conn net.Conn
	r    *bufio.Reader
	tag  int
}

// Mailbox : l'état d'une boîte ouverte.
type Mailbox struct {
	// UIDValidity : si elle change, les UID déjà vus ne désignent plus
	// les mêmes messages (RFC 3501, 2.3.1.1).
	UIDValidity uint32
	Exists      int
}

// Message : un message relevé, brut (RFC 5322).
type Message struct {
	UID          uint32
	Flags        []string
	InternalDate time.Time
	Size         int
	Raw          []byte
}

// Seen : le message a déjà été lu dans la boîte.
func (m Message) Seen() bool {
	for _, f := range m.Flags {
		if strings.EqualFold(f, `\Seen`) {
			return true
		}
	}
	return false
}

// Dial ouvre une session sur addr (« imap.gmail.com:993 »), en TLS si
// useTLS. L'échéance de ctx vaut pour toute la session : une connexion
// muette ne bloque jamais la relève.
func Dial(ctx context.Context, addr string, useTLS bool) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("imap: connexion à %s : %w", addr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	if useTLS {
		host, _, _ := net.SplitHostPort(addr)
		tc := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			stop()
			conn.Close()
			return nil, fmt.Errorf("imap: TLS avec %s : %w", addr, err)
		}
		conn = tc
	}
	c, err := NewClient(conn)
	if err != nil {
		stop()
		conn.Close()
		return nil, err
	}
	return c, nil
}

// NewClient démarre une session sur une connexion déjà ouverte : lit
// l'accueil du serveur.
func NewClient(conn net.Conn) (*Client, error) {
	c := &Client{conn: conn, r: bufio.NewReader(conn)}
	greet, err := c.readResponse()
	if err != nil {
		return nil, fmt.Errorf("imap: accueil : %w", err)
	}
	if greet.tag != "*" || (greet.status != "OK" && greet.status != "PREAUTH") {
		return nil, fmt.Errorf("imap: accueil refusé : %s %s", greet.status, greet.text)
	}
	return c, nil
}

// Close ferme la connexion sans LOGOUT.
func (c *Client) Close() error { return c.conn.Close() }

// Login s'authentifie (mot de passe d'application pour Gmail).
func (c *Client) Login(user, password string) error {
	u, err := quote(user)
	if err != nil {
		return err
	}
	p, err := quote(password)
	if err != nil {
		return err
	}
	if _, err := c.command("LOGIN " + u + " " + p); err != nil {
		return fmt.Errorf("imap: connexion refusée : %w", err)
	}
	return nil
}

// Examine ouvre une boîte en lecture seule : la relève ne marque jamais
// un message comme lu.
func (c *Client) Examine(mailbox string) (Mailbox, error) {
	q, err := quote(mailbox)
	if err != nil {
		return Mailbox{}, err
	}
	untagged, err := c.command("EXAMINE " + q)
	if err != nil {
		return Mailbox{}, fmt.Errorf("imap: ouverture de %s : %w", mailbox, err)
	}
	var box Mailbox
	for _, r := range untagged {
		if r.status == "OK" && strings.HasPrefix(r.text, "[UIDVALIDITY ") {
			v, _, _ := strings.Cut(strings.TrimPrefix(r.text, "[UIDVALIDITY "), "]")
			n, _ := strconv.ParseUint(v, 10, 32)
			box.UIDValidity = uint32(n)
		}
		if len(r.fields) == 2 && r.fields[1] == "EXISTS" {
			box.Exists, _ = strconv.Atoi(str(r.fields[0]))
		}
	}
	return box, nil
}

// SearchSince : les UID des messages reçus depuis le jour de since (date
// seule, au sens du serveur).
func (c *Client) SearchSince(since time.Time) ([]uint32, error) {
	return c.search("UID SEARCH SINCE " + since.Format("2-Jan-2006"))
}

// SearchAfter : les UID strictement supérieurs à uid.
func (c *Client) SearchAfter(uid uint32) ([]uint32, error) {
	all, err := c.search(fmt.Sprintf("UID SEARCH UID %d:*", uid+1))
	if err != nil {
		return nil, err
	}
	// « n:* » inclut toujours le dernier message, même d'UID inférieur.
	var out []uint32
	for _, u := range all {
		if u > uid {
			out = append(out, u)
		}
	}
	return out, nil
}

func (c *Client) search(cmd string) ([]uint32, error) {
	untagged, err := c.command(cmd)
	if err != nil {
		return nil, fmt.Errorf("imap: recherche : %w", err)
	}
	var out []uint32
	for _, r := range untagged {
		if len(r.fields) == 0 || r.fields[0] != "SEARCH" {
			continue
		}
		for _, f := range r.fields[1:] {
			if n, err := strconv.ParseUint(str(f), 10, 32); err == nil {
				out = append(out, uint32(n))
			}
		}
	}
	return out, nil
}

// Fetch relève les messages bruts de ces UID, sans les marquer comme lus
// (BODY.PEEK).
func (c *Client) Fetch(uids []uint32) ([]Message, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	set := make([]string, len(uids))
	for i, u := range uids {
		set[i] = strconv.FormatUint(uint64(u), 10)
	}
	untagged, err := c.command("UID FETCH " + strings.Join(set, ",") + " (UID FLAGS INTERNALDATE RFC822.SIZE BODY.PEEK[])")
	if err != nil {
		return nil, fmt.Errorf("imap: relève : %w", err)
	}
	var out []Message
	for _, r := range untagged {
		if len(r.fields) != 3 || r.fields[1] != "FETCH" {
			continue
		}
		items, _ := r.fields[2].([]any)
		var m Message
		for i := 0; i+1 < len(items); i += 2 {
			key, val := strings.ToUpper(str(items[i])), items[i+1]
			switch key {
			case "UID":
				n, _ := strconv.ParseUint(str(val), 10, 32)
				m.UID = uint32(n)
			case "FLAGS":
				list, _ := val.([]any)
				for _, f := range list {
					m.Flags = append(m.Flags, str(f))
				}
			case "INTERNALDATE":
				m.InternalDate, _ = time.Parse("_2-Jan-2006 15:04:05 -0700", str(val))
			case "RFC822.SIZE":
				m.Size, _ = strconv.Atoi(str(val))
			case "BODY[]":
				switch v := val.(type) {
				case []byte:
					m.Raw = v
				case string:
					m.Raw = []byte(v)
				}
			}
		}
		if m.UID != 0 {
			out = append(out, m)
		}
	}
	return out, nil
}

// Logout termine la session et ferme la connexion.
func (c *Client) Logout() error {
	_, err := c.command("LOGOUT")
	c.conn.Close()
	return err
}

// command envoie une commande étiquetée et lit les réponses jusqu'à son
// achèvement : les réponses non étiquetées, ou l'erreur du serveur.
func (c *Client) command(cmd string) ([]response, error) {
	c.tag++
	tag := "A" + strconv.Itoa(c.tag)
	if _, err := fmt.Fprintf(c.conn, "%s %s\r\n", tag, cmd); err != nil {
		return nil, err
	}
	var untagged []response
	for {
		r, err := c.readResponse()
		if err != nil {
			return nil, err
		}
		if r.tag != tag {
			untagged = append(untagged, r)
			continue
		}
		if r.status != "OK" {
			return nil, fmt.Errorf("%s %s", r.status, r.text)
		}
		return untagged, nil
	}
}

// quote : une chaîne IMAP entre guillemets. Un retour à la ligne
// terminerait la commande et en injecterait une autre : refusé.
func quote(s string) (string, error) {
	if strings.ContainsAny(s, "\r\n\x00") {
		return "", errors.New("imap: retour à la ligne interdit dans un argument")
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`, nil
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	}
	return ""
}
