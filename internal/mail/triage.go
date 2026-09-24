package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/llm"
)

// DefaultTriagePrompt : les consignes de tri par défaut. Modifiables depuis
// l'interface (onglet Agents).
const DefaultTriagePrompt = `Tu tries la boîte de réception de l'utilisateur. Lis l'email ci-dessous et réponds en JSON :
- category : « a_traiter » si l'utilisateur doit agir (répondre, payer, signer, prendre rendez-vous, envoyer un document) ; « document » si l'email apporte surtout un document à garder (facture, relevé, contrat, billet) ; « information » pour une information personnelle ou professionnelle à lire, sans action ; « notification » pour un message automatique d'un service (connexion, livraison, confirmation) ; « newsletter » pour une lettre d'information ou une promotion.
- summary : une ou deux phrases en français, factuelles : qui écrit, quoi, avec les dates et montants importants.
- action : si une action est attendue, la tâche à inscrire dans la todo, courte, commençant par un verbe (« Payer la facture Acme de 120 € ») ; sinon une chaîne vide.
N'invente rien qui ne soit pas dans l'email.`

// Triager fait trier et résumer un email par le modèle local.
type Triager struct {
	LLM   llm.Client
	Model string // nom du modèle, gardé avec le tri
	// Prompt : les consignes en vigueur (agents.Registry) ; vide :
	// DefaultTriagePrompt.
	Prompt func() string
	Now    func() time.Time
}

// maxTriageText : le corps envoyé au modèle, au plus (le contexte du
// modèle de documents est de 8k jetons ; le début d'un email suffit).
const maxTriageText = 5000

var triageSchema = func() json.RawMessage {
	cats := make([]string, len(Categories))
	for i, c := range Categories {
		cats[i] = string(c)
	}
	b, _ := json.Marshal(map[string]any{
		"type":     "object",
		"required": []string{"category", "summary", "action"},
		"properties": map[string]any{
			"category": map[string]any{"type": "string", "enum": cats},
			"summary":  map[string]any{"type": "string"},
			"action":   map[string]any{"type": "string"},
		},
	})
	return b
}()

// Triage trie un email.
func (t Triager) Triage(ctx context.Context, m Mail) (Triage, error) {
	prompt := DefaultTriagePrompt
	if t.Prompt != nil {
		if p := strings.TrimSpace(t.Prompt()); p != "" {
			prompt = p
		}
	}
	res, err := t.LLM.Extract(ctx, llm.ExtractRequest{Page: 1, Text: triageInput(m), Schema: triageSchema, Prompt: prompt})
	if err != nil {
		return Triage{}, fmt.Errorf("mail: tri : %w", err)
	}
	var out struct {
		Category Category `json:"category"`
		Summary  string   `json:"summary"`
		Action   string   `json:"action"`
	}
	if err := json.Unmarshal(res.JSON, &out); err != nil {
		return Triage{}, fmt.Errorf("mail: %w (%s)", errBadReply, res.JSON)
	}
	known := false
	for _, c := range Categories {
		known = known || out.Category == c
	}
	if !known {
		return Triage{}, fmt.Errorf("mail: %w (catégorie inconnue %q)", errBadReply, out.Category)
	}
	now := time.Now
	if t.Now != nil {
		now = t.Now
	}
	return Triage{
		Category: out.Category, Summary: strings.TrimSpace(out.Summary), Action: strings.TrimSpace(out.Action),
		Model: t.Model, Prompt: prompt, At: now(),
	}, nil
}

// triageInput : ce que le modèle lit d'un email.
func triageInput(m Mail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "De : %s\nObjet : %s\n", m.From, m.Subject)
	if !m.Date.IsZero() {
		fmt.Fprintf(&b, "Date : %s\n", m.Date.Local().Format("02/01/2006 15:04"))
	}
	if len(m.Attachments) > 0 {
		names := make([]string, len(m.Attachments))
		for i, a := range m.Attachments {
			names[i] = a.Filename
		}
		fmt.Fprintf(&b, "Pièces jointes : %s\n", strings.Join(names, ", "))
	}
	b.WriteString("\n" + truncate(m.Text, maxTriageText))
	return b.String()
}
