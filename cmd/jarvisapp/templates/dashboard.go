package templates

// StatCard : une statistique du tableau de bord, cliquable vers sa
// section.
type StatCard struct {
	Icon   string
	Label  string
	Value  string
	Detail string
	Href   string
	// Alert : valeur mise en avant (retards, échecs, réponses attendues).
	Alert bool
}

// DashboardView : la page d'accueil de l'application (ticket "Revoir
// ordre des sections") — une carte par section, puis les derniers
// documents importés.
type DashboardView struct {
	Cards  []StatCard
	Recent []DocumentRow
	// RecentErr : la liste des derniers documents n'a pas pu être lue —
	// affiché tel quel, jamais confondu avec une bibliothèque vide.
	RecentErr bool
}
