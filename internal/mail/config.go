package mail

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DefaultHost : IMAP de Gmail (TLS).
const DefaultHost = "imap.gmail.com:993"

// Config : la boîte à relever. Le mot de passe (d'application, pour
// Gmail) reste sur la machine, dans un fichier lisible par l'utilisateur
// seul — jamais dans Atlas.
type Config struct {
	Host     string `json:"host"`
	User     string `json:"user"`
	Password string `json:"password"`
}

// String : pour les journaux, sans le mot de passe.
func (c Config) String() string { return c.User + " @ " + c.Host }

// appPassword : un mot de passe d'application Google, tel qu'affiché
// (« abcd efgh ijkl mnop »).
var appPassword = regexp.MustCompile(`^[a-zA-Z]{4}( [a-zA-Z]{4}){3}$`)

// Normalize : serveur par défaut, port IMAPS ajouté, espaces autour de
// l'adresse retirés, et ceux qu'affiche Google dans un mot de passe
// d'application.
func (c Config) Normalize() Config {
	c.Host, c.User = strings.TrimSpace(c.Host), strings.TrimSpace(c.User)
	if c.Host == "" {
		c.Host = DefaultHost
	}
	if !strings.Contains(c.Host, ":") {
		c.Host += ":993"
	}
	if pw := strings.TrimSpace(c.Password); appPassword.MatchString(pw) {
		c.Password = strings.ReplaceAll(pw, " ", "")
	}
	return c
}

// Validate : serveur, adresse et mot de passe renseignés.
func (c Config) Validate() error {
	switch {
	case c.Host == "":
		return errors.New("serveur IMAP manquant")
	case c.User == "":
		return errors.New("adresse email manquante")
	case c.Password == "":
		return errors.New("mot de passe manquant")
	}
	return nil
}

// LoadConfig lit la configuration ; ok=false si elle n'existe pas encore.
func LoadConfig(path string) (Config, bool, error) {
	var c Config
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, false, nil
	}
	if err != nil {
		return c, false, fmt.Errorf("mail: %w", err)
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, false, fmt.Errorf("mail: %s illisible : %w", path, err)
	}
	return c, true, nil
}

// SaveConfig écrit la configuration, lisible par l'utilisateur seul (elle
// contient un mot de passe).
func SaveConfig(path string, c Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil { // un fichier existant garde son mode
		return err
	}
	return os.Rename(tmp, path)
}
