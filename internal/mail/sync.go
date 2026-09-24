package mail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	netmail "net/mail"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JulianAndrieux/Jarvis/internal/email"
	"github.com/JulianAndrieux/Jarvis/internal/imap"
)

// Session : une connexion à la boîte (*imap.Client en production).
type Session interface {
	Examine(mailbox string) (imap.Mailbox, error)
	SearchSince(since time.Time) ([]uint32, error)
	SearchAfter(uid uint32) ([]uint32, error)
	Fetch(uids []uint32) ([]imap.Message, error)
	Logout() error
}

// ConnectIMAP : connexion TLS et authentification (production).
func ConnectIMAP(ctx context.Context, cfg Config) (Session, error) {
	c, err := imap.Dial(ctx, cfg.Host, true)
	if err != nil {
		return nil, err
	}
	if err := c.Login(cfg.User, cfg.Password); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Limites de ce qui est copié dans Atlas.
const (
	// MaxStoredAttachment : au-delà, seule la fiche de la pièce jointe est
	// gardée (un document MongoDB fait 16 Mo au plus).
	MaxStoredAttachment = 15 << 20
	// maxText : le corps en texte, au plus (un email reste lisible ; la
	// limite protège le document MongoDB).
	maxText = 200_000
)

// Syncer relève les nouveaux emails de la boîte de réception.
type Syncer struct {
	Store   Store
	Connect func(ctx context.Context, cfg Config) (Session, error)
	// Days : la première relève (ou après un changement d'UIDVALIDITY)
	// remonte ce nombre de jours (0 : 30).
	Days int
	// Batch : messages relevés par requête (0 : 10).
	Batch int
	Now   func() time.Time
}

// Report : le résultat d'une relève.
type Report struct {
	Fetched int // messages relevés
	New     int // dont nouveaux
}

// Sync relève les messages arrivés depuis la dernière relève.
func (s *Syncer) Sync(ctx context.Context, cfg Config) (Report, error) {
	var rep Report
	if err := cfg.Validate(); err != nil {
		return rep, fmt.Errorf("mail: configuration incomplète : %w", err)
	}
	sess, err := s.Connect(ctx, cfg)
	if err != nil {
		return rep, err
	}
	defer sess.Logout()
	box, err := sess.Examine(Mailbox)
	if err != nil {
		return rep, err
	}
	last, err := s.Store.LastUID(ctx, cfg.User, Mailbox, box.UIDValidity)
	if err != nil {
		return rep, err
	}
	var uids []uint32
	if last == 0 {
		days := s.Days
		if days <= 0 {
			days = 30
		}
		uids, err = sess.SearchSince(s.now().AddDate(0, 0, -days))
	} else {
		uids, err = sess.SearchAfter(last)
	}
	if err != nil {
		return rep, err
	}
	// Du plus ancien au plus récent : une relève interrompue reprend après
	// le dernier enregistré, sans trou.
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	batch := s.Batch
	if batch <= 0 {
		batch = 10
	}
	for start := 0; start < len(uids); start += batch {
		end := min(start+batch, len(uids))
		msgs, err := sess.Fetch(uids[start:end])
		if err != nil {
			return rep, err
		}
		for _, im := range msgs {
			m, files := fromIMAP(cfg.User, box.UIDValidity, im, s.now())
			created, err := s.Store.Save(ctx, m, files)
			if err != nil {
				return rep, err
			}
			rep.Fetched++
			if created {
				rep.New++
			}
		}
	}
	return rep, nil
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// fromIMAP lit un message relevé. Un message illisible est gardé quand
// même (objet « (email illisible) », raison dans le texte) : jamais perdu
// en silence.
func fromIMAP(account string, validity uint32, im imap.Message, fetched time.Time) (Mail, map[int][]byte) {
	msgID := messageID(im.Raw)
	m := Mail{
		ID: mailID(account, msgID, validity, im.UID), Account: account, Mailbox: Mailbox,
		UIDValidity: validity, UID: im.UID, MessageID: msgID,
		Date: im.InternalDate, Seen: im.Seen(), FetchedAt: fetched,
	}
	parsed, err := email.Parse(im.Raw)
	if err != nil {
		m.Subject = "(email illisible)"
		m.Text = "Le message n'a pas pu être lu : " + err.Error()
		return m, nil
	}
	m.Subject = parsed.Subject
	if len(parsed.From) > 0 {
		m.From = Address(parsed.From[0])
	}
	for _, a := range parsed.To {
		m.To = append(m.To, Address(a))
	}
	for _, a := range parsed.Cc {
		m.Cc = append(m.Cc, Address(a))
	}
	if !parsed.Date.IsZero() {
		m.Date = parsed.Date
	}
	body := strings.TrimSpace(parsed.Text)
	if body == "" {
		body = strings.TrimSpace(email.HTMLToText(parsed.HTML))
	}
	m.Text = truncate(body, maxText)
	files := map[int][]byte{}
	for i, a := range parsed.Attachments {
		att := Attachment{Index: i, Filename: a.Filename, ContentType: a.ContentType, Size: a.Size}
		if len(a.Data) <= MaxStoredAttachment {
			att.Stored = true
			files[i] = a.Data
		}
		m.Attachments = append(m.Attachments, att)
	}
	return m, files
}

// messageID : l'en-tête Message-ID ("" s'il manque).
func messageID(raw []byte) string {
	msg, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(msg.Header.Get("Message-Id"))
}

// mailID : identifiant stable d'un email pour un compte — son Message-ID
// (le même message relevé deux fois, même après un changement
// d'UIDVALIDITY, n'est pas dupliqué), sinon sa position dans la boîte.
func mailID(account, msgID string, validity, uid uint32) string {
	key := msgID
	if key == "" {
		key = fmt.Sprintf("uid:%d:%d", validity, uid)
	}
	sum := sha256.Sum256([]byte(account + "\x00" + key))
	return hex.EncodeToString(sum[:16])
}

// truncate coupe s à n octets au plus, sans couper un caractère.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}
