package launcher

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig_DerivesPathsFromHomeAndRepo(t *testing.T) {
	cfg := DefaultConfig("/home/andri", "/home/andri/Documents/Jarvis")

	wantVLMModel := "/home/andri/models/olmocr2/olmOCR-2-7B-1025-Q6_K.gguf"
	if cfg.VLMModelPath != wantVLMModel {
		t.Errorf("VLMModelPath = %q, want %q", cfg.VLMModelPath, wantVLMModel)
	}
	wantMMProj := "/home/andri/models/olmocr2/mmproj-olmOCR-2-7B-1025-F16.gguf"
	if cfg.VLMMMProjPath != wantMMProj {
		t.Errorf("VLMMMProjPath = %q, want %q", cfg.VLMMMProjPath, wantMMProj)
	}
	wantLLMModel := "/home/andri/models/qwen3-8b/Qwen3-8B-Q5_K_M.gguf"
	if cfg.LLMModelPath != wantLLMModel {
		t.Errorf("LLMModelPath = %q, want %q", cfg.LLMModelPath, wantLLMModel)
	}
	if cfg.RepoDir != "/home/andri/Documents/Jarvis" {
		t.Errorf("RepoDir = %q, want the given repoDir", cfg.RepoDir)
	}
	if cfg.MongoURI != "" {
		t.Errorf("MongoURI = %q, want empty (never guessed)", cfg.MongoURI)
	}
	if cfg.VLMPort == 0 || cfg.LLMPort == 0 || cfg.VLMPort == cfg.LLMPort {
		t.Errorf("VLMPort/LLMPort = %d/%d, want distinct non-zero ports", cfg.VLMPort, cfg.LLMPort)
	}
	if cfg.Addr == "" {
		t.Error("Addr is empty, want a default listen address for jarvisapp")
	}
}

func TestSaveThenLoadConfig_RoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launcher.json")
	want := DefaultConfig("/home/andri", "/home/andri/Documents/Jarvis")
	want.MongoURI = "mongodb+srv://user:pass@cluster/?appName=x"

	if err := SaveConfig(path, want); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got != want {
		t.Errorf("LoadConfig() = %+v, want %+v", got, want)
	}
}

func TestSaveConfig_RestrictsPermissionsToOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launcher.json")
	if err := SaveConfig(path, DefaultConfig("/home/andri", "/repo")); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %o, want 0600 — le fichier contient l'URI MongoDB en clair", perm)
	}
}

func TestLoadConfig_MissingFile_ReturnsNotExistError(t *testing.T) {
	_, err := LoadConfig(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("LoadConfig() error = %v, want it to satisfy errors.Is(err, os.ErrNotExist)", err)
	}
}

func TestEnsureConfig_MissingFile_CreatesDefaultAndReportsCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "launcher.json")

	cfg, created, err := EnsureConfig(path, "/home/andri", "/home/andri/Documents/Jarvis")
	if err != nil {
		t.Fatalf("EnsureConfig() error = %v", err)
	}
	if !created {
		t.Error("created = false, want true for a first run")
	}
	if cfg.RepoDir != "/home/andri/Documents/Jarvis" {
		t.Errorf("cfg.RepoDir = %q, want the given repoDir", cfg.RepoDir)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("config file was not written to disk: %v", err)
	}
}

func TestEnsureConfig_ExistingFile_LoadsWithoutOverwriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launcher.json")
	existing := DefaultConfig("/home/andri", "/home/andri/Documents/Jarvis")
	existing.MongoURI = "mongodb+srv://already-configured/"
	if err := SaveConfig(path, existing); err != nil {
		t.Fatal(err)
	}

	cfg, created, err := EnsureConfig(path, "/somewhere/else", "/somewhere/else/Jarvis")
	if err != nil {
		t.Fatalf("EnsureConfig() error = %v", err)
	}
	if created {
		t.Error("created = true, want false — the file already existed")
	}
	if cfg.MongoURI != "mongodb+srv://already-configured/" {
		t.Errorf("cfg.MongoURI = %q, want the value already on disk (not overwritten)", cfg.MongoURI)
	}
}
