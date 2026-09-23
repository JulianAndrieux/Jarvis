package templates

// SideItem : un traitement en cours ou une erreur, dans la barre latérale.
type SideItem struct {
	Href   string
	Icon   string
	Title  string
	Detail string
	// Full : le texte complet (erreur), affiché au survol.
	Full string
}

// SidebarView : la barre latérale de l'application (jalon 34).
type SidebarView struct {
	Week     []TaskView // tâches en retard ou à échéance dans les 7 jours
	WeekMore int        // tâches de la semaine non affichées
	Running  []SideItem
	Errors   []SideItem
}
