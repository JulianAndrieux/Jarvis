package mail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfig_SaveLoadPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "mail.json")
	if _, ok, err := LoadConfig(path); ok || err != nil {
		t.Fatalf("LoadConfig(missing) = %v, %v, want not configured", ok, err)
	}
	c := Config{Host: "imap.gmail.com:993", User: "moi@gmail.com", Password: "abcdefghijklmnop"}
	if err := SaveConfig(path, c); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v, want 0600 (the file holds a password)", info.Mode().Perm(), err)
	}
	got, ok, err := LoadConfig(path)
	if err != nil || !ok || got != c {
		t.Errorf("LoadConfig = %+v, %v, %v", got, ok, err)
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
