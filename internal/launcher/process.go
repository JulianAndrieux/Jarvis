package launcher

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/JulianAndrieux/Jarvis/internal/models"
)

// ResolvePath retourne path tel quel s'il est absolu, sinon path joint
// à repoDir — utilisé pour JarvisAppBinary/OutDir, qu'on veut pouvoir
// écrire soit en relatif (le cas courant, cf. DefaultConfig) soit en
// absolu si l'utilisateur édite le fichier de config à la main.
func ResolvePath(repoDir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(repoDir, path)
}

// ArgsForVLM construit les arguments de `llama-server` pour le serveur
// VLM (étage Parsing) — mêmes valeurs que "Setup local complet" dans
// CLAUDE.md.
func ArgsForVLM(cfg Config) []string {
	return []string{
		"-m", cfg.VLMModelPath,
		"--mmproj", cfg.VLMMMProjPath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(cfg.VLMPort),
		"--ctx-size", "8192",
		"--image-min-tokens", "1024",
	}
}

// ArgsForLLM construit les arguments de `llama-server` pour le serveur
// LLM (étage Extraction).
func ArgsForLLM(cfg Config) []string {
	return []string{
		"-m", cfg.LLMModelPath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(cfg.LLMPort),
		"--ctx-size", "8192",
	}
}

// ArgsForCode construit les arguments du serveur du modèle de code
// (jalon 37) : 32k de contexte, un seul emplacement (sinon 4 × 32k),
// cache en 8 bits — mesuré : c'est ce qui tient sur le Mac (24 Go).
func ArgsForCode(cfg Config) []string {
	args := []string{
		"-m", cfg.CodeModelPath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(cfg.CodePort),
		"--ctx-size", "32768", "-np", "1", "--jinja",
		"-ngl", "99", "-fa", "on", "-ctk", "q8_0", "-ctv", "q8_0",
	}
	if cfg.CodeMMProjPath != "" {
		args = append(args, "--mmproj", cfg.CodeMMProjPath)
	}
	return args
}

// ModelProfiles décrit les profils de modèles gérés par jarvisapp
// (jalon 37).
func ModelProfiles(cfg Config, logDir string) models.Config {
	return models.Config{
		Binary: cfg.LlamaServerBinary,
		LogDir: logDir,
		Profiles: map[string][]models.Server{
			"documents": {
				{Name: "vlm", Port: cfg.VLMPort, Args: ArgsForVLM(cfg)},
				{Name: "llm", Port: cfg.LLMPort, Args: ArgsForLLM(cfg)},
			},
			"code": {{Name: "code", Port: cfg.CodePort, Args: ArgsForCode(cfg)}},
		},
	}
}

// ArgsForJarvisApp construit les arguments de cmd/jarvisapp. MongoURI
// n'y figure jamais : elle contient un mot de passe, lisible par tout
// utilisateur de la machine dans la ligne de commande (ps). Elle passe
// par l'environnement (EnvForJarvisApp) ; jarvisapp refuse de démarrer
// sans, avec un message explicite.
func ArgsForJarvisApp(cfg Config) []string {
	args := []string{
		"--vlm-url", fmt.Sprintf("http://127.0.0.1:%d/v1", cfg.VLMPort),
		"--vlm-model", cfg.VLMModel,
		"--vlm-model-version", cfg.VLMModelVersion,
		"--llm-url", fmt.Sprintf("http://127.0.0.1:%d/v1", cfg.LLMPort),
		"--llm-model", cfg.LLMModel,
		"--llm-model-version", cfg.LLMModelVersion,
		"--mongo-db", cfg.MongoDB,
		"--mongo-collection", cfg.MongoCollection,
		"--out-dir", ResolvePath(cfg.RepoDir, cfg.OutDir),
		"--addr", cfg.Addr,
		"--module-dir", cfg.RepoDir,
	}
	if cfg.CodeEnabled() {
		// Réglages mesurés sur le ticket d'exemple (Devstral, 32k de
		// contexte) : l'agent voit bien plus que les 3000 caractères
		// calibrés pour Qwen3-8B à 8k.
		args = append(args,
			"--models-file", cfg.ModelsFile,
			"--agent-url", fmt.Sprintf("http://127.0.0.1:%d/v1", cfg.CodePort),
			"--agent-model", cfg.CodeModel,
			"--agent-context-chars", "55000",
			"--agent-tool-output-chars", "12000",
			"--agent-max-tokens", "12288",
			"--agent-timeout", "90m",
			"--agent-temperature", "0.15",
		)
	}
	return args
}

// EnvForJarvisApp : l'environnement base, avec MONGO_URI fixée à celle de
// la configuration (toute valeur précédente retirée). base n'est pas
// modifié.
func EnvForJarvisApp(cfg Config, base []string) []string {
	env := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if !strings.HasPrefix(kv, "MONGO_URI=") {
			env = append(env, kv)
		}
	}
	return append(env, "MONGO_URI="+cfg.MongoURI)
}

// StartProcess démarre name avec args et l'environnement env (nil :
// celui du lanceur), répertoire de travail dir,
// redirigeant stdout+stderr vers un fichier de log (créé/complété, pas
// écrasé — pour garder la trace d'exécutions précédentes le temps d'une
// session de debug). Ne bloque pas : l'appelant surveille cmd (Wait,
// Process.Signal) séparément. Essentiel ici puisque le lanceur est
// pensé pour tourner sans terminal visible (raccourci Dock) — sans ce
// fichier, toute sortie des serveurs serait perdue.
func StartProcess(name string, args, env []string, dir, logPath string) (*exec.Cmd, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("launcher: create log dir for %s: %w", logPath, err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("launcher: open log file %s: %w", logPath, err)
	}

	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("launcher: start %s: %w", name, err)
	}

	// logFile doit rester ouvert tant que le process écrit dedans ; il
	// sera fermé par le GC/la fin du processus lanceur — acceptable pour
	// un outil de développement qui tourne le temps d'une session.
	return cmd, nil
}
