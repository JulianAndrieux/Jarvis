// Command codebrowser sert une page de contrôle locale de
// l'architecture du code Jarvis : navigateur de classes (types, champs,
// méthodes, embeddings, interfaces) façon Smalltalk / Glamorous
// Toolkit, et une page de tests catégorisés par package, exécutables
// localement avec résultat affiché immédiatement. Outil de
// développement uniquement — ne touche à aucune donnée du pipeline
// d'extraction, n'a besoin d'aucun modèle VLM/LLM.
package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

//go:embed static
var staticFS embed.FS

func main() {
	addr := flag.String("addr", "127.0.0.1:8091", "Adresse d'écoute HTTP")
	moduleDir := flag.String("module-dir", "", "Racine du module Go à analyser (vide = répertoire courant)")
	flag.Parse()

	dir := *moduleDir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			log.Fatalf("codebrowser: répertoire courant : %v", err)
		}
		dir = wd
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		log.Fatalf("codebrowser: chemin absolu de %s : %v", dir, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		log.Fatalf("codebrowser: pas de go.mod dans %s (--module-dir doit pointer sur la racine du module) : %v", dir, err)
	}

	srv := &Server{ModuleDir: dir}
	log.Printf("codebrowser: analyse de %s...", dir)
	if err := srv.Refresh(); err != nil {
		log.Fatalf("codebrowser: %v", err)
	}

	staticContent, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("codebrowser: static assets: %v", err)
	}

	mux := srv.Routes()
	mux.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticContent))))

	log.Printf("codebrowser: listening on http://%s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("codebrowser: %v", err)
	}
}
