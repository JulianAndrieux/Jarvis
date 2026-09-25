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
- question : si l'expéditeur attend une réponse écrite de l'utilisateur, ce qu'il lui demande, en une phrase courte commençant par un verbe (« Confirmer sa présence à la réunion de jeudi ») ; sinon une chaîne vide.
- reply : true si l'utilisateur doit répondre à cet email, false sinon.
Une réponse est attendue quand une personne s'adresse à l'utilisateur et attend qu'il lui écrive en retour : une question qui lui est posée, une demande de confirmation, d'avis, de rendez-vous, d'information ou de document, une invitation qui demande une réponse, un message personnel qui appelle une réponse.
Aucune réponse n'est attendue pour : les newsletters et promotions, les messages automatiques d'un service (commande, livraison, connexion, relevé, facture envoyée automatiquement), un email où l'utilisateur est seulement en copie sans qu'on s'adresse à lui, une simple information ou un remerciement qui ne demande rien. Payer une facture ou signer un document en ligne est une action, pas une réponse.
Dans le doute, pour un email écrit par une personne directement à l'utilisateur, réponds reply = true.
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
		"required": []string{"category", "summary", "action", "question", "reply"},
		// Champs dans l'ordre de génération (clés triées) : question avant
		// reply, le modèle formule la demande avant de trancher.
		"properties": map[string]any{
			"category": map[string]any{"type": "string", "enum": cats},
			"summary":  map[string]any{"type": "string"},
			"action":   map[string]any{"type": "string"},
			"question": map[string]any{"type": "string"},
			"reply":    map[string]any{"type": "boolean"},
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
		Question string   `json:"question"`
		Reply    bool     `json:"reply"`
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
	question := strings.TrimSpace(out.Question)
	if !out.Reply || automatedSender(m.From.Email) {
		out.Reply, question = false, ""
	}
	return Triage{
		Category: out.Category, Summary: strings.TrimSpace(out.Summary), Action: strings.TrimSpace(out.Action),
		Reply: out.Reply, Question: question,
		Model: t.Model, Prompt: prompt, At: now(), Version: TriageVersion,
	}, nil
}

// automatedMarkers : ce que contient la partie locale d'une adresse qui
// n'attend jamais de réponse (sans tirets ni points : « no-reply »,
// « ne.pas.repondre »…).
var automatedMarkers = []string{"noreply", "donotreply", "nepasrepondre", "notification", "mailerdaemon"}

// automatedSender : l'adresse est celle d'un envoi automatique — aucune
// réponse attendue, quoi qu'en dise le modèle.
func automatedSender(email string) bool {
	local, _, _ := strings.Cut(strings.ToLower(email), "@")
	local = strings.NewReplacer("-", "", "_", "", ".", "").Replace(local)
	for _, marker := range automatedMarkers {
		if strings.Contains(local, marker) {
			return true
		}
	}
	return false
}

// triageInput : ce que le modèle lit d'un email.
func triageInput(m Mail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "De : %s\n", m.From)
	if len(m.To) > 0 {
		fmt.Fprintf(&b, "À : %s\n", joinAddresses(m.To))
	}
	if len(m.Cc) > 0 {
		fmt.Fprintf(&b, "Cc : %s\n", joinAddresses(m.Cc))
	}
	if m.Account != "" {
		fmt.Fprintf(&b, "L'utilisateur (%s) %s\n", m.Account, recipientRole(m))
	}
	fmt.Fprintf(&b, "Objet : %s\n", m.Subject)
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

func joinAddresses(list []Address) string {
	parts := make([]string, len(list))
	for i, a := range list {
		parts[i] = a.String()
	}
	return strings.Join(parts, ", ")
}

// recipientRole : la place de l'utilisateur parmi les destinataires, dite
// au modèle (seulement en copie : rarement une réponse attendue).
func recipientRole(m Mail) string {
	has := func(list []Address) bool {
		for _, a := range list {
			if strings.EqualFold(strings.TrimSpace(a.Email), m.Account) {
				return true
			}
		}
		return false
	}
	switch {
	case has(m.To):
		return "est destinataire direct."
	case has(m.Cc):
		return "est seulement en copie."
	}
	return "n'apparaît pas parmi les destinataires (liste de diffusion ou copie cachée)."
}
