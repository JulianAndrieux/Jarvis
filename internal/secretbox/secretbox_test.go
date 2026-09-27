package secretbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKey_CreeePuisRelue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	k1, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("LoadOrCreateKey : %v", err)
	}
	// Le fichier n'est lisible que par son propriétaire : c'est la clé de
	// tout ce qui est chiffré.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %o, veut 600", perm)
	}
	k2, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if k1 != k2 {
		t.Error("la clé doit être relue, pas régénérée : sinon tout ce qui était chiffré devient illisible")
	}
}

func TestSealOpen(t *testing.T) {
	k, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "k"))
	if err != nil {
		t.Fatal(err)
	}
	secret := "abcd efgh ijkl mnop"
	sealed, err := Seal(k, []byte(secret))
	if err != nil {
		t.Fatalf("Seal : %v", err)
	}
	if strings.Contains(sealed, "abcd") {
		t.Error("le texte chiffré ne doit pas contenir le clair")
	}
	got, err := Open(k, sealed)
	if err != nil {
		t.Fatalf("Open : %v", err)
	}
	if string(got) != secret {
		t.Errorf("Open = %q, veut %q", got, secret)
	}
}

// Deux chiffrements du même texte diffèrent (nonce aléatoire) : sinon on
// pourrait dire que deux comptes partagent le même mot de passe.
func TestSeal_NonDeterministe(t *testing.T) {
	k, _ := LoadOrCreateKey(filepath.Join(t.TempDir(), "k"))
	a, err := Seal(k, []byte("même secret"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Seal(k, []byte("même secret"))
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("deux chiffrements du même texte ne doivent pas être identiques")
	}
}

func TestOpen_RefuseUneAutreCle(t *testing.T) {
	dir := t.TempDir()
	k1, _ := LoadOrCreateKey(filepath.Join(dir, "k1"))
	k2, _ := LoadOrCreateKey(filepath.Join(dir, "k2"))
	sealed, err := Seal(k1, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(k2, sealed); err == nil {
		t.Error("une autre clé ne doit rien pouvoir lire — c'est tout l'intérêt : la base seule ne suffit pas")
	}
}

func TestOpen_RefuseUnTexteAltere(t *testing.T) {
	k, _ := LoadOrCreateKey(filepath.Join(t.TempDir(), "k"))
	sealed, err := Seal(k, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	// Un octet changé : l'authentification doit le voir.
	altered := []byte(sealed)
	altered[len(altered)-2] ^= 'x'
	if _, err := Open(k, string(altered)); err == nil {
		t.Error("un texte chiffré altéré doit être refusé")
	}
	for _, bad := range []string{"", "pas du base64 !", "AAAA"} {
		if _, err := Open(k, bad); err == nil {
			t.Errorf("Open(%q) doit échouer", bad)
		}
	}
}

// Une clé absente et un dossier inexistant : le dossier est créé, pas une
// erreur (le lanceur écrit dans ~/.jarvis avant que rien n'existe).
func TestLoadOrCreateKey_CreeLeDossier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sous", "dossier", "secret.key")
	if _, err := LoadOrCreateKey(path); err != nil {
		t.Fatalf("LoadOrCreateKey : %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("la clé doit exister : %v", err)
	}
}

// Une clé de mauvaise taille est une erreur claire, pas un chiffrement
// silencieusement faible.
func TestLoadOrCreateKey_RefuseUneCleTronquee(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	if err := os.WriteFile(path, []byte("trop court"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKey(path); err == nil {
		t.Error("une clé de mauvaise taille doit être refusée")
	}
}
