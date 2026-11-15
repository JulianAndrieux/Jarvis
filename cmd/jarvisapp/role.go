package main

import "fmt"

// role : ce que cette instance fait en arrière-plan. Une seule instance
// (« full », le défaut) fait tout. Quand Jarvis est hébergé, deux
// instances partagent la même base : « cloud » (le serveur, toujours
// allumé : documents, emails, marchés, notes — l'inférence restant servie
// par le Mac) et « worker » (le Mac : seulement le cycle des tickets, qui
// demande le dépôt git, les agents et de quoi se redéployer). Chaque
// travail de fond appartient à exactement une des deux.
type role struct {
	Name string
	// TicketWork : analyse, développement, déploiement, relève et reprise
	// des tickets. Faux : tickets en lecture et création seulement.
	TicketWork bool
	// RecoverJobs : au démarrage, les documents restés en cours sont
	// marqués interrompus — vrai seulement pour l'instance qui les traite.
	RecoverJobs bool
	// Mail : relève et tri des emails.
	Mail bool
	// Collect : collecte des prix et des actualités des marchés.
	Collect bool
	// Watch : dossier surveillé.
	Watch bool
	// WaitModels : les modèles sont servis par une autre machine, qui peut
	// être éteinte — un document attend leur retour au lieu d'échouer.
	WaitModels bool
}

func roleSettings(name string) (role, error) {
	switch name {
	case "", "full":
		return role{Name: "full", TicketWork: true, RecoverJobs: true, Mail: true, Collect: true, Watch: true}, nil
	case "cloud":
		return role{Name: "cloud", RecoverJobs: true, Mail: true, Collect: true, Watch: true, WaitModels: true}, nil
	case "worker":
		return role{Name: "worker", TicketWork: true}, nil
	}
	return role{}, fmt.Errorf("--role %q inconnu (full, cloud ou worker)", name)
}
