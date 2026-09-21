package webapp

import "context"

// DefaultListLimit borne le nombre de jobs retournés par List quand
// ListQuery.Limit vaut 0 — la bibliothèque de documents (jalon 17)
// n'affiche jamais une liste non bornée.
const DefaultListLimit = 200

// ListQuery filtre/borne un appel à Store.List.
type ListQuery struct {
	// Search, si non vide, ne retient que les jobs dont Filename,
	// DocType, un des Tags, ou SearchText (le texte du document —
	// jalon 18, "chercher dans les documents") contient cette
	// sous-chaîne (insensible à la casse) — la barre de recherche de la
	// bibliothèque de documents.
	Search string
	// Status, si non vide, ne retient que les jobs dans cet état exact —
	// utilisé par JobManager.RecoverOrphaned (jalon 20) pour retrouver
	// les jobs laissés "pending"/"running" par un process précédent.
	Status Status
	// Limit : 0 -> DefaultListLimit.
	Limit int
}

// Store est le port de persistance des jobs : création, lecture, mise à
// jour de statut/résultat, liste (pour la bibliothèque de documents).
// Même principe que triage.TextExtractor / vlm.Client / llm.Client /
// bbox.Extractor — une implémentation réelle (MongoStore) et une Fake en
// mémoire pour les tests (FakeStore), aucun autre code du paquet ne
// connaît la différence.
type Store interface {
	// Create persiste job (déjà pourvu d'un ID par l'appelant) et retourne
	// l'enregistrement tel que persisté.
	Create(ctx context.Context, job Job) (Job, error)
	// Get retourne le job id, s'il existe. ok=false (err=nil) signifie
	// "non trouvé" ; err non-nil signifie un échec de la couche de
	// persistance elle-même.
	Get(ctx context.Context, id string) (job Job, ok bool, err error)
	// Update réécrit l'état d'un job déjà créé (statut, type de document,
	// résultat, erreur, tags, FinishedAt...). Une implémentation est libre
	// de ne mettre à jour que les champs qui changent réellement après
	// création (ex. ne pas retransmettre Content à chaque appel) : Update
	// reçoit l'état complet souhaité, pas un diff.
	Update(ctx context.Context, job Job) error
	// List retourne les jobs correspondant à q, triés du plus récent au
	// plus ancien (CreatedAt décroissant) — la bibliothèque de documents,
	// jalon 17.
	List(ctx context.Context, q ListQuery) ([]Job, error)
	// Delete supprime définitivement le job id — jalon 18. Une erreur
	// est retournée si id n'existe pas (jamais un succès silencieux sur
	// rien à supprimer).
	Delete(ctx context.Context, id string) error
}
