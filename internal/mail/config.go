package mail

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JulianAndrieux/Jarvis/internal/secretbox"
)

// DefaultHost : IMAP de Gmail (TLS).
const DefaultHost = "imap.gmail.com:993"

// Config : la boîte à relever. Le mot de passe (d'application, pour
// Gmail) reste sur la machine, dans un fichier lisible par l'utilisateur
// seul — jamais dans Atlas.
type Config struct {
	Host string `json:"host"`
	User string `json:"user"`
	// Password n'est jamais sérialisé : c'est SealedPassword qui est écrit,
	// chiffré avec la clé de la machine (internal/secretbox). Un fichier de
	// configuration, une sauvegarde ou un dump ne livre donc plus le mot de
	// passe de la boîte.
	Password string `json:"-"`
	// SealedPassword : le mot de passe chiffré.
	SealedPassword string `json:"sealed_password,omitempty"`
	// LegacyPassword : l'ancien champ en clair, lu une fois pour migrer, et
	// jamais réécrit.
	LegacyPassword string `json:"password,omitempty"`
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
	return LoadConfigKey(path, secretbox.Key{})
}

// LoadConfigKey lit la configuration et déchiffre le mot de passe avec key.
// Une clé nulle ne déchiffre rien : seule une configuration antérieure au
// chiffrement reste alors lisible.
func LoadConfigKey(path string, key secretbox.Key) (Config, bool, error) {
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
	switch {
	case c.SealedPassword != "":
		if key == (secretbox.Key{}) {
			return c, true, fmt.Errorf("mail: %s : mot de passe chiffré, mais aucune clé fournie", path)
		}
		clear, err := secretbox.Open(key, c.SealedPassword)
		if err != nil {
			return c, true, fmt.Errorf("mail: %s : %w", path, err)
		}
		c.Password = string(clear)
	case c.LegacyPassword != "":
		// Configuration d'avant le chiffrement : lue telle quelle une fois.
		// Le prochain enregistrement la réécrira chiffrée.
		c.Password = c.LegacyPassword
	}
	return c, true, nil
}

// SaveConfig écrit la configuration, lisible par l'utilisateur seul (elle
// contient un mot de passe).
func SaveConfig(path string, c Config) error {
	return SaveConfigKey(path, c, secretbox.Key{})
}

// SaveConfigKey écrit la configuration avec le mot de passe chiffré par
// key. Sans clé, l'écriture est refusée plutôt que de reposer un mot de
// passe en clair sur le disque.
func SaveConfigKey(path string, c Config, key secretbox.Key) error {
	if c.Password != "" {
		if key == (secretbox.Key{}) {
			return fmt.Errorf("mail: aucune clé de chiffrement : refus d'écrire un mot de passe en clair")
		}
		sealed, err := secretbox.Seal(key, []byte(c.Password))
		if err != nil {
			return err
		}
		c.SealedPassword = sealed
	}
	c.LegacyPassword = "" // l'ancien champ en clair n'est jamais réécrit
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
