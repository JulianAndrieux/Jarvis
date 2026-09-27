package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/changes"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

func (s *Server) activityRoutes(r chi.Router) {
	r.Get("/activity", s.handleActivity)
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	if s.Journal == nil {
		http.NotFound(w, r)
		return
	}
	scope := scopeOf(r)
	everyone := r.URL.Query().Get("tout") == "1"
	if scope.User == "" {
		// Mode mono-utilisateur (aucune authentification configurée) : il n'y
		// a pas de « moi » sur quoi filtrer, et filtrer sur un user vide
		// n'afficherait rien. La vue montre donc tout l'environnement, et le
		// dit.
		everyone = true
	}
	q := changes.Query{Env: scope.Env}
	if !everyone {
		q.User = scope.User
	}
	ops, err := s.Journal.List(r.Context(), q)
	if err != nil {
		serverError(w, err)
		return
	}
	view := templates.ActivityView{
		Everyone: everyone,
		EnvName:  s.envName(r.Context(), scope.Env),
		Entries:  make([]templates.ActivityEntry, 0, len(ops)),
	}
	who := s.userNames(r.Context(), ops)
	for _, o := range ops {
		view.Entries = append(view.Entries, templates.ActivityEntry{
			When:  o.At.Local().Format("02/01 15:04"),
			Who:   who[o.User],
			Kind:  string(o.Kind),
			Label: o.Label,
			What:  describeOp(o),
			Href:  entityHref(o),
			Mine:  o.User == scope.User && o.User != "",
		})
	}
	renderPage(w, r, http.StatusOK, templates.ActivityPage(view), "activity")
}

// envName : le nom de l'environnement, ou son identifiant si les comptes
// ne sont pas configurés.
func (s *Server) envName(ctx context.Context, env tenancy.EnvID) string {
	if s.Accounts == nil {
		return string(env)
	}
	if e, ok, err := s.Accounts.Store.Env(ctx, env); err == nil && ok && e.Name != "" {
		return e.Name
	}
	return string(env)
}

// userNames : le nom à afficher pour chaque auteur. Un identifiant ne dit
// rien à personne ; sans comptes configurés, on le montre tout de même
// plutôt que rien.
func (s *Server) userNames(ctx context.Context, ops []changes.Op) map[tenancy.UserID]string {
	names := map[tenancy.UserID]string{}
	for _, o := range ops {
		if o.User == "" || names[o.User] != "" {
			continue
		}
		names[o.User] = string(o.User)
		if s.Accounts == nil {
			continue
		}
		if u, ok, err := s.Accounts.Store.UserByID(ctx, o.User); err == nil && ok {
			if u.Name != "" {
				names[o.User] = u.Name
			} else if u.Email != "" {
				names[o.User] = u.Email
			}
		}
	}
	return names
}

// describeOp met une opération en français. Les valeurs sont du JSON :
// affichées telles quelles pour un texte court, résumées au-delà — une
// note entière n'a rien à faire dans une ligne de tableau.
func describeOp(o changes.Op) string {
	switch o.Action {
	case changes.Create:
		return "créé"
	case changes.Delete:
		return "supprimé"
	}
	field := o.Field
	if field == "" {
		field = "modifié"
	}
	return fmt.Sprintf("%s : %s → %s", field, shortJSON(o.Before), shortJSON(o.After))
}

// shortJSON rend une valeur lisible et bornée.
func shortJSON(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == `""` || v == "null" {
		return "(vide)"
	}
	if len(v) > 60 {
		return v[:60] + "…"
	}
	return v
}

// entityHref : le lien vers l'entité modifiée. Une entité supprimée n'a
// plus de page : pas de lien plutôt qu'un lien mort.
func entityHref(o changes.Op) string {
	if o.Action == changes.Delete {
		return ""
	}
	switch o.Kind {
	case changes.KindDocument:
		return "/documents/" + o.Target
	case changes.KindNote:
		return "/notes/" + o.Target
	case changes.KindTask:
		return "/tasks/" + o.Target
	}
	return ""
}
