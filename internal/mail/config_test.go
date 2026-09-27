package mail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/secretbox"
)

func TestConfig_SaveLoadPrivateFile(t *testing.T) {
	dir := t.TempDir()
	key, err := secretbox.LoadOrCreateKey(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sub", "mail.json")
	if _, ok, err := LoadConfigKey(path, key); ok || err != nil {
		t.Fatalf("LoadConfig(missing) = %v, %v, want not configured", ok, err)
	}
	c := Config{Host: "imap.gmail.com:993", User: "moi@gmail.com", Password: "abcdefghijklmnop"}
	if err := SaveConfigKey(path, c, key); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v, want 0600 (the file holds the key to a password)", info.Mode().Perm(), err)
	}
	got, ok, err := LoadConfigKey(path, key)
	if err != nil || !ok {
		t.Fatalf("LoadConfig = %+v, %v, %v", got, ok, err)
	}
	// Hôte, adresse et mot de passe font l'aller-retour ; le champ chiffré,
	// lui, n'a pas de raison d'être comparé.
	if got.Host != c.Host || got.User != c.User || got.Password != c.Password {
		t.Errorf("LoadConfig = %+v, veut %+v", got, c)
	}
}

func TestConfig_Normalize(t *testing.T) {
	c := Config{User: "  moi@gmail.com ", Password: "abcd efgh ijkl mnop"}.Normalize()
	if c.Host != DefaultHost || c.User != "moi@gmail.com" {
		t.Errorf("Normalize = %+v", c)
	}
	if c.Password != "abcdefghijklmnop" {
		t.Errorf("Password = %q, want the spaces Google displays removed", c.Password)
	}
	if got := (Config{Password: "mon mot de passe"}).Normalize().Password; got != "mon mot de passe" {
		t.Errorf("Password = %q, want an ordinary password untouched", got)
	}
	if got := (Config{Host: "imap.free.fr"}).Normalize().Host; got != "imap.free.fr:993" {
		t.Errorf("Host = %q, want the IMAPS port added", got)
	}
}

func TestConfig_Validate(t *testing.T) {
	ok := Config{Host: "imap.gmail.com:993", User: "moi@gmail.com", Password: "x"}
	if err := ok.Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
	for _, c := range []Config{{Host: ok.Host, Password: "x"}, {Host: ok.Host, User: ok.User}, {User: ok.User, Password: "x"}} {
		if err := c.Validate(); err == nil {
			t.Errorf("Validate(%+v) = nil", c)
		}
	}
	if !strings.Contains(ok.String(), "moi@gmail.com") || strings.Contains(Config{Password: "secret"}.String(), "secret") {
		t.Error("String must never show the password")
	}
}

// Le mot de passe n'est plus écrit en clair : c'est la contrainte de vie
// privée du projet, rendue vérifiable (jalon 50).
func TestSaveConfigKey_NEcritPasLeMotDePasseEnClair(t *testing.T) {
	dir := t.TempDir()
	key, err := secretbox.LoadOrCreateKey(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mail.json")
	cfg := Config{Host: "imap.gmail.com:993", User: "julian@example.test", Password: "abcdefghijklmnop"}
	if err := SaveConfigKey(path, cfg, key); err != nil {
		t.Fatalf("SaveConfigKey : %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "abcdefghijklmnop") {
		t.Fatalf("le fichier contient le mot de passe en clair :\n%s", raw)
	}
	got, ok, err := LoadConfigKey(path, key)
	if err != nil || !ok {
		t.Fatalf("LoadConfigKey = (%v, %v)", ok, err)
	}
	if got.Password != "abcdefghijklmnop" {
		t.Errorf("mot de passe relu = %q", got.Password)
	}
	// Une autre machine (autre clé) ne peut pas le lire.
	other, err := secretbox.LoadOrCreateKey(filepath.Join(dir, "autre.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadConfigKey(path, other); err == nil {
		t.Error("une autre clé ne doit pas pouvoir lire le mot de passe")
	}
}

// Sans clé, on refuse d'écrire plutôt que de reposer un mot de passe en
// clair.
func TestSaveConfigKey_RefuseSansCle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mail.json")
	if err := SaveConfig(path, Config{Host: "h", User: "u", Password: "secret"}); err == nil {
		t.Error("écrire un mot de passe sans clé doit être refusé")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("aucun fichier ne doit avoir été écrit")
	}
}

// Une configuration d'avant le chiffrement reste lisible une fois, puis se
// réécrit chiffrée : personne n'a à retaper son mot de passe.
func TestLoadConfigKey_MigreUneAncienneConfiguration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mail.json")
	if err := os.WriteFile(path, []byte(`{"host":"h","user":"u","password":"ancien-secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	key, _ := secretbox.LoadOrCreateKey(filepath.Join(dir, "k"))
	got, ok, err := LoadConfigKey(path, key)
	if err != nil || !ok {
		t.Fatalf("LoadConfigKey = (%v, %v)", ok, err)
	}
	if got.Password != "ancien-secret" {
		t.Fatalf("mot de passe = %q", got.Password)
	}
	if err := SaveConfigKey(path, got, key); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "ancien-secret") {
		t.Errorf("après réécriture, le clair est encore là :\n%s", raw)
	}
	if strings.Contains(string(raw), `"password"`) {
		t.Errorf("l'ancien champ ne doit plus être écrit :\n%s", raw)
	}
}
