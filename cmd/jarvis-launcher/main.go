// Command jarvis-launcher démarre en un geste tout ce qu'il faut pour
// utiliser Jarvis en local : les deux serveurs llama.cpp (VLM, LLM) et
// l'application web unifiée (cmd/jarvisapp), puis ouvre le navigateur.
// Pensé pour être lancé sans terminal (raccourci Dock, cf. CLAUDE.md
// jalon 14) : toute la configuration vit dans un fichier JSON
// (~/.jarvis/launcher.json, créé avec des valeurs par défaut au premier
// lancement), toute la sortie va dans des fichiers de log
// (~/.jarvis/logs/) en plus du terminal quand il y en a un.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/deploy"
	"github.com/JulianAndrieux/Jarvis/internal/launcher"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "jarvis-launcher: répertoire personnel introuvable : %v\n", err)
		os.Exit(1)
	}

	jarvisDir := filepath.Join(home, ".jarvis")
	logFile, err := setupLogging(filepath.Join(jarvisDir, "logs", "launcher.log"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "jarvis-launcher: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	if err := run(jarvisDir, home); err != nil {
		log.Fatalf("jarvis-launcher: %v", err)
	}
}

// setupLogging fait écrire log.* à la fois sur stderr (utile lancé
// depuis un terminal) et dans un fichier (seul moyen d'observer quoi
// que ce soit une fois lancé sans terminal, depuis le Dock).
func setupLogging(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("créer le répertoire de logs: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("ouvrir %s: %w", path, err)
	}
	log.SetOutput(newTeeWriter(os.Stderr, f))
	log.SetFlags(log.LstdFlags)
	return f, nil
}

