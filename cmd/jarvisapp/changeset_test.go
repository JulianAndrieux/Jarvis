package main

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/changes"
	"github.com/JulianAndrieux/Jarvis/internal/notes"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// newChangesetServer : l'application avec le changeset actif, plus un
// service « de l'autre » qui écrit directement dans la base.
func newChangesetServer(t *testing.T) (*Server, *notes.Service, *notes.FakeStore) {
	t.Helper()
	s, _ := newTestServer(t, &blockingRunner{})
	base := notes.NewFakeStore()
	staged := changes.NewFakeChangesetStore()
	clock := func() time.Time { return notesNow }
	s.Notes = &notes.Service{
		Store:  base,
		Now:    clock,
		Staged: staged,
		Stager: &changes.Stager{Store: staged, Now: clock},
		// La portée de l'installation mono-utilisateur, celle que le
		// middleware injecte quand l'authentification n'est pas configurée :
		// les assertions doivent interroger le même changeset que les
		// requêtes HTTP.
		DefaultScope: tenancy.LocalScope(),
	}
	autre := &notes.Service{Store: base, Now: clock,
		DefaultScope: tenancy.Scope{Env: tenancy.Local, User: "autre", Session: "s-autre", Role: tenancy.RoleMember}}
	return s, autre, base
}

// Le cycle complet depuis l'interface : je modifie, la page des
// changements le montre, je commite, la base l'a.
func TestChangesPage_CommitDepuisLInterface(t *testing.T) {
	s, autre, base := newChangesetServer(t)
	ctx := context.Background()
	n, err := autre.NewNote(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := autre.SaveNote(ctx, n.ID, "Courses", "du lait", "", false); err != nil {
		t.Fatal(err)
	}

	// Je modifie par l'interface (le formulaire porte la version affichée).
	mine, _, err := s.Notes.Store.For(tenancy.LocalScope()).GetNote(ctx, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec := do(s, http.MethodPost, "/notes/"+n.ID, url.Values{
		"version": {strconv.Itoa(mine.Version)}, "title": {"Courses du samedi"}, "body": {"du lait"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("enregistrement : %d %s", rec.Code, rec.Body.String())
	}

	// La base n'a pas bougé.
	inBase, _, _ := base.GetNote(ctx, n.ID)
	if inBase.Title != "Courses" {
		t.Errorf("base = %q : la modification doit rester en attente", inBase.Title)
	}
	// La page des changements la montre.
	page := do(s, http.MethodGet, "/changes", nil).Body.String()
	for _, want := range []string{"Courses du samedi", "title"} {
		if !strings.Contains(page, want) {
			t.Errorf("la page des changements doit contenir %q", want)
		}
	}
	// La barre latérale aussi.
	if side := do(s, http.MethodGet, "/sidebar", nil).Body.String(); !strings.Contains(side, "non commitée") {
		t.Error("la barre latérale doit signaler les modifications en attente")
	}

	// Je commite.
	rec = do(s, http.MethodPost, "/changes/commit", url.Values{})
	if rec.Code != http.StatusOK {
		t.Fatalf("commit : %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "appliquée") {
		t.Errorf("le résultat du commit doit être annoncé, reçu : %s", rec.Body.String())
	}
	inBase, _, _ = base.GetNote(ctx, n.ID)
	if inBase.Title != "Courses du samedi" {
		t.Errorf("après commit, base = %q", inBase.Title)
	}
}

// Un conflit s'affiche avec la valeur en base, et l'opération reste en
// attente.
func TestChangesPage_Conflit(t *testing.T) {
	s, autre, _ := newChangesetServer(t)
	ctx := context.Background()
	n, _ := autre.NewNote(ctx, "")
	if _, err := autre.SaveNote(ctx, n.ID, "Courses", "", "", false); err != nil {
		t.Fatal(err)
	}
	mine, _, _ := s.Notes.Store.For(tenancy.LocalScope()).GetNote(ctx, n.ID)
	do(s, http.MethodPost, "/notes/"+n.ID, url.Values{
		"version": {strconv.Itoa(mine.Version)}, "title": {"Mon titre"},
	})
	// L'autre écrit le même champ entre-temps.
	if _, err := autre.SaveNote(ctx, n.ID, "Son titre", "", "", false); err != nil {
		t.Fatal(err)
	}

	body := do(s, http.MethodPost, "/changes/commit", url.Values{}).Body.String()
	if !strings.Contains(body, "modifié entre-temps") {
		t.Errorf("le conflit doit être expliqué, reçu : %s", body)
	}
	if !strings.Contains(body, "Son titre") {
		t.Error("la valeur en base doit être montrée")
	}
	if ops, _ := s.Notes.Pending(tenancy.WithScope(ctx, tenancy.LocalScope())); len(ops) != 1 {
		t.Errorf("%d opérations en attente, veut 1 : un conflit reste à trancher", len(ops))
	}
}

// Abandonner une opération, puis tout abandonner.
func TestChangesPage_Abandon(t *testing.T) {
	s, autre, base := newChangesetServer(t)
	ctx := context.Background()
	n, _ := autre.NewNote(ctx, "")
	if _, err := autre.SaveNote(ctx, n.ID, "Courses", "", "", false); err != nil {
		t.Fatal(err)
	}
	mine, _, _ := s.Notes.Store.For(tenancy.LocalScope()).GetNote(ctx, n.ID)
	do(s, http.MethodPost, "/notes/"+n.ID, url.Values{
		"version": {strconv.Itoa(mine.Version)}, "title": {"Mon titre"},
	})
	scoped := tenancy.WithScope(ctx, tenancy.LocalScope())
	ops, _ := s.Notes.Pending(scoped)
	if len(ops) == 0 {
		t.Fatal("aucune opération en attente")
	}
	if rec := do(s, http.MethodPost, "/changes/"+ops[0].ID+"/discard", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("abandon : %d", rec.Code)
	}
	if left, _ := s.Notes.Pending(scoped); len(left) != len(ops)-1 {
		t.Errorf("%d opérations après abandon, veut %d", len(left), len(ops)-1)
	}
	if got, _, _ := base.GetNote(ctx, n.ID); got.Title != "Courses" {
		t.Errorf("base = %q : un abandon ne touche pas la base", got.Title)
	}
}

// Sans changeset configuré, la page n'existe pas : rien ne laisse croire
// qu'on met des modifications de côté alors qu'elles sont directes.
func TestChangesPage_SansChangeset(t *testing.T) {
	s, _ := newNotesServer(t)
	if rec := do(s, http.MethodGet, "/changes", nil); rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, veut 404", rec.Code)
	}
}
