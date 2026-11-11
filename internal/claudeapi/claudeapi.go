// Package claudeapi est le port de Jarvis vers l'API d'Anthropic : une
// requête (consigne, outils, conversation), une réponse (texte, appels
// d'outils). La boucle d'outils, elle, appartient à internal/assistant.
//
// SDKClient l'implémente avec le SDK officiel ; FakeClient sert aux tests
// (aucun test ne demande de clé ni de réseau). Distinct d'internal/
// claudecode, qui pilote le CLI « claude » pour développer les tickets.
//
// Exception réseau n° 9 (docs/plan-assistant.md) : ce qui part, c'est la
// conversation et le résultat des outils que Claude a demandés.
package claudeapi

import (
	"context"
	"encoding/json"
	"errors"
)

// Tool : un outil proposé au modèle. Schema est un JSON Schema d'objet
// ({"type":"object","properties":…,"required":…}).
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
}

// Call : un appel d'outil demandé par le modèle.
type Call struct {
	ID    string          `bson:"id"`
	Name  string          `bson:"name"`
	Input json.RawMessage `bson:"input"`
}

// Result : la réponse de Jarvis à un Call.
type Result struct {
	CallID  string `bson:"call_id"`
	Content string `bson:"content"`
	IsError bool   `bson:"is_error"`
}

// Message : un tour de la conversation. Exactement un des trois champs
// est renseigné.
type Message struct {
	// User : texte de l'utilisateur.
	User string
	// Assistant : le message du modèle tel qu'il l'a rendu (Response.Raw),
	// rejoué sans y toucher — les blocs de réflexion qu'il contient ne
	// sont valides qu'inchangés.
	Assistant json.RawMessage
	// Results : les réponses aux appels d'outils du message précédent,
	// toutes dans le même tour.
	Results []Result
}

// Request : une requête au modèle.
type Request struct {
	System   string
	Tools    []Tool
	Messages []Message
}

// StopReason : pourquoi le modèle s'est arrêté.
type StopReason string

const (
	StopEnd       StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopRefusal   StopReason = "refusal"
	StopPause     StopReason = "pause_turn"
)

// Usage : les jetons d'une requête.
type Usage struct {
	Input      int `bson:"input"`
	Output     int `bson:"output"`
	CacheRead  int `bson:"cache_read"`
	CacheWrite int `bson:"cache_write"`
}

// Total : tout ce qui a été lu ou écrit par le modèle.
func (u Usage) Total() int { return u.Input + u.Output + u.CacheRead + u.CacheWrite }

// Add : la somme de deux usages.
func (u Usage) Add(o Usage) Usage {
	return Usage{u.Input + o.Input, u.Output + o.Output, u.CacheRead + o.CacheRead, u.CacheWrite + o.CacheWrite}
}

// Response : la réponse du modèle.
type Response struct {
	// Text : le texte visible (les blocs de texte, réunis).
	Text  string
	Calls []Call
	// Raw : le message entier, à rejouer tel quel (Message.Assistant).
	Raw  json.RawMessage
	Stop StopReason
	// Refusal : la catégorie d'un refus (Stop == StopRefusal), si connue.
	Refusal string
	Usage   Usage
	// Model : le modèle qui a répondu (un autre que celui demandé après un
	// repli côté serveur).
	Model string
}

var (
	// ErrRateLimited : trop de requêtes ou quota atteint (429).
	ErrRateLimited = errors.New("claudeapi: limite de débit atteinte")
	// ErrAuth : clé absente, invalide ou sans droit (401, 403).
	ErrAuth = errors.New("claudeapi: clé d'API refusée")
	// ErrOverloaded : l'API est indisponible ou surchargée (5xx).
	ErrOverloaded = errors.New("claudeapi: API indisponible")
)

// Client envoie une requête au modèle.
type Client interface {
	Complete(ctx context.Context, req Request) (Response, error)
}
