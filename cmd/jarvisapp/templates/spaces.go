package templates

// Jalon 33 : deux espaces. adminPages : les onglets de l'Admin (thème
// sombre, sous /admin) ; toutes les autres pages sont l'application.
var adminPages = map[string]bool{"agents": true, "architecture": true, "classes": true, "model": true, "tests": true}

func isAdmin(active string) bool { return adminPages[active] }

func themeClass(active string) string {
	if isAdmin(active) {
		return "theme-admin"
	}
	return "theme-user"
}
