package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/changes"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

func TestActivityPage(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})
	j := changes.NewFakeJournal()
	s.Journal = j
	at := time.Date(2026, 9, 27, 14, 30, 0, 0, time.UTC)
	if err := j.Append(context.Background(),
		changes.Op{ID: "1", Env: tenancy.Local, User: tenancy.LocalUser, Session: tenancy.LocalSession, At: at,
			Kind: changes.KindNote, Target: "n1", Label: "Courses", Field: "title",
			Action: changes.Set, Before: `"Sans titre"`, After: `"Courses"`},
		changes.Op{ID: "2", Env: tenancy.Local, User: "u-autre", Session: "s2", At: at.Add(time.Minute),
			Kind: changes.KindDocument, Target: "d1", Label: "facture.pdf", Action: changes.Delete},
	); err != nil {
		t.Fatal(err)
	}

	// Tout l'environnement : les deux lignes.
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1:8090/activity?tout=1", nil))
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	for _, want := range []string{"Courses", "facture.pdf", "supprimé", "title"} {
		if !strings.Contains(body, want) {
			t.Errorf("la page doit contenir %q", want)
		}
	}
	// Une entité supprimée n'a plus de page : pas de lien mort.
	if strings.Contains(body, `href="/documents/d1"`) {
		t.Error("un document supprimé ne doit pas être lié")
	}
	if !strings.Contains(body, `href="/notes/n1"`) {
		t.Error("une note modifiée doit être liée")
	}

	// « Mes modifications » : en mode mono-utilisateur, je suis l'utilisateur
	// local — je vois les miennes, pas celles inscrites au nom d'un autre.
	rec = httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1:8090/activity", nil))
	body = rec.Body.String()
	if !strings.Contains(body, "Courses") {
		t.Error("mes modifications doivent apparaître")
	}
	if strings.Contains(body, "facture.pdf") {
		t.Error("celles d'un autre auteur ne doivent pas apparaître dans « mes modifications »")
	}
}

// Avec une session, « mes modifications » ne montre que les miennes.
func TestActivityPage_MesModifications(t *testing.T) {
	s, mgr, _ := newAuthServer(t)
	j := changes.NewFakeJournal()
	s.Journal = j
	token := signIn(t, mgr, "julian@example.test")
	scope, ok, err := mgr.Resolve(context.Background(), token)
	if err != nil || !ok {
		t.Fatalf("Resolve = (%v, %v)", ok, err)
	}
	at := time.Date(2026, 9, 27, 14, 30, 0, 0, time.UTC)
	if err := j.Append(context.Background(),
		changes.Op{ID: "1", Env: scope.Env, User: scope.User, At: at,
			Kind: changes.KindNote, Target: "n1", Label: "Ma note", Action: changes.Create},
		changes.Op{ID: "2", Env: scope.Env, User: "quelqu-un-d-autre", At: at,
			Kind: changes.KindNote, Target: "n2", Label: "Sa note", Action: changes.Create},
	); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, withSession(httptest.NewRequest("GET", "http://127.0.0.1:8090/activity", nil), token))
	body := rec.Body.String()
	if !strings.Contains(body, "Ma note") {
		t.Error("mes modifications doivent apparaître")
	}
	if strings.Contains(body, "Sa note") {
		t.Error("celles des autres ne doivent pas apparaître dans « mes modifications »")
	}
	// Et la vue de tout l'environnement les montre.
	rec = httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, withSession(httptest.NewRequest("GET", "http://127.0.0.1:8090/activity?tout=1", nil), token))
	if !strings.Contains(rec.Body.String(), "Sa note") {
		t.Error("la vue de tout l'environnement doit montrer les modifications des autres")
	}
}

// Sans journal configuré, l'onglet n'existe pas plutôt que d'afficher une
// page vide trompeuse.
func TestActivityPage_SansJournal(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1:8090/activity", nil))
	if rec.Code != 404 {
		t.Errorf("code = %d, veut 404", rec.Code)
	}
}

func TestDescribeOp(t *testing.T) {
	if got := describeOp(changes.Op{Action: changes.Create}); got != "créé" {
		t.Errorf("création = %q", got)
	}
	got := describeOp(changes.Op{Action: changes.Set, Field: "tags", Before: `[]`, After: `["urgent"]`})
	if !strings.Contains(got, "tags") || !strings.Contains(got, "urgent") {
		t.Errorf("modification = %q", got)
	}
	// Une valeur longue est bornée : une note entière n'a rien à faire dans
	// une ligne de tableau.
	long := describeOp(changes.Op{Action: changes.Set, Field: "body", After: `"` + strings.Repeat("x", 200) + `"`})
	if len(long) > 160 {
		t.Errorf("description de %d caractères, trop longue", len(long))
	}
	if got := shortJSON(`""`); got != "(vide)" {
		t.Errorf("valeur vide = %q", got)
	}
}
