package imap

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// step : une commande attendue (sans son étiquette) et les réponses du
// serveur ; "$T" y est remplacé par l'étiquette reçue.
type step struct {
	expect string
	reply  []string
}

// fakeServer joue un dialogue IMAP écrit à l'avance et signale toute
// commande inattendue.
func fakeServer(t *testing.T, greeting string, steps []step) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	errs := make(chan string, 1)
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		fmt.Fprint(server, greeting+"\r\n")
		for _, s := range steps {
			line, err := r.ReadString('\n')
			if err != nil {
				errs <- fmt.Sprintf("lecture : %v (attendu %q)", err, s.expect)
				return
			}
			line = strings.TrimRight(line, "\r\n")
			tag, cmd, _ := strings.Cut(line, " ")
			if cmd != s.expect {
				errs <- fmt.Sprintf("commande = %q, attendu %q", cmd, s.expect)
				return
			}
			for _, rep := range s.reply {
				fmt.Fprint(server, strings.ReplaceAll(rep, "$T", tag))
			}
		}
		errs <- ""
	}()
	t.Cleanup(func() {
		select {
		case e := <-errs:
			if e != "" {
				t.Error("serveur : " + e)
			}
		case <-time.After(2 * time.Second):
		}
	})
	return client
}

func TestClient_LoginSelectSearchFetch(t *testing.T) {
	raw := "From: Alice <alice@example.com>\r\nSubject: Bonjour\r\n\r\nCorps\r\n"
	conn := fakeServer(t, "* OK Gimap ready", []step{
		{`LOGIN "moi@gmail.com" "abcd \"efgh\" \\ijkl"`, []string{"* CAPABILITY IMAP4rev1\r\n", "$T OK moi@gmail.com authenticated (Success)\r\n"}},
		{`EXAMINE "INBOX"`, []string{
			"* FLAGS (\\Answered \\Flagged \\Seen)\r\n",
			"* OK [UIDVALIDITY 594] UIDs valid.\r\n",
			"* 12 EXISTS\r\n",
			"* OK [UIDNEXT 4021] Predicted next UID.\r\n",
			"$T OK [READ-ONLY] INBOX selected. (Success)\r\n",
		}},
		{`UID SEARCH SINCE 20-Aug-2026`, []string{"* SEARCH 4001 4007 4012\r\n", "$T OK SEARCH completed\r\n"}},
		{`UID FETCH 4001,4007 (UID FLAGS INTERNALDATE RFC822.SIZE BODY.PEEK[])`, []string{
			fmt.Sprintf("* 1 FETCH (UID 4001 FLAGS (\\Seen) INTERNALDATE \"23-Sep-2026 09:15:02 +0200\" RFC822.SIZE %d BODY[] {%d}\r\n%s)\r\n", len(raw), len(raw), raw),
			// Ordre différent, sans drapeau, corps contenant une parenthèse fermante.
			"* 2 FETCH (RFC822.SIZE 8 BODY[] {8}\r\nab)\r\n(cd UID 4007 FLAGS () INTERNALDATE \"24-Sep-2026 18:00:00 +0000\")\r\n",
			"$T OK Success\r\n",
		}},
		{`LOGOUT`, []string{"* BYE LOGOUT Requested\r\n", "$T OK 73 good day (Success)\r\n"}},
	})
	c, err := NewClient(conn)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login("moi@gmail.com", `abcd "efgh" \ijkl`); err != nil {
		t.Fatalf("Login : %v", err)
	}
	box, err := c.Examine("INBOX")
	if err != nil || box.UIDValidity != 594 || box.Exists != 12 {
		t.Fatalf("Examine = %+v, %v", box, err)
	}
	uids, err := c.SearchSince(time.Date(2026, 8, 20, 23, 0, 0, 0, time.UTC))
	if err != nil || fmt.Sprint(uids) != "[4001 4007 4012]" {
		t.Fatalf("SearchSince = %v, %v", uids, err)
	}
	msgs, err := c.Fetch([]uint32{4001, 4007})
	if err != nil || len(msgs) != 2 {
		t.Fatalf("Fetch = %+v, %v", msgs, err)
	}
	m := msgs[0]
	if m.UID != 4001 || string(m.Raw) != raw || m.Size != len(raw) || !m.Seen() || m.InternalDate.UTC().Format(time.RFC3339) != "2026-09-23T07:15:02Z" {
		t.Errorf("msgs[0] = %+v", m)
	}
	if m := msgs[1]; m.UID != 4007 || string(m.Raw) != "ab)\r\n(cd" || m.Seen() {
		t.Errorf("msgs[1] = %+v (raw %q)", m, m.Raw)
	}
	if err := c.Logout(); err != nil {
		t.Errorf("Logout : %v", err)
	}
}

func TestClient_LoginRefused(t *testing.T) {
	conn := fakeServer(t, "* OK ready", []step{
		{`LOGIN "moi@gmail.com" "faux"`, []string{"$T NO [AUTHENTICATIONFAILED] Invalid credentials (Failure)\r\n"}},
	})
	c, err := NewClient(conn)
	if err != nil {
		t.Fatal(err)
	}
	err = c.Login("moi@gmail.com", "faux")
	if err == nil || !strings.Contains(err.Error(), "Invalid credentials") {
		t.Errorf("Login = %v, want the server's refusal", err)
	}
}

// Un mot de passe contenant un retour à la ligne injecterait une commande :
// refusé avant tout envoi.
func TestClient_LoginRejectsLineBreaks(t *testing.T) {
	c, err := NewClient(fakeServer(t, "* OK ready", nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login("moi", "a\r\nA2 DELETE INBOX"); err == nil {
		t.Error("Login accepted a password with a line break")
	}
}

func TestClient_SearchAfterIgnoresLowerUIDs(t *testing.T) {
	// « 4012:* » renvoie toujours le dernier message, même s'il est plus
	// ancien que 4012 (RFC 3501) : il ne doit pas revenir.
	conn := fakeServer(t, "* OK ready", []step{
		{`UID SEARCH UID 4012:*`, []string{"* SEARCH 4011\r\n", "$T OK done\r\n"}},
		{`UID SEARCH UID 4012:*`, []string{"* SEARCH 4012 4015\r\n", "$T OK done\r\n"}},
	})
	c, _ := NewClient(conn)
	if got, err := c.SearchAfter(4011); err != nil || len(got) != 0 {
		t.Errorf("SearchAfter = %v, %v, want none", got, err)
	}
	if got, err := c.SearchAfter(4011); err != nil || fmt.Sprint(got) != "[4012 4015]" {
		t.Errorf("SearchAfter = %v, %v", got, err)
	}
}

func TestClient_GreetingMustBeOK(t *testing.T) {
	if _, err := NewClient(fakeServer(t, "* BYE trop de connexions", nil)); err == nil {
		t.Error("NewClient accepted a BYE greeting")
	}
}

func TestDial_TimesOutOnSilentServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			defer c.Close()
			time.Sleep(2 * time.Second)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Dial(ctx, ln.Addr().String(), true); err == nil {
		t.Error("Dial succeeded against a silent server")
	}
	if time.Since(start) > time.Second {
		t.Errorf("Dial took %s, want the context deadline", time.Since(start))
	}
}
