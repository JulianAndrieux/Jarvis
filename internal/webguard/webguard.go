// Package webguard refuse les requêtes qu'un autre site déclenche dans le
// navigateur de l'utilisateur.
//
// Pourquoi : un navigateur autorise la soumission d'un formulaire HTML
// inter-origine sans requête préalable. La réponse est illisible par
// l'attaquant, mais l'effet a lieu. Sur Jarvis, POST /tickets lit son
// titre, son besoin et ses critères dans le formulaire ; la relève
// automatique prend le brouillon dans les minutes qui suivent et le
// pilote automatique le fait développer par un agent, puis déployer dans
// main. Autrement dit, sans ce paquet, une page web visitée pendant que
// l'application tourne peut faire écrire et déployer du code décrit par
// un tiers. Écouter sur 127.0.0.1 n'y change rien : c'est le navigateur
// de l'utilisateur qui émet la requête, depuis sa machine.
//
// Le principe : une méthode qui ne change rien (GET, HEAD, OPTIONS) passe
// toujours ; une méthode qui change quelque chose doit venir de
// l'application elle-même. Le navigateur le dit dans Sec-Fetch-Site, un
// en-tête que le code d'une page ne peut pas écrire (liste des en-têtes
// interdits) — donc digne de confiance, contrairement à Origin, que
// l'attaquant contrôle pour ses propres requêtes mais ne peut pas
// falsifier en se faisant passer pour nous.
package webguard

import (
	"fmt"
	"net/http"
)

// idempotent : les méthodes qui ne modifient rien.
func idempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// Allow décide si une requête peut modifier l'état. Fonction pure : le
// middleware n'en est que le câblage.
//
// host est l'hôte visé (r.Host, avec son port), tls indique si la requête
// est arrivée en TLS — les deux servent à reconstruire notre propre
// origine pour la comparer à Origin.
//
// Sans Sec-Fetch-Site ni Origin, la requête est acceptée : ce n'est pas
// un navigateur (curl, script, test), et la menace traitée ici est une
// page web. Un programme qui tourne déjà sur la machine a de toute façon
// mieux à sa disposition. Choix assumé, pas un oubli.
func Allow(method, secFetchSite, origin, host string, tls bool) (ok bool, reason string) {
	if idempotent(method) {
		return true, ""
	}
	if secFetchSite != "" {
		if secFetchSite == "same-origin" {
			return true, ""
		}
		return false, fmt.Sprintf("%s refusé : le navigateur signale une origine %q (seul same-origin peut modifier l'état)", method, secFetchSite)
	}
	if origin != "" {
		if origin == ourOrigin(host, tls) {
			return true, ""
		}
		return false, fmt.Sprintf("%s refusé : origine %q, attendue %q", method, origin, ourOrigin(host, tls))
	}
	return true, ""
}

func ourOrigin(host string, tls bool) string {
	if tls {
		return "https://" + host
	}
	return "http://" + host
}

// Guard est le middleware. Logf, s'il est donné, journalise chaque refus :
// un refus n'est pas une erreur de l'utilisateur, c'est le signal qu'une
// page a essayé — ça mérite une trace.
type Guard struct {
	Logf func(format string, args ...any)
}

func (g Guard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, reason := Allow(r.Method, r.Header.Get("Sec-Fetch-Site"), r.Header.Get("Origin"), r.Host, r.TLS != nil)
		if !ok {
			if g.Logf != nil {
				g.Logf("webguard: %s %s : %s", r.Method, r.URL.Path, reason)
			}
			http.Error(w, "Requête refusée : elle ne vient pas de cette application (contrôle d'origine). "+reason, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
