package templates

// Jalon 57 : l'onglet Assistant. Le texte de l'assistant est rendu en
// Markdown échappé (c'est la sortie d'un modèle) ; les entrées et sorties
// d'outils sont affichées comme du texte.

// AssistantView : la page de l'assistant.
type AssistantView struct {
	// Disabled : pourquoi l'assistant n'est pas en service ("" : il l'est).
	Disabled      string
	Model         string
	Sources       []string
	Usage         string
	Conversations []ConversationRow
	Facts         []FactRow
	// Thread : la conversation ouverte (nil : nouvelle conversation).
	Thread *ThreadView
	Error  string
	// Draft : le message refusé, rendu à l'utilisateur.
	Draft string
}

type ConversationRow struct {
	ID      string
	Title   string
	When    string
	Running bool
	Failed  bool
	// Waiting : une action attend la réponse de l'utilisateur.
	Waiting bool
	Active  bool
}

type FactRow struct {
	ID   string
	Text string
}

// ThreadView : le fil d'une conversation.
type ThreadView struct {
	ID       string
	Title    string
	Running  bool
	Error    string
	Messages []MessageView
	// Notice : message d'une action refusée (envoi pendant un tour).
	Notice string
	Draft  string
	// Pending : les actions qui attendent une confirmation ; non vide, la
	// saisie libre est remplacée par la question.
	Pending []PendingView
}

// PendingView : une action à confirmer ou refuser.
type PendingView struct {
	CallID  string
	Summary string
	Tool    string
	Input   string
}

// MessageView : une bulle du fil.
type MessageView struct {
	FromUser bool
	Text     string // utilisateur : texte brut
	HTML     string // assistant : Markdown rendu, échappé
	When     string
	Steps    []StepView
}

// StepView : un outil appelé par l'assistant, et ce qu'il a rendu.
type StepView struct {
	Tool    string
	Label   string
	Input   string
	Output  string
	IsError bool
	Pending bool
}
