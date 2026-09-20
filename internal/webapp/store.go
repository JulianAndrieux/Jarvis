package webapp

import "context"

// Store est le port de persistance des jobs : création, lecture, mise à
// jour de statut/résultat. Même principe que triage.TextExtractor /
// vlm.Client / llm.Client / bbox.Extractor — une implémentation réelle
// (MongoStore) et une Fake en mémoire pour les tests (FakeStore), aucun
// autre code du paquet ne connaît la différence.
type Store interface {
	// Create persiste job (déjà pourvu d'un ID par l'appelant) et retourne
	// l'enregistrement tel que persisté.
	Create(ctx context.Context, job Job) (Job, error)
	// Get retourne le job id, s'il existe. ok=false (err=nil) signifie
	// "non trouvé" ; err non-nil signifie un échec de la couche de
	// persistance elle-même.
	Get(ctx context.Context, id string) (job Job, ok bool, err error)
	// Update réécrit l'état d'un job déjà créé (statut, résultat, erreur,
	// FinishedAt...). Une implémentation est libre de ne mettre à jour que
	// les champs qui changent réellement après création (ex. ne pas
	// retransmettre Content à chaque appel) : Update reçoit l'état complet
	// souhaité, pas un diff.
	Update(ctx context.Context, job Job) error
}
