// Package launcher orchestre le démarrage local de tout ce qu'il faut
// pour utiliser Jarvis en un geste : les deux serveurs llama.cpp (VLM,
// LLM) et l'application web unifiée (cmd/jarvisapp) — voir CLAUDE.md,
// jalon 14. Pensé pour être lancé sans terminal (double-clic depuis un
// raccourci macOS), donc rien ici ne dépend d'une variable
// d'environnement ou d'un flag : la configuration vit dans un fichier
// JSON, lu/écrit explicitement.
package launcher

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config rassemble tout ce que le lanceur doit savoir pour démarrer les
// trois processus. Aucun champ n'a de valeur cachée au moment de
// l'utilisation : DefaultConfig les remplit explicitement une fois, le
// résultat est écrit sur disque et relu tel quel ensuite — cohérent avec
// "pas de défaut silencieux".
type Config struct {
	RepoDir         string `json:"repo_dir"`         // racine du module Jarvis (cmd/jarvisapp, go.mod)
	JarvisAppBinary string `json:"jarvisapp_binary"` // chemin du binaire jarvisapp, relatif à RepoDir si non absolu
	Addr            string `json:"addr"`             // adresse d'écoute de jarvisapp
	OutDir          string `json:"out_dir"`          // copie locale additionnelle des résultats, relatif à RepoDir si non absolu

	LlamaServerBinary string `json:"llama_server_binary"` // nom/chemin du binaire llama-server (llama.cpp)

	VLMPort         int    `json:"vlm_port"`
	VLMModelPath    string `json:"vlm_model_path"`
	VLMMMProjPath   string `json:"vlm_mmproj_path"`
	VLMModel        string `json:"vlm_model"`         // identifiant transmis à jarvisapp (--vlm-model)
	VLMModelVersion string `json:"vlm_model_version"` // idem (--vlm-model-version)

	LLMPort         int    `json:"llm_port"`
	LLMModelPath    string `json:"llm_model_path"`
	LLMModel        string `json:"llm_model"`
	LLMModelVersion string `json:"llm_model_version"`

	// MongoURI n'est JAMAIS deviné ni pré-rempli par DefaultConfig — voir
	// cmd/jarvis-launcher : demandé une fois via une boîte de dialogue
	// macOS au premier lancement, puis mémorisé ici.
	MongoURI        string `json:"mongo_uri"`
	MongoDB         string `json:"mongo_db"`
	MongoCollection string `json:"mongo_collection"`
}

// DefaultConfig construit la configuration par défaut à partir des
// chemins documentés dans CLAUDE.md ("Setup local complet") : les poids
// des modèles sous homeDir/models/..., le module sous repoDir. Fonction
// pure (aucun accès disque) : les chemins ne sont pas vérifiés ici, ils
// le sont au démarrage réel (cmd/jarvis-launcher), avec un message
// explicite si un modèle est introuvable.
func DefaultConfig(homeDir, repoDir string) Config {
	return Config{
		RepoDir:         repoDir,
		JarvisAppBinary: "bin/jarvisapp",
		Addr:            "127.0.0.1:8090",
		OutDir:          "data/results",

		LlamaServerBinary: "llama-server",

		VLMPort:         8080,
		VLMModelPath:    filepath.Join(homeDir, "models", "olmocr2", "olmOCR-2-7B-1025-Q6_K.gguf"),
		VLMMMProjPath:   filepath.Join(homeDir, "models", "olmocr2", "mmproj-olmOCR-2-7B-1025-F16.gguf"),
		VLMModel:        "olmOCR-2-7B-1025",
		VLMModelVersion: "Q6_K",

		LLMPort:         8081,
		LLMModelPath:    filepath.Join(homeDir, "models", "qwen3-8b", "Qwen3-8B-Q5_K_M.gguf"),
		LLMModel:        "qwen3-8b",
		LLMModelVersion: "Q5_K_M",

		MongoDB:         "jarvis",
		MongoCollection: "jobs",
	}
}

// LoadConfig lit et décode le fichier JSON à path. Si le fichier
// n'existe pas, l'erreur satisfait errors.Is(err, os.ErrNotExist) — à
// l'appelant de décider (EnsureConfig le fait) si c'est un premier
// lancement légitime ou une vraie erreur.
func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("launcher: decode config %s: %w", path, err)
	}
	return cfg, nil
}

// SaveConfig écrit cfg en JSON indenté à path, créant les répertoires
// parents si besoin. Permissions restreintes au propriétaire (0600) :
// le fichier contient l'URI MongoDB en clair (identifiants compris).
func SaveConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("launcher: create config dir: %w", err)
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("launcher: encode config: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("launcher: write config %s: %w", path, err)
	}
	return nil
}

// EnsureConfig charge la configuration à path, ou la crée avec
// DefaultConfig(homeDir, repoDir) si le fichier n'existe pas encore —
// created indique lequel des deux cas s'est produit, pour que l'appelant
// puisse par exemple prévenir l'utilisateur qu'il doit la compléter
// (MongoURI notamment) avant de continuer.
func EnsureConfig(path, homeDir, repoDir string) (cfg Config, created bool, err error) {
	cfg, err = LoadConfig(path)
	if err == nil {
		return cfg, false, nil
	}
	if !os.IsNotExist(err) {
		return Config{}, false, err
	}

	cfg = DefaultConfig(homeDir, repoDir)
	if err := SaveConfig(path, cfg); err != nil {
		return Config{}, false, err
	}
	return cfg, true, nil
}
