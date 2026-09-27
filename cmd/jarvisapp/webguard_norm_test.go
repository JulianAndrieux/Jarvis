package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Norme (esprit du jalon 9 : une norme est un test ordinaire) : aucune
// route qui modifie l'état ne doit accepter une requête déclenchée par un
// autre site. Le test énumère les routes réellement montées — donc une
// route ajoutée demain, ou montée sur un sous-routeur sans le middleware,
// fait échouer ce test au lieu de rouvrir la porte du §9.1 du plan
// (formulaire hostile -> POST /tickets -> relève -> pilote automatique ->
// déploiement).
func TestMutatingRoutes_RejectCrossSiteRequests(t *testing.T) {
	s, _ := newTestServer(t, &blockingRunner{})
	routes := s.Routes()

	param := regexp.MustCompile(`\{[^}]*\}`)
	checked := 0
	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		switch method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			return nil // ne modifie rien : autorisé d'où que ça vienne
		}
		// Un motif de route n'est pas une URL : {id} et /* sont remplacés par
		// une valeur quelconque — le refus a lieu avant le handler, donc la
		// valeur n'a aucune importance.
		path := param.ReplaceAllString(route, "x")
		if len(path) > 1 && path[len(path)-1] == '*' {
			path = path[:len(path)-1] + "x"
		}

		req := httptest.NewRequest(method, "http://127.0.0.1:8090"+path, nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s : code %d, veut 403 — cette route accepte une requête d'un autre site", method, route, rec.Code)
		}
		checked++
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk : %v", err)
	}
	if checked == 0 {
		t.Fatal("aucune route modifiante trouvée : le test ne prouve rien")
	}
	t.Logf("%d routes modifiantes vérifiées", checked)
}

// Note sur le pendant « une requête de la page passe » : il n'est pas
// écrit en rejouant chaque route, parce qu'un tel test appellerait vraiment
// les handlers — dont POST /admin/tests/run, qui lance `go test` sur le
// module (constaté : 145 s), et POST /admin/refresh, qui réanalyse tout le
// code. Le laisser-passer est prouvé là où il se décide, sans effet de
// bord : internal/webguard (TestAllow, TestMiddleware_LaissepasserLaPage).
// Et toutes les requêtes des autres tests de ce paquet passent sans
// en-tête d'origine, donc par le chemin autorisé : elles échoueraient en
// bloc si le middleware refusait à tort.
