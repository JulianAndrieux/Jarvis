package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/accounts"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

func (s *Server) environmentRoutes(r chi.Router) {
	// Sous /admin : la gestion des environnements et des accès appartient au
	// propriétaire de l'instance (le contrôle est fait par le middleware).
	r.Get("/admin/environments", s.handleEnvironments)
	r.Post("/admin/environments", s.handleEnvironmentCreate)
	r.Post("/admin/environments/{env}/invite", s.handleEnvironmentInvite)
	r.Post("/admin/environments/{env}/remove", s.handleEnvironmentRemove)
}

func (s *Server) handleEnvironments(w http.ResponseWriter, r *http.Request) {
	if s.Accounts == nil {
		http.NotFound(w, r)
		return
	}
	s.renderEnvironments(w, r, "")
}

func (s *Server) renderEnvironments(w http.ResponseWriter, r *http.Request, failure string) {
	view, err := s.environmentsView(r)
	if err != nil {
		serverError(w, err)
		return
	}
	view.Error = failure
	status := http.StatusOK
	if failure != "" {
		status = http.StatusUnprocessableEntity
	}
	renderPage(w, r, status, templates.EnvironmentsPage(view), "environments")
}

func (s *Server) environmentsView(r *http.Request) (templates.EnvironmentsView, error) {
	ctx := r.Context()
	scope := scopeOf(r)
	envs, err := s.Accounts.Store.ListEnvs(ctx)
	if err != nil {
		return templates.EnvironmentsView{}, err
	}
	view := templates.EnvironmentsView{
		Roles: []string{string(tenancy.RoleMember), string(tenancy.RoleAdmin), string(tenancy.RoleOwner)},
	}
	for _, e := range envs {
		ev := templates.EnvView{ID: string(e.ID), Name: e.Name, Current: e.ID == scope.Env}
		members, err := s.Accounts.Store.MembershipsOfEnv(ctx, e.ID)
		if err != nil {
			return templates.EnvironmentsView{}, err
		}
		for _, m := range members {
			mv := templates.EnvMemberView{UserID: string(m.User), Role: string(m.Role), Me: m.User == scope.User}
			if u, ok, err := s.Accounts.Store.UserByID(ctx, m.User); err == nil && ok {
				mv.Email, mv.Name = u.Email, u.Name
			}
			ev.Members = append(ev.Members, mv)
		}
		view.Envs = append(view.Envs, ev)
	}
	return view, nil
}

func (s *Server) handleEnvironmentCreate(w http.ResponseWriter, r *http.Request) {
	if s.Accounts == nil {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.renderEnvironments(w, r, "Un nom est nécessaire.")
		return
	}
	id, err := s.freeEnvID(r.Context(), name)
	if err != nil {
		s.renderEnvironments(w, r, err.Error())
		return
	}
	if err := s.Accounts.Store.CreateEnv(r.Context(), accounts.Environment{
		ID: id, Name: name, CreatedAt: time.Now(),
	}); err != nil {
		s.renderEnvironments(w, r, err.Error())
		return
	}
	// Le créateur en est membre, sinon il viendrait de fabriquer un
	// environnement dans lequel il ne peut pas entrer.
	if scope := scopeOf(r); scope.User != "" {
		if err := s.Accounts.Store.SetMembership(r.Context(), accounts.Membership{
			Env: id, User: scope.User, Role: tenancy.RoleAdmin,
		}); err != nil {
			s.renderEnvironments(w, r, err.Error())
			return
		}
	}
	http.Redirect(w, r, "/admin/environments", http.StatusSeeOther)
}

func (s *Server) handleEnvironmentInvite(w http.ResponseWriter, r *http.Request) {
	if s.Accounts == nil {
		http.NotFound(w, r)
		return
	}
	env := tenancy.EnvID(chi.URLParam(r, "env"))
	email := strings.TrimSpace(r.FormValue("email"))
	role := tenancy.Role(r.FormValue("role"))
	switch {
	case email == "" || !strings.Contains(email, "@"):
		s.renderEnvironments(w, r, "Une adresse est nécessaire pour inviter.")
		return
	case role != tenancy.RoleMember && role != tenancy.RoleAdmin && role != tenancy.RoleOwner:
		// Un rôle inventé ne doit pas devenir un rôle : il viendrait d'un
		// formulaire, donc de l'extérieur.
		s.renderEnvironments(w, r, fmt.Sprintf("Rôle inconnu (%s).", role))
		return
	}
	if _, err := s.Accounts.Invite(r.Context(), env, email, role); err != nil {
		s.renderEnvironments(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/admin/environments", http.StatusSeeOther)
}

func (s *Server) handleEnvironmentRemove(w http.ResponseWriter, r *http.Request) {
	if s.Accounts == nil {
		http.NotFound(w, r)
		return
	}
	env := tenancy.EnvID(chi.URLParam(r, "env"))
	user := tenancy.UserID(r.FormValue("user"))
	if user == scopeOf(r).User {
		// Se retirer soi-même du seul environnement où l'on est admin
		// fermerait la porte de l'intérieur.
		s.renderEnvironments(w, r, "On ne se retire pas soi-même.")
		return
	}
	if err := s.Accounts.Store.RemoveMembership(r.Context(), env, user); err != nil {
		s.renderEnvironments(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/admin/environments", http.StatusSeeOther)
}

// freeEnvID dérive un identifiant lisible du nom, et s'assure qu'il est
// libre. Un identifiant lisible plutôt qu'un aléa : il apparaît dans les
// journaux, dans les noms de dossiers de la copie locale et dans les
// fichiers stockés.
func (s *Server) freeEnvID(ctx context.Context, name string) (tenancy.EnvID, error) {
	base := slug(name)
	if base == "" {
		return "", fmt.Errorf("le nom %q ne donne aucun identifiant utilisable", name)
	}
	for i := 0; i < 50; i++ {
		candidate := tenancy.EnvID(base)
		if i > 0 {
			candidate = tenancy.EnvID(fmt.Sprintf("%s-%d", base, i+1))
		}
		_, taken, err := s.Accounts.Store.Env(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("trop d'environnements portent déjà ce nom")
}

// slug : un identifiant en minuscules sans accent ni espace.
func slug(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case unicode.IsLetter(r):
			// Lettre accentuée : ramenée à sa forme de base quand c'est
			// évident, ignorée sinon — un identifiant reste ASCII.
			if base, ok := deaccent[r]; ok {
				b.WriteRune(base)
				prevDash = false
			}
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

var deaccent = map[rune]rune{
	'à': 'a', 'â': 'a', 'ä': 'a', 'á': 'a', 'ã': 'a', 'å': 'a',
	'ç': 'c',
	'è': 'e', 'é': 'e', 'ê': 'e', 'ë': 'e',
	'ì': 'i', 'í': 'i', 'î': 'i', 'ï': 'i',
	'ñ': 'n',
	'ò': 'o', 'ó': 'o', 'ô': 'o', 'ö': 'o', 'õ': 'o',
	'ù': 'u', 'ú': 'u', 'û': 'u', 'ü': 'u',
	'ý': 'y', 'ÿ': 'y',
}
