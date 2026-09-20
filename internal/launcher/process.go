package launcher

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// ArgsForJarvisApp construit les arguments de cmd/jarvisapp. MongoURI
// n'est jamais omis même vide : jarvisapp refuse déjà de démarrer sans
// (message explicite), pas la peine de dupliquer cette validation ici.
func ArgsForJarvisApp(cfg Config) []string {
	return []string{
		"--vlm-url", fmt.Sprintf("http://127.0.0.1:%d/v1", cfg.VLMPort),
		"--vlm-model", cfg.VLMModel,
		"--vlm-model-version", cfg.VLMModelVersion,
		"--llm-url", fmt.Sprintf("http://127.0.0.1:%d/v1", cfg.LLMPort),
		"--llm-model", cfg.LLMModel,
		"--llm-model-version", cfg.LLMModelVersion,
		"--mongo-uri", cfg.MongoURI,
		"--mongo-db", cfg.MongoDB,
		"--mongo-collection", cfg.MongoCollection,
		"--out-dir", ResolvePath(cfg.RepoDir, cfg.OutDir),
		"--addr", cfg.Addr,
		"--module-dir", cfg.RepoDir,
	}
}

// StartProcess démarre name avec args (répertoire de travail dir),
// redirigeant stdout+stderr vers un fichier de log (créé/complété, pas
// écrasé — pour garder la trace d'exécutions précédentes le temps d'une
// session de debug). Ne bloque pas : l'appelant surveille cmd (Wait,
// Process.Signal) séparément. Essentiel ici puisque le lanceur est
// pensé pour tourner sans terminal visible (raccourci Dock) — sans ce
// fichier, toute sortie des serveurs serait perdue.
func StartProcess(name string, args []string, dir, logPath string) (*exec.Cmd, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("launcher: create log dir for %s: %w", logPath, err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("launcher: open log file %s: %w", logPath, err)
	}

	cmd := exec.Command(name, args...)
	cmd.Dir = dir
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
