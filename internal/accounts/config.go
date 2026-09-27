package accounts

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// Config est le fournisseur d'identité de cette instance, lu depuis un
// fichier local (0600) et jamais depuis une option de ligne de commande :
// un secret passé en argument est lisible par tout utilisateur de la
// machine (ps), comme MONGO_URI l'a déjà appris à ce projet.
//
// Tant que ce fichier n'existe pas, l'authentification reste désactivée et
// l'application se comporte comme avant : un seul environnement, aucun
// écran de connexion. C'est ce qui permet de continuer à la lancer depuis
// le Dock sans rien préparer.
type Config struct {
	// ClientID/ClientSecret : le client OAuth Google. Type « application de
	// bureau » ou « web » avec http://127.0.0.1:<port>/auth/callback en URI
	// de redirection — Google autorise http sur l'adresse de boucle locale,
	// nulle part ailleurs.
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	// OwnerEmail : le propriétaire de l'instance, le seul compte qui entre
	// sans invitation. Jamais deviné.
	OwnerEmail string `json:"owner_email"`
}

// String : pour les journaux, sans le secret.
func (c Config) String() string {
	id := c.ClientID
	if len(id) > 12 {
		id = id[:12] + "…"
	}
	return fmt.Sprintf("client %s, propriétaire %s", id, c.OwnerEmail)
}

func (c Config) Normalize() Config {
	c.ClientID = strings.TrimSpace(c.ClientID)
	c.ClientSecret = strings.TrimSpace(c.ClientSecret)
	c.OwnerEmail = strings.TrimSpace(c.OwnerEmail)
	return c
}

// Validate : un propriétaire est obligatoire (sinon personne ne peut
// entrer la première fois) ; le client Google est optionnel — sans lui, il
// reste le lien de secours local.
func (c Config) Validate() error {
	if c.OwnerEmail == "" {
		return errors.New("accounts: owner_email est requis (le propriétaire de l'instance)")
	}
	if !strings.Contains(c.OwnerEmail, "@") {
		return fmt.Errorf("accounts: owner_email %q n'est pas une adresse", c.OwnerEmail)
	}
	if (c.ClientID == "") != (c.ClientSecret == "") {
		return errors.New("accounts: client_id et client_secret vont ensemble")
	}
	return nil
}

// LoadConfig lit la configuration. ok=false (sans erreur) signifie « pas
// de fichier » : l'authentification reste désactivée, ce n'est pas une
// panne.
func LoadConfig(path string) (Config, bool, error) {
	if path == "" {
		return Config{}, false, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("accounts: lecture de %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, false, fmt.Errorf("accounts: %s illisible: %w", path, err)
	}
	c = c.Normalize()
	if err := c.Validate(); err != nil {
		return Config{}, false, fmt.Errorf("accounts: %s: %w", path, err)
	}
	return c, true, nil
}

// InsecurePermissions dit si le fichier est lisible par d'autres que son
// propriétaire. Un secret de client OAuth en 0644 est un avertissement à
// donner, pas un refus de démarrer (le fichier a pu être créé à la main).
func InsecurePermissions(path string) (bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return st.Mode().Perm()&0o077 != 0, nil
}
