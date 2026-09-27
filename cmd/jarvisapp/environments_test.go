package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/accounts"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// post : une requête de la page, avec session.
func postAs(s *Server, token, target string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8090"+target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	return rec
}

func getAs(s *Server, token, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8090"+target, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	return rec
}

func TestEnvironments_CreerEtInviter(t *testing.T) {
	s, mgr, store := newAuthServer(t)
	owner := signIn(t, mgr, "julian@example.test")
	ctx := context.Background()

	if rec := postAs(s, owner, "/admin/environments", url.Values{"name": {"Cabinet Dupré"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("création : %d %s", rec.Code, rec.Body.String())
	}
	// L'identifiant est lisible et sans accent : il apparaît dans les
	// journaux et dans les noms de dossiers.
	if _, ok, err := store.Env(ctx, "cabinet-dupre"); err != nil || !ok {
		t.Fatalf("environnement cabinet-dupre = (%v, %v)", ok, err)
	}
	// Le créateur en est membre, sinon il aurait fabriqué un environnement
	// où il ne peut pas entrer.
	ms, _ := store.MembershipsOfEnv(ctx, "cabinet-dupre")
	if len(ms) != 1 || ms[0].Role != tenancy.RoleAdmin {
		t.Errorf("membres = %+v, veut le créateur en admin", ms)
	}

	// Invitation.
	if rec := postAs(s, owner, "/admin/environments/cabinet-dupre/invite",
		url.Values{"email": {"nouveau@example.test"}, "role": {"member"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("invitation : %d %s", rec.Code, rec.Body.String())
	}
	invited, ok, _ := store.UserByEmail(ctx, "nouveau@example.test")
	if !ok {
		t.Fatal("l'invité doit exister avant sa première connexion")
	}
	// Et il entre dans cet environnement.
	token, _, err := mgr.SignIn(ctx, accounts.Profile{
		Sub: "g-nouveau", Email: "nouveau@example.test", EmailVerified: true,
	}, "test")
	if err != nil {
		t.Fatalf("SignIn de l'invité : %v", err)
	}
	scope, okScope, _ := mgr.Resolve(ctx, token)
	if !okScope || scope.Env != "cabinet-dupre" || scope.User != invited.ID {
		t.Errorf("portée de l'invité = %+v", scope)
	}

	// La page les liste.
	page := getAs(s, owner, "/admin/environments").Body.String()
	for _, want := range []string{"Cabinet Dupré", "cabinet-dupre", "nouveau@example.test"} {
		if !strings.Contains(page, want) {
			t.Errorf("la page doit contenir %q", want)
		}
	}
}

// Un rôle inventé vient d'un formulaire, donc de l'extérieur : il ne doit
// pas devenir un rôle.
func TestEnvironments_RefuseUnRoleInvente(t *testing.T) {
	s, mgr, store := newAuthServer(t)
	owner := signIn(t, mgr, "julian@example.test")
	rec := postAs(s, owner, "/admin/environments/cabinet/invite",
		url.Values{"email": {"x@example.test"}, "role": {"superadmin"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("code = %d, veut 422", rec.Code)
	}
	if ms, _ := store.MembershipsOfEnv(context.Background(), "cabinet"); len(ms) != 1 {
		t.Errorf("membres = %+v : l'invitation refusée ne doit rien créer (seule marie devrait être là)", ms)
	}
}

func TestEnvironments_RefusExpliques(t *testing.T) {
	s, mgr, _ := newAuthServer(t)
	owner := signIn(t, mgr, "julian@example.test")
	for _, c := range []struct {
		name   string
		target string
		form   url.Values
		want   string
	}{
		{"sans nom", "/admin/environments", url.Values{"name": {"  "}}, "nom"},
		{"nom sans lettre utilisable", "/admin/environments", url.Values{"name": {"***"}}, "identifiant"},
		{"invitation sans adresse", "/admin/environments/cabinet/invite", url.Values{"email": {""}, "role": {"member"}}, "adresse"},
		{"environnement inconnu", "/admin/environments/fantome/invite", url.Values{"email": {"a@b.c"}, "role": {"member"}}, "inconnu"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := postAs(s, owner, c.target, c.form)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("code = %d, veut 422", rec.Code)
			}
			if !strings.Contains(strings.ToLower(rec.Body.String()), c.want) {
				t.Errorf("le refus doit être expliqué et mentionner %q", c.want)
			}
		})
	}
}

// On ne se retire pas soi-même : ce serait fermer la porte de l'intérieur.
func TestEnvironments_NePasSeRetirerSoiMeme(t *testing.T) {
	s, mgr, store := newAuthServer(t)
	owner := signIn(t, mgr, "julian@example.test")
	me, _, _ := store.UserByEmail(context.Background(), "julian@example.test")
	rec := postAs(s, owner, "/admin/environments/local/remove", url.Values{"user": {string(me.ID)}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d, veut 422", rec.Code)
	}
	if ms, _ := store.MembershipsOfUser(context.Background(), me.ID); len(ms) == 0 {
		t.Error("l'appartenance ne doit pas avoir été retirée")
	}
}

// La gestion des environnements est réservée au propriétaire de l'instance
// (elle vit sous /admin, dont le middleware contrôle le rôle).
func TestEnvironments_ReserveeAuProprietaire(t *testing.T) {
	s, mgr, _ := newAuthServer(t)
	member := signIn(t, mgr, "marie@example.test")
	if rec := getAs(s, member, "/admin/environments"); rec.Code != http.StatusForbidden {
		t.Errorf("code = %d, veut 403", rec.Code)
	}
	if rec := postAs(s, member, "/admin/environments", url.Values{"name": {"Le mien"}}); rec.Code != http.StatusForbidden {
		t.Errorf("création par un membre : code %d, veut 403", rec.Code)
	}
}

// Le sélecteur n'apparaît qu'à partir de deux environnements : choisir
// entre une possibilité n'est pas un choix.
func TestSidebar_SelecteurDEnvironnement(t *testing.T) {
	s, mgr, store := newAuthServer(t)
	ctx := context.Background()
	owner := signIn(t, mgr, "julian@example.test")

	if body := getAs(s, owner, "/sidebar").Body.String(); strings.Contains(body, "env-switch") {
		t.Error("un seul environnement : pas de sélecteur")
	}
	me, _, _ := store.UserByEmail(ctx, "julian@example.test")
	if err := store.SetMembership(ctx, accounts.Membership{Env: "cabinet", User: me.ID, Role: tenancy.RoleMember}); err != nil {
		t.Fatal(err)
	}
	body := getAs(s, owner, "/sidebar").Body.String()
	if !strings.Contains(body, "env-switch") {
		t.Fatalf("deux environnements : un sélecteur est attendu, reçu :\n%s", body)
	}
	if !strings.Contains(body, "Cabinet") {
		t.Error("le sélecteur doit nommer les environnements")
	}
	if !strings.Contains(body, `action="/env/switch"`) {
		t.Error("le changement doit passer par la session, pas par l'URL")
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Cabinet Dupré":      "cabinet-dupre",
		"  Perso  ":          "perso",
		"École & Associés":   "ecole-associes",
		"2026 — Facturation": "2026-facturation",
		"***":                "",
	} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, veut %q", in, got, want)
		}
	}
}