func run(jarvisDir, home string) error {
	// Lancé depuis le Finder/Dock, le PATH est minimal : sans Homebrew ni
	// Go, ni llama-server (ici), ni go et soffice (jarvisapp, qui hérite
	// de cet environnement) ne sont trouvés. Trouvé au jalon 27.
	os.Setenv("PATH", launcher.WithToolPaths(os.Getenv("PATH"), home))

	defaultRepoDir := filepath.Join(home, "Documents", "Jarvis")
	configPath := filepath.Join(jarvisDir, "launcher.json")

	cfg, created, err := launcher.EnsureConfig(configPath, home, defaultRepoDir)
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if created {
		log.Printf("première utilisation : configuration créée à %s (chemins des modèles pré-remplis d'après CLAUDE.md, à ajuster si besoin)", configPath)
	}

	if cfg.MongoURI == "" {
		uri, err := resolveMongoURI()
		if err != nil {
			return fmt.Errorf("URI MongoDB : %w", err)
		}
		cfg.MongoURI = uri
		if err := launcher.SaveConfig(configPath, cfg); err != nil {
			return fmt.Errorf("enregistrer l'URI MongoDB dans la configuration : %w", err)
		}
		log.Printf("URI MongoDB enregistrée dans %s pour les prochains lancements", configPath)
	}

	if err := checkFileExists("modèle VLM", cfg.VLMModelPath); err != nil {
		return err
	}
	if err := checkFileExists("mmproj VLM", cfg.VLMMMProjPath); err != nil {
		return err
	}
	if err := checkFileExists("modèle LLM", cfg.LLMModelPath); err != nil {
		return err
	}
	if _, err := exec.LookPath(cfg.LlamaServerBinary); err != nil {
		return fmt.Errorf("binaire %q introuvable sur le PATH (brew install llama.cpp ?) : %w", cfg.LlamaServerBinary, err)
	}
	jarvisAppPath := launcher.ResolvePath(cfg.RepoDir, cfg.JarvisAppBinary)
	if err := checkFileExists("binaire jarvisapp", jarvisAppPath); err != nil {
		return fmt.Errorf("%w (lance `make build-app` dans %s)", err, cfg.RepoDir)
	}

	logDir := filepath.Join(jarvisDir, "logs")
	var started []*exec.Cmd

	vlmAddr := fmt.Sprintf("127.0.0.1:%d", cfg.VLMPort)
	if launcher.PortOpen(vlmAddr, 300*time.Millisecond) {
		log.Printf("VLM : un serveur écoute déjà sur %s, réutilisé tel quel", vlmAddr)
	} else {
		log.Printf("VLM : démarrage de %s...", cfg.LlamaServerBinary)
		cmd, err := launcher.StartProcess(cfg.LlamaServerBinary, launcher.ArgsForVLM(cfg), cfg.RepoDir, filepath.Join(logDir, "vlm.log"))
		if err != nil {
			return fmt.Errorf("démarrage VLM : %w", err)
		}
		started = append(started, cmd)
	}

	llmAddr := fmt.Sprintf("127.0.0.1:%d", cfg.LLMPort)
	if launcher.PortOpen(llmAddr, 300*time.Millisecond) {
		log.Printf("LLM : un serveur écoute déjà sur %s, réutilisé tel quel", llmAddr)
	} else {
		log.Printf("LLM : démarrage de %s...", cfg.LlamaServerBinary)
		cmd, err := launcher.StartProcess(cfg.LlamaServerBinary, launcher.ArgsForLLM(cfg), cfg.RepoDir, filepath.Join(logDir, "llm.log"))
		if err != nil {
			return fmt.Errorf("démarrage LLM : %w", err)
		}
		started = append(started, cmd)
	}

	// Chargement à froid observé jusqu'à ~30s (CLAUDE.md, "Performances
	// observées") ; 5 min de marge large plutôt qu'un timeout serré.
	healthCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	log.Printf("VLM : attente de %s/health...", vlmAddr)
	if err := launcher.WaitHealthy(healthCtx, "http://"+vlmAddr+"/health", 2*time.Second); err != nil {
		terminateAll(started)
		return fmt.Errorf("VLM jamais prêt : %w", err)
	}
	log.Printf("LLM : attente de %s/health...", llmAddr)
	if err := launcher.WaitHealthy(healthCtx, "http://"+llmAddr+"/health", 2*time.Second); err != nil {
		terminateAll(started)
		return fmt.Errorf("LLM jamais prêt : %w", err)
	}
	log.Printf("VLM et LLM prêts.")

	appLog := filepath.Join(logDir, "jarvisapp.log")
	startApp := func() (*exec.Cmd, error) {
		cmd, err := launcher.StartProcess(jarvisAppPath, launcher.ArgsForJarvisApp(cfg), cfg.RepoDir, appLog)
		if err != nil {
			return nil, err
		}
		appCtx, cancelApp := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancelApp()
		if err := launcher.WaitHealthy(appCtx, "http://"+cfg.Addr+"/", 500*time.Millisecond); err != nil {
			return cmd, err
		}
		return cmd, nil
	}
	var app *exec.Cmd
	if launcher.PortOpen(cfg.Addr, 300*time.Millisecond) {
		log.Printf("jarvisapp : un serveur écoute déjà sur %s, réutilisé tel quel", cfg.Addr)
	} else {
		log.Printf("jarvisapp : démarrage...")
		cmd, err := startApp()
		if cmd != nil {
			started = append(started, cmd)
		}
		if err != nil {
			terminateAll(started)
			return fmt.Errorf("jarvisapp jamais prêt : %w", err)
		}
		app = cmd
	}
	log.Printf("jarvisapp prêt sur http://%s", cfg.Addr)

	openBrowser(cfg.Addr)

	waitForShutdown(started, app, startApp, filepath.Join(jarvisDir, "deploy.json"), appLog)
	return nil
}

func checkFileExists(label, path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("%s introuvable à %s : %w", label, path, err)
	}
	return nil
}

// resolveMongoURI essaie d'abord la variable d'environnement MONGO_URI
// (pratique en lancement depuis un terminal), puis une boîte de
// dialogue macOS (seul moyen de demander quoi que ce soit sans
// terminal, cf. en-tête de fichier). Jamais de valeur devinée ou vide
// acceptée silencieusement : jarvisapp refuserait de toute façon de
// démarrer sans, mais on préfère le dire clairement ici plutôt que de
// laisser échouer plus loin sans contexte.
func resolveMongoURI() (string, error) {
	if uri := os.Getenv("MONGO_URI"); uri != "" {
		return uri, nil
	}
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("MONGO_URI n'est pas défini dans l'environnement (pas de boîte de dialogue hors macOS) — exporte MONGO_URI ou édite le fichier de configuration")
	}

	script := `display dialog "URI de connexion MongoDB (ex: Atlas) :" default answer "" with title "Jarvis — configuration" buttons {"Annuler", "OK"} default button "OK"`
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return "", fmt.Errorf("boîte de dialogue annulée ou échouée : %w", err)
	}
	uri, err := launcher.ParseOSAScriptTextReturned(string(out))
	if err != nil {
		return "", err
	}
	if uri == "" {
		return "", fmt.Errorf("URI MongoDB vide")
	}
	return uri, nil
}

