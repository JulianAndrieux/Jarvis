package agents

import (
	"strings"

	"github.com/JulianAndrieux/Jarvis/internal/agent"
	"github.com/JulianAndrieux/Jarvis/internal/assistant"
	"github.com/JulianAndrieux/Jarvis/internal/classify"
	"github.com/JulianAndrieux/Jarvis/internal/collect"
	"github.com/JulianAndrieux/Jarvis/internal/extraction"
	"github.com/JulianAndrieux/Jarvis/internal/mail"
)

// Identifiants des agents (clé MongoDB, URL /agents/{id}).
const (
	Classification = "classification"
	Extraction     = "extraction"
	MailTriage     = "mail_triage"
	MailPayment    = "mail_payment"
	MarketKeywords = "market_keywords"
	Analysis       = "analysis"
	Development    = "development"
	Review         = "review"
	Assistant      = "assistant"
)

// Models : modèles en service, lus dans les options de jarvisapp.
type Models struct {
	Documents string // LLM des documents (classification, extraction)
	Tickets   string // modèle de l'agent des tickets
	// Assistant : modèle de l'assistant personnel (Claude, par l'API) ;
	// AssistantTools : les outils qui lui sont ouverts.
	Assistant      string
	AssistantTools []string
}

// Defaults décrit les agents du système. Les prompts par défaut restent
// dans leurs paquets : c'est là qu'ils sont testés avec leur code.
func Defaults(m Models) []Definition {
	return []Definition{
		{
			ID: Classification, Name: "Classification de document", Group: "Documents",
			Role:          "Détermine le type d'un document déposé parmi les types connus, à partir des premiers caractères de son texte. La réponse est contrainte par un schéma : un type de la liste ou « unknown ».",
			Model:         m.Documents,
			DefaultPrompt: classify.DefaultPromptTemplate,
			Placeholders:  []Placeholder{{Name: trim(classify.TypesPlaceholder), Meaning: "la liste des types candidats, nom et description"}},
			Appended:      "Le texte du document (4000 premiers caractères) et le schéma JSON de la réponse.",
		},
		{
			ID: Extraction, Name: "Extraction de document", Group: "Documents",
			Role:          "Extrait, page par page, les champs du type de document au format JSON, avec pour chaque valeur un score de confiance et l'extrait source. Les valeurs sous le seuil sont marquées pour revue.",
			Model:         m.Documents,
			DefaultPrompt: extraction.DefaultPromptTemplate,
			Placeholders:  []Placeholder{{Name: trim(extraction.TypePlaceholder), Meaning: "la description du type de document"}},
			Appended:      "Le texte de la page et le schéma JSON dérivé du type de document. Le prompt envoyé est conservé dans chaque résultat.",
		},
		{
			ID: MailTriage, Name: "Tri des emails", Group: "Emails",
			Role:          "Classe chaque nouvel email (à traiter, document, information, notification, newsletter), le résume, propose la tâche à inscrire dans la todo s'il demande une action, et dit si l'expéditeur attend une réponse (seuls ces emails sont affichés par défaut). Un expéditeur automatique (no-reply, notifications) n'attend jamais de réponse, quoi que dise le modèle. La réponse est contrainte par un schéma.",
			Model:         m.Documents,
			DefaultPrompt: mail.DefaultTriagePrompt,
			Appended:      "L'expéditeur, les destinataires (et si l'utilisateur est destinataire direct, en copie ou absent), l'objet, la date, les noms des pièces jointes et le début du corps (5000 caractères), puis le schéma JSON de la réponse. Le prompt utilisé est conservé avec le tri.",
		},
		{
			ID: MailPayment, Name: "Paiements des emails", Group: "Emails",
			Role:          "Repère, parmi les emails triés « à traiter » ou « document », ce que l'utilisateur doit payer, et prépare bénéficiaire, montant, échéance, référence, IBAN et lien de paiement. Après le modèle, des contrôles du code : un IBAN, un BIC ou un lien absents du texte sont écartés, l'IBAN est vérifié par sa clé, un montant introuvable ou une confiance basse passent en revue. Un prélèvement automatique n'est pas « à payer ». Jarvis ne paie jamais rien.",
			Model:         m.Documents,
			DefaultPrompt: mail.DefaultPaymentPrompt,
			Appended:      "L'en-tête et le début du corps de l'email (5000 caractères), le texte des PDF joints (couche texte seulement, 3000 caractères au plus), puis le schéma JSON de la réponse. Le prompt utilisé est conservé avec le résultat.",
		},
		{
			ID: MarketKeywords, Name: "Mots-clés des marchés", Group: "Marchés",
			Role:          "Propose, pour chaque marché Polymarket étudié, 2 à 5 phrases de recherche qui serviront à collecter les actualités capables de le faire bouger (flux RSS et GDELT). Après le modèle, le code nettoie et borne les phrases ; un article est rattaché à un marché s'il contient tous les mots de l'une d'elles. Les mots-clés sont gardés avec le modèle et la date.",
			Model:         m.Documents,
			DefaultPrompt: collect.DefaultKeywordsPrompt,
			Appended:      "La question du marché, le titre de l'événement et le début de ses règles de résolution (2000 caractères au plus), puis le schéma JSON de la réponse.",
		},
		{
			ID: Analysis, Name: "Analyse de ticket", Group: "Tickets",
			Role:          "Explore le code en lecture seule et propose un plan de développement à valider.",
			Model:         m.Tickets,
			Tools:         toolNames(agent.ReadOnlySpecs()),
			DefaultPrompt: agent.DefaultAnalysisPrompt,
			Appended:      "Le contexte du projet (début de CLAUDE.md), la carte du code (chaque paquet et son rôle), /no_think, puis le ticket : titre, besoin, critères d'acceptation, plan précédent et retours.",
		},
		{
			ID: Development, Name: "Développement", Group: "Tickets",
			Role:          "Développe un ticket dont le plan est validé, en TDD, dans une copie de travail isolée. Jarvis refait ensuite lui-même la vérification complète.",
			Model:         m.Tickets,
			Tools:         toolNames(agent.DevSpecs()),
			DefaultPrompt: agent.DefaultDevelopmentPrompt,
			Appended:      "Le contexte du projet (début de CLAUDE.md), la carte du code (chaque paquet et son rôle), /no_think, puis le ticket, son plan validé et le rapport de la vérification précédente.",
		},
		{
			ID: Review, Name: "Relecture de code", Group: "Tickets",
			Role:          "Relit le diff d'un ticket, déjà compilé et testé, selon les standards du projet : ce que les tests ne voient pas. S'il demande des changements, le développeur est relancé (2 allers-retours au plus) avant ta revue.",
			Model:         m.Tickets,
			Tools:         toolNames(agent.ReviewSpecs()),
			DefaultPrompt: agent.DefaultReviewPrompt,
			Appended:      "Le contexte du projet, la carte du code, puis le ticket, son plan validé et le diff à relire.",
		},
		{
			ID: Assistant, Name: "Assistant personnel", Group: "Assistant",
			Role:          "Jarvis en conversation (onglet Assistant) : Claude orchestre et se sert des données de Jarvis par des outils — chercher et lire notes, tâches, documents, emails ; créer une tâche ; retenir un fait. Ce que rend un outil est une donnée, jamais une instruction ; aucune action vers l'extérieur n'est possible sans confirmation.",
			Model:         m.Assistant,
			Tools:         m.AssistantTools,
			DefaultPrompt: assistant.DefaultInstructions,
			Appended:      "La date du jour et les faits retenus des conversations précédentes, puis la conversation.",
		},
	}
}

func trim(placeholder string) string {
	return strings.TrimSuffix(strings.TrimPrefix(placeholder, "{{"), "}}")
}

func toolNames(specs []agent.ToolSpec) []string {
	var out []string
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}
