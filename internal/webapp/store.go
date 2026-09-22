package webapp

import (
	"context"

	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
)

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
	// SummaryOnly, si vrai, autorise le Store à omettre les champs lourds
	// (Content, Result, Thumbnail, Progress) des jobs retournés — la grille de la
	// bibliothèque de documents (jalon 22) n'affiche que des métadonnées,
	// et charger jusqu'à DefaultListLimit PDF complets pour ça était du
	// gaspillage. Un job ainsi chargé ne doit jamais être repassé tel quel
	// à Update (il écraserait Result par nil).
	SummaryOnly bool
	// Format, si non vide, ne retient que les fichiers de cette famille
	// ("pdf", "sheet"... cf. internal/formats) — filtre par type de la
	// bibliothèque (jalon 25). Un job antérieur au jalon 25, sans format
	// enregistré, est un PDF.
	Format string
}

// Store est le port de persistance des jobs : création, lecture, mise à
// jour de statut/résultat, liste (pour la bibliothèque de documents).
// Même principe que triage.TextExtractor / vlm.Client / llm.Client /
// bbox.Extractor — une implémentation réelle (MongoStore) et une Fake en
// mémoire pour les tests (FakeStore), aucun autre code du paquet ne
// connaît la différence.
type Store interface {
	// Create persiste job (déjà pourvu d'un ID par l'appelant) et retourne
	// l'enregistrement tel que persisté. job.Content est écrit comme
	// FileOriginal ; Get ne le recharge jamais (voir ReadFile).
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
	// SetThumbnail enregistre la miniature (PNG) de la première page du
	// job id — jalon 22. Écriture ciblée, distincte d'Update : Update
	// reçoit un Job complet, qui peut venir d'une liste SummaryOnly sans
	// miniature. Erreur si id n'existe pas.
	SetThumbnail(ctx context.Context, id string, png []byte) error
	// SetProgress enregistre l'avancement du traitement en cours du job
	// id (texte déjà lu, étape, compteurs) — jalon 23. nil l'efface.
	// Écriture ciblée, comme SetThumbnail : Update ne touche jamais à
	// l'avancement, pour qu'un Job en mémoire (qui ne le porte pas) ne
	// l'efface pas en terminant. Erreur si id n'existe pas.
	SetProgress(ctx context.Context, id string, progress *pipeline.Progress) error

	// WriteFile enregistre (ou remplace) le fichier name du job id —
	// jalon 25 : les fichiers vivent à part des métadonnées (GridFS côté
	// MongoStore), sans limite de 16 Mo ni rechargement à chaque lecture
	// du job. Erreur si id n'existe pas.
	WriteFile(ctx context.Context, id string, name FileName, data []byte) error
	// ReadFile lit le fichier name du job id ; ok=false (err=nil) s'il
	// n'existe pas.
	ReadFile(ctx context.Context, id string, name FileName) (data []byte, ok bool, err error)
}

// FileName désigne un des fichiers rattachés à un job.
type FileName string

const (
	// FileOriginal est le fichier tel que déposé — écrit par Create à
	// partir de Job.Content.
	FileOriginal FileName = "original"
	// FileRendition est la version PDF d'un fichier non-PDF (aperçu,
	// miniature, entrée du pipeline).
	FileRendition FileName = "rendition.pdf"
	// FilePreview est l'aperçu natif éventuel (feuilles de calcul en
	// HTML, image HEIC/TIFF en JPEG).
	FilePreview FileName = "preview"
)