func openBrowser(addr string) {
	url := "http://" + addr
	if runtime.GOOS != "darwin" {
		log.Printf("ouvre %s dans ton navigateur", url)
		return
	}
	if err := exec.Command("open", url).Start(); err != nil {
		log.Printf("impossible d'ouvrir le navigateur automatiquement (%v) — ouvre %s manuellement", err, url)
	}
}

// terminateAll envoie SIGTERM à chaque processus démarré par CE
// lanceur (jamais à un processus détecté déjà en cours — on ne coupe
// pas ce qu'on n'a pas allumé).
func terminateAll(cmds []*exec.Cmd) {
	for _, cmd := range cmds {
		if cmd.Process == nil {
			continue
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
}

// waitForShutdown bloque jusqu'à un signal d'arrêt (Ctrl-C dans un
// terminal, ou SIGTERM à la fermeture) ou jusqu'à ce qu'un des
// processus démarrés par ce lanceur se termine de lui-même (crash) —
// puis arrête proprement les autres.
//
// Exception (jalon 30) : si jarvisapp s'arrête pendant un déploiement pas
// encore confirmé (marqueur), l'ancienne version est rétablie et relancée
// — c'est elle qui ramène main en arrière et renvoie le ticket en revue.
func waitForShutdown(started []*exec.Cmd, app *exec.Cmd, startApp func() (*exec.Cmd, error), markerPath, appLog string) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	type exit struct {
		cmd *exec.Cmd
		err error
	}
	exited := make(chan exit, len(started)+1)
	watch := func(cmd *exec.Cmd) {
		go func() { exited <- exit{cmd, cmd.Wait()} }()
	}
	for _, cmd := range started {
		watch(cmd)
	}

	for {
		select {
		case <-sigCh:
			log.Printf("arrêt demandé, fin des processus démarrés par ce lanceur...")
			terminateAll(started)
			return
		case e := <-exited:
			if app != nil && e.cmd == app {
				reason := fmt.Sprintf("jarvisapp s'est arrêté (%v)\n%s", e.err, logTail(appLog, 2000))
				rolled, err := deploy.RollbackBinary(markerPath, reason)
				if err != nil {
					log.Printf("retour arrière du déploiement impossible : %v", err)
				}
				if rolled {
					log.Printf("la nouvelle version de jarvisapp s'est arrêtée avant d'être confirmée : ancienne version rétablie, redémarrage...")
					cmd, err := startApp()
					if cmd != nil {
						started = append(started, cmd)
						app = cmd
						watch(cmd)
					}
					if err == nil {
						log.Printf("jarvisapp (ancienne version) de nouveau en service")
						continue
					}
					log.Printf("l'ancienne version ne redémarre pas non plus : %v", err)
				}
			}
			log.Printf("%s s'est arrêté de façon inattendue, arrêt du reste", e.cmd.Path)
			terminateAll(started)
			return
		}
	}
}

// logTail : la fin d'un journal (la raison d'un arrêt s'y trouve).
func logTail(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	off := info.Size() - n
	if off < 0 {
		off = 0
	}
	buf := make([]byte, info.Size()-off)
	f.ReadAt(buf, off)
	return string(buf)
}

// newTeeWriter écrit sur tous les writers donnés (log.SetOutput
// n'accepte qu'un seul io.Writer).
func newTeeWriter(writers ...*os.File) *teeWriter {
	return &teeWriter{writers: writers}
}

type teeWriter struct {
	writers []*os.File
}

func (t *teeWriter) Write(p []byte) (int, error) {
	for _, w := range t.writers {
		if _, err := w.Write(p); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
