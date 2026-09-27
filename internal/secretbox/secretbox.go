// Package secretbox chiffre de petits secrets avec une clé qui ne quitte
// pas la machine.
//
// À quoi ça sert ici : le mot de passe d'une boîte mail (et, à terme, tout
// identifiant d'un service tiers) doit pouvoir être enregistré sans être
// lisible par qui accède au stockage — un dump de base, une sauvegarde, un
// fournisseur cloud. La clé vit dans un fichier local en 0600 ; le texte
// chiffré, lui, peut voyager. La contrainte de CLAUDE.md devient donc
// vérifiable : « le mot de passe n'est jamais enregistré en clair hors de
// l'hôte ».
//
// AES-256-GCM : chiffre et authentifie à la fois, donc un texte altéré est
// refusé au lieu de rendre n'importe quoi. Nonce aléatoire par message,
// préfixé au texte chiffré. Rien d'exotique, tout dans la bibliothèque
// standard.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// KeySize : AES-256.
const KeySize = 32

// Key est la clé de cette machine.
type Key [KeySize]byte

// LoadOrCreateKey lit la clé, ou en crée une la première fois. Le fichier
// est en 0600 et son dossier créé au besoin.
//
// Une clé de mauvaise taille est une erreur : mieux vaut refuser de
// démarrer que chiffrer avec autre chose que ce qu'on croit.
func LoadOrCreateKey(path string) (Key, error) {
	var k Key
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(b) != KeySize {
			return k, fmt.Errorf("secretbox: %s: clé de %d octets, %d attendus", path, len(b), KeySize)
		}
		copy(k[:], b)
		return k, nil
	case errors.Is(err, fs.ErrNotExist):
		if _, err := rand.Read(k[:]); err != nil {
			return k, fmt.Errorf("secretbox: génération de la clé: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return k, fmt.Errorf("secretbox: %w", err)
		}
		if err := os.WriteFile(path, k[:], 0o600); err != nil {
			return k, fmt.Errorf("secretbox: écriture de la clé: %w", err)
		}
		return k, nil
	default:
		return k, fmt.Errorf("secretbox: lecture de %s: %w", path, err)
	}
}

func aead(k Key) (cipher.AEAD, error) {
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, fmt.Errorf("secretbox: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secretbox: %w", err)
	}
	return gcm, nil
}

// Seal chiffre plaintext. Le résultat est du base64, utilisable tel quel
// dans un document JSON ou BSON.
func Seal(k Key, plaintext []byte) (string, error) {
	gcm, err := aead(k)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secretbox: nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Open déchiffre. Une clé différente, ou un texte altéré, rend une erreur —
// jamais un contenu approximatif.
func Open(k Key, sealed string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, fmt.Errorf("secretbox: texte chiffré illisible: %w", err)
	}
	gcm, err := aead(k)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("secretbox: texte chiffré tronqué")
	}
	nonce, body := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	out, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, fmt.Errorf("secretbox: déchiffrement refusé (mauvaise clé ou contenu altéré): %w", err)
	}
	return out, nil
}
