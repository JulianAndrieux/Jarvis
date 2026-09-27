package accounts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig_AbsentNestPasUneErreur(t *testing.T) {
	c, ok, err := LoadConfig(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || ok {
		t.Errorf("LoadConfig(absent) = (%+v, %v, %v), veut (vide, false, nil) : pas de fichier n'est pas une panne", c, ok, err)
	}
	if _, ok, err := LoadConfig(""); err != nil || ok {
		t.Errorf("LoadConfig(\"\") = (%v, %v)", ok, err)
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "oauth.json")
	if err := os.WriteFile(p, []byte(`{"client_id":" id ","client_secret":"secret","owner_email":" julian@example.test "}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, ok, err := LoadConfig(p)
	if err != nil || !ok {
		t.Fatalf("LoadConfig = (%v, %v)", ok, err)
	}
	if c.ClientID != "id" || c.OwnerEmail != "julian@example.test" {
		t.Errorf("config = %+v : les espaces doivent être retirés", c)
	}
	// Le secret ne doit jamais apparaître dans un journal.
	if strings.Contains(c.String(), "secret") {
		t.Errorf("String() = %q, ne doit pas contenir le secret", c.String())
	}
}

func TestConfig_Validate(t *testing.T) {
	cases := []struct {
		name string
		c    Config
		ok   bool
	}{
		{"complète", Config{ClientID: "i", ClientSecret: "s", OwnerEmail: "a@b.c"}, true},
		{"sans client Google (lien de secours seulement)", Config{OwnerEmail: "a@b.c"}, true},
		{"sans propriétaire", Config{ClientID: "i", ClientSecret: "s"}, false},
		{"propriétaire sans arobase", Config{OwnerEmail: "julian"}, false},
		{"secret sans identifiant", Config{ClientSecret: "s", OwnerEmail: "a@b.c"}, false},
		{"identifiant sans secret", Config{ClientID: "i", OwnerEmail: "a@b.c"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.c.Validate()
			if (err == nil) != tc.ok {
				t.Errorf("Validate() = %v, veut ok=%v", err, tc.ok)
			}
		})
	}
}

func TestLoadConfig_FichierInvalide(t *testing.T) {
	p := filepath.Join(t.TempDir(), "oauth.json")
	if err := os.WriteFile(p, []byte("pas du json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadConfig(p); err == nil {
		t.Error("un fichier illisible doit être une erreur, pas un démarrage silencieux sans authentification")
	}
}

func TestInsecurePermissions(t *testing.T) {
	dir := t.TempDir()
	tight, loose := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	if err := os.WriteFile(tight, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loose, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if bad, err := InsecurePermissions(tight); err != nil || bad {
		t.Errorf("0600 = (%v, %v), veut sûr", bad, err)
	}
	if bad, err := InsecurePermissions(loose); err != nil || !bad {
		t.Errorf("0644 = (%v, %v), veut signalé", bad, err)
	}
}
