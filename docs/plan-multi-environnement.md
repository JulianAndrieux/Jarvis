# Plan : environnements, users, sessions, changements traçables

Document de travail, **rien n'est décidé ici** : il prépare la décision.
Écrit à la demande de l'utilisateur ("prépare un énorme refactoring […]
réfléchis à un plan pour mettre en œuvre ça et après on décidera comment
faire"). CLAUDE.md n'est pas modifié : il décrit l'état réel du projet,
pas une intention.

---

## 1. Ce que je comprends de la demande

> L'environnement en local actuel c'est mon environnement. L'idée c'est
> d'avoir plusieurs environnements. Chaque environnement aura un ou
> plusieurs users. À chaque fois qu'un user se connecte il aura une
> session. La session doit pouvoir gérer et tracker les changements
> effectués par le user. Il faudra pouvoir commiter un changement sans
> créer de conflits.

Quatre notions à introduire, aucune n'existe aujourd'hui dans le code :

| Notion | Ce que j'en comprends |
|---|---|
| **Environnement** | Un espace de travail cloisonné : ses documents, ses emails, ses notes, ses tâches, ses tickets. L'installation actuelle sur le Mac devient l'environnement `local` de Julian. |
| **User** | Une personne qui appartient à un ou plusieurs environnements, avec un rôle (au minimum : membre / administrateur). |
| **Session** | Ce qu'un user obtient en se connectant : son identité, son environnement courant, et **le fil de ce qu'il a changé**. |
| **Changement** | Une modification de données attribuée à (user, session, instant), traçable, et **commitable sans conflit**. |

**Trois ambiguïtés que je ne tranche pas seul** — elles changent la taille
du chantier d'un facteur 3, elles sont reprises en §3 avec une
recommandation chacune :

1. **« Environnement »** = locataire logique dans un seul processus, ou
   déploiement isolé (un processus + une base par environnement) ?
2. **« Les changements effectués par le user »** = les **données** de
   l'application (un tag, un commentaire, une note, un type de document
   réattribué) ou le **code** (ce que les tickets font déjà : copie de
   travail git, diff, fusion dans main, déploiement) ?
3. **« Commiter sans conflits »** = écritures sûres avec détection de
   conflit (versions), ou vraies *changesets* à la Monticello / Glamorous
   Toolkit (mes changements invisibles des autres jusqu'au commit) ?

Je retiens comme lecture de travail : **données**, **locataire logique**,
**versions + journal**, avec la porte ouverte aux deux autres. Le reste du
document est écrit sur cette base, et dit explicitement ce qui change si
une autre option est retenue.

---

## 2. État des lieux : ce que le code suppose aujourd'hui

Chiffres : **23 900 lignes de Go applicatif**, 21 100 lignes de tests,
**867 tests**, 138 fichiers de test, **78 handlers HTTP**, 5 stores
MongoDB, 3 binaires. Le refactoring touche la signature de presque tous
les stores, donc une bonne partie des 867 tests.

### 2.1 Aucune notion d'identité, nulle part

Vérifié : **zéro** occurrence de `middleware`, `cookie`, `session`,
`csrf`, `authoriz` dans `cmd/jarvisapp` et `internal/webapp` (hors le
`--claude-timeout` qui parle des sessions de Claude Code). Les 78
handlers servent tout le monde, il n'y a personne à servir. `--addr`
vaut `127.0.0.1:8090` : la seule protection est de ne pas écouter sur le
réseau.

Conséquence directe : **`/admin/tests/run` exécute `go test` sur la
machine** (`internal/testrunner`), `/admin/refresh` relit le dépôt,
l'onglet Tickets écrit dans une copie de travail git, et le déploiement
remplace le processus en service (`internal/deploy`). Ouvrir ce port à
un second humain, aujourd'hui, c'est donner l'exécution de code à qui
peut l'atteindre. Ce n'est pas un détail à traiter au jalon 50 : c'est la
condition d'entrée du multi-user.

### 2.2 Les données sont globales par construction

Cinq stores, chacun avec son interface, son `FakeStore` et son
`MongoStore` — c'est la bonne nouvelle : **la couture existe déjà
partout**, elle a juste besoin d'un paramètre en plus.

| Paquet | Interface | Collections |
|---|---|---|
| `internal/webapp` | `Store` (`store.go:55`) — 12 méthodes | `jobs` + GridFS `jobs_files` |
| `internal/notes` | `Store` (`notes.go:106`) — 10 méthodes | `notes`, `tasks` |
| `internal/mail` | `Store` (`mail.go:145`) | `emails`, `email_files` |
| `internal/tickets` | `Store` (`ticket.go:174`) | `tickets` |
| `internal/agents` | `Store` | `agents` |

Aucun de ces documents ne porte de propriétaire (34 accès Mongo au total dans `internal`, hors tests). Toutes les clés
primaires sont globales (`Job.ID`, `Note.ID`, `Mail.ID` = compte +
Message-ID…). Les fichiers GridFS sont nommés `<job_id>/<nom>`.

`internal/store` (JSON sur disque) et la CLI `jarvis` restent
mono-utilisateur local par nature : **hors périmètre**, inchangés.

### 2.3 Les ressources physiques sont des singletons de processus

C'est ici que le multi-environnement se heurte au réel, pas dans le code
de persistance :

- **`gate.Gate` = un seul jeton** (`cmd/jarvisapp/main.go:279`,
  `gate.New(1)`), partagé par les documents, le tri des emails et les
  agents des tickets. Il existe précisément parce que les serveurs
  llama.cpp échouent sous charge concurrente sur ce matériel (jalon
  21 bis). À deux environnements, B attend derrière les 18 minutes de
  scan de A, en FIFO, sans équité.
- **`models.Switcher`** bascule les profils `documents` / `code` au
  niveau de la **machine** : les modèles ne tiennent pas ensemble en
  mémoire (12 Go + 16 Go sur 24). Deux environnements qui veulent des
  profils différents font battre la bascule.
- **`mail.Service` = une seule boîte**, lue dans `~/.jarvis/mail.json`
  (0600, le mot de passe ne quitte pas la machine — contrainte explicite
  de CLAUDE.md). Une boîte par user casse cette formulation telle quelle.
- **`tickets` + `workspace` + `deploy` = un seul dépôt git**, et un
  déploiement **remplace le processus en service** (`syscall.Exec`) :
  un ticket d'un environnement redémarre l'application de tous les
  autres.
- **`internal/launcher`** décrit une machine : un fichier de config, des
  ports fixes, un `.app` dans le Dock.

### 2.4 L'historique du projet dit déjà où sont les conflits

Ce n'est pas une inquiétude théorique. Quatre bugs réels de CLAUDE.md
sont **exactement** des conflits d'écriture, trouvés avec **un seul
utilisateur** :

- jalon 15 : `Update` n'écrivait pas `doc_type` → type classifié perdu.
- jalon 23 : `FakeStore.Update` remplaçait le job entier → miniature et
  avancement effacés (le faux mentait par rapport au vrai).
- jalons 26-27 : des tags posés **pendant** un traitement étaient
  effacés à la fin (fenêtre de plusieurs minutes), sans aucun signal.
- jalon 39 : `SetTags`/`SetComment` relisaient puis réécrivaient tout le
  job → une fin de traitement écrite dans la même fenêtre était annulée,
  statut remis à `running`.

La réponse trouvée à chaque fois, empiriquement : **des écritures
ciblées** (`SetThumbnail`, `SetProgress`, `SetTags`, `SetComment` :
`$set` d'un seul champ) au lieu du read-modify-write du document entier.
C'est déjà la moitié de « commiter sans conflits », découverte à la
dure. Le multi-user ne crée pas ce problème : il le rend permanent. D'où
le §6.

---

## 3. Les décisions à prendre (avec ma recommandation)

### D1. Environnement = locataire logique ou déploiement isolé ?

| Option | Pour | Contre |
|---|---|---|
| **A. Un processus, une base, `env_id` partout** | Le moins de code ; un seul déploiement à exploiter ; la bascule des modèles reste cohérente | Un filtre oublié = fuite entre environnements (à garantir par construction, cf. §5) |
| **B. Un processus + une base par environnement** | Isolation parfaite, **zéro** changement dans les stores | N × mémoire ; il faut un routeur en façade et un plan de contrôle ; les modèles locaux ne tiennent de toute façon qu'en un exemplaire |
| **C. Hybride** | Locataire logique dans les données, ressources physiques déclarées **par environnement** plutôt que globales au processus | Un peu plus de plomberie de configuration |

**Recommandation : C.** Le cloisonnement des données se fait dans le
code (option A), mais tout ce qui est physique (modèles, dépôt git,
boîte mail, dossiers de travail) devient une **ressource d'environnement
déclarée**, pas une variable de processus. Passer plus tard de 1 à N
processus devient alors un choix d'exploitation, pas une réécriture.

### D2. « Changements » = données ou code ?

**Recommandation : les données.** Le cycle sur le code existe déjà et
fonctionne (tickets → copie de travail → diff → relecture → fusion
vérifiée → déploiement → retour arrière, jalons 26-41). Ce qui n'existe
pas, c'est la trace de « qui a retagué ce document », « qui a réattribué
ce type », « qui a modifié cette note ». À noter tout de même : la
self-modification devient un privilège d'**un seul** environnement
(§7.3), sinon un ticket de A redémarre l'application de B.

### D3. Niveau de « commit sans conflits » ?

Trois niveaux, détaillés en §6. **Recommandation : niveau 1 partout**
(journal + versions + écritures ciblées) **+ niveau 2b ciblé** (brouillon
explicite pour les rares entités où ça a du sens : une note longue, un
ticket). Le niveau 2a (overlay complet de session, à la Monticello) est
un projet en soi : il impose que les 78 handlers lisent à travers une
couche de superposition. À ne faire que si « mon Jarvis doit se
comporter comme une image Smalltalk » est un objectif en soi — ce qui
serait cohérent avec l'atelier de code déjà demandé dans cet esprit, mais
c'est alors un jalon majeur assumé, pas un effet de bord.

### D4. Authentification : quoi ?

Options : (a) comptes locaux, mot de passe haché argon2id ; (b) OIDC /
Google ; (c) lien magique par email (l'IMAP est là, pas le SMTP).

**Recommandation : (a) derrière un port `Authenticator`**, pour que (b)
se branche plus tard sans toucher aux sessions. `golang.org/x/crypto`
est déjà une dépendance indirecte : argon2id sans nouvelle dépendance
directe, cohérent avec « primitives, pas de frameworks ».

### D5. L'application quitte-t-elle `127.0.0.1` ?

C'est la vraie question de sécurité, et elle est indépendante des
autres. Si oui : TLS, CSRF sur tous les `POST`/`DELETE` HTMX, expiration
de session, limitation de débit, et `/admin` verrouillé par rôle (§8).
Si non (plusieurs users sur la même machine, ou derrière un tunnel),
le périmètre se réduit beaucoup.

**Recommandation : concevoir comme si oui, déployer comme si non**
jusqu'au jalon d'exposition. Rien d'irréversible, et ça évite d'écrire
du code qui suppose un réseau de confiance.

---

## 4. Le modèle proposé

Un paquet nouveau, `internal/tenancy`, qui ne dépend de rien d'autre du
projet (donc importable par tous les stores sans cycle) :

```go
package tenancy

type EnvID string
type UserID string
type SessionID string

// Scope est le « qui/où » d'une opération. Jamais reconstruit depuis
// une URL ou un formulaire : il vient toujours du cookie de session,
// résolu par le middleware.
type Scope struct {
    Env     EnvID
    User    UserID
    Session SessionID
    Role    Role // membre | admin de l'environnement | propriétaire de l'instance
}
```

Et trois paquets pour les entités nouvelles :

- `internal/envs` : `Environment{ID, Nom, CreatedAt, Settings}` +
  appartenances `Membership{Env, User, Role}`.
- `internal/users` : `User{ID, Email, PasswordHash, CreatedAt, Disabled}`,
  port `Authenticator`.
- `internal/session` : `Session{ID, Env, User, CreatedAt, LastSeenAt,
  ExpiresAt, UserAgent}`, persistée ; le **cookie porte un jeton
  aléatoire de 32 octets dont seul le SHA-256 est stocké** (un dump de la
  base n'est pas un trousseau de clés vivantes) ; `HttpOnly`,
  `SameSite=Lax`, `Secure` dès qu'il y a du TLS.

Chaque store existant garde son interface, avec **une méthode de portée
en plus** :

```go
// Avant : jobStore.Get(ctx, id)
// Après : jobStore.For(scope).Get(ctx, id)
```

`For(scope)` retourne une vue dont **chaque filtre et chaque insertion
portent déjà `env_id`**. C'est le point central du §5 : un handler ne
peut pas oublier la portée, parce qu'il n'a jamais accès au store
non-scopé.

---

## 5. La portée garantie par construction, pas par discipline

Le risque de l'option A (une base partagée) est un filtre oublié dans un
des 34 accès Mongo. Trois garde-fous, tous dans l'esprit « norme = un
test » du jalon 9 :

1. **Le store non-scopé n'est pas accessible aux handlers.** Le
   `MongoStore` devient un type non exporté derrière un
   `Root` qui n'expose que `For(scope) Store`. Ce qui est impossible à
   obtenir est impossible à mal utiliser.
2. **Un test de contrat d'isolation, imposé aux cinq stores.**
   `internal/notes/contract_test.go` et `internal/mail/contract_test.go`
   font déjà tourner le **même** contrat sur `FakeStore` et `MongoStore`
   (précisément pour que le faux ne mente plus, leçon du jalon 23). On y
   ajoute : écrire dans l'environnement A, puis vérifier que **chaque**
   méthode de lecture, de liste, de compte, de suppression et de mise à
   jour est aveugle depuis B. Un nouveau store ne passe le contrat que
   scopé.
3. **Une norme sur les routes.** `chi.Walk` énumère les routes réellement
   montées ; le test échoue si une route n'est ni dans la liste blanche
   des routes publiques (`/login`, `/static/*`, `/health`) ni couverte par
   le middleware de session. Ajouter un handler sans y penser fait échouer
   un test au lieu d'ouvrir un trou.

Deux détails qui comptent :

- **Les identifiants d'une autre portée répondent 404, jamais 403.** Un
  403 confirme l'existence de l'objet.
- **Les fichiers GridFS deviennent `<env>/<job>/<nom>`**, pour qu'une
  lecture mal scopée ne puisse pas tomber sur les octets d'un autre
  environnement même si l'identifiant fuit.

---

## 6. Tracker et commiter les changements

### Niveau 0 — le journal (répond à « tracker »)

Une collection `changes`, une entrée par mutation :

```go
type Change struct {
    ID      string
    Env     EnvID
    User    UserID
    Session SessionID
    At      time.Time
    Kind    string // "job", "note", "task", "mail", "ticket"
    Target  string // identifiant de l'entité
    Op      string // "create", "update", "delete", "retag", "reprocess"...
    Fields  []string       // champs touchés
    Before  map[string]any // seulement les champs touchés, pas l'entité entière
    After   map[string]any
}
```

Écrit par un **décorateur** autour de chaque store scopé (pas de code de
journalisation dispersé dans les 78 handlers). Rend possible : la page
« ma session » (ce que j'ai changé depuis que je suis connecté),
l'historique d'une entité dans son volet, et le « qui a fait ça »
qu'aucune trace ne donne aujourd'hui.

Append-only, comme `runs.jsonl` (jalon 6) et le fil des tickets
(`AppendEvent`, `$push` atomique) : deux mécanismes déjà en place dans le
projet, pour la même raison.

### Niveau 1 — versions et conflits détectés (recommandé, partout)

Chaque entité porte `Version int`. Toute écriture est conditionnelle :

```go
res := coll.UpdateOne(ctx,
    bson.D{{"_id", id}, {"env_id", env}, {"version", expected}},
    bson.D{{"$set", fields}, {"$inc", bson.D{{"version", 1}}}})
if res.MatchedCount == 0 { return ErrConflict }  // jamais un succès silencieux
```

Trois propriétés, dans cet ordre d'importance :

1. **Les écritures restent ciblées** (la leçon des jalons 23/39) : deux
   users qui touchent des champs différents de la même note ne se
   marchent jamais dessus. C'est là que « sans conflit » se gagne
   vraiment — par la granularité, pas par un algorithme de fusion.
2. **Quand il y a collision, elle est vue** : `ErrConflict` porte l'état
   courant, l'interface montre « quelqu'un a modifié ceci pendant ta
   saisie », côte à côte, avec un choix. Jamais un écrasement muet —
   c'est la même règle que « les valeurs sous le seuil sont marquées pour
   revue humaine, jamais acceptées silencieusement ».
3. **Les traitements de fond écrivent leurs propres champs**, et rien
   d'autre : le pipeline n'a aucune raison de réécrire un tag posé par un
   humain pendant qu'il tournait. C'est déjà la direction prise au jalon
   39, généralisée et verrouillée par des tests.

Test dédié : **rejouer chacun des quatre bugs de §2.4 avec deux
écrivains concurrents**, et vérifier qu'aucune donnée ne disparaît. Ces
bugs existent, ils sont documentés, ils font des tests de régression
parfaits.

### Niveau 2 — changesets (« commiter » au sens fort)

Trois formes, par coût croissant :

- **2b. Brouillons ciblés (recommandé).** Certaines entités seulement
  (note, ticket) acceptent une version brouillon rattachée à une session,
  invisible des autres, publiée au commit. Couvre le besoin réel (« je
  rédige, je ne veux pas que mes demi-phrases soient visibles ») pour un
  coût contenu.
- **2a. Overlay de session complet (Monticello / GT).** Toutes mes
  modifications vivent dans un changeset ; mes lectures voient
  `base + mes opérations` ; le commit rebase contre les versions
  courantes et signale les opérations dont la base a bougé. C'est la
  version conceptuellement juste — et elle impose que **tout** chemin de
  lecture passe par l'overlay : les 78 handlers, les listes, la
  recherche Mongo (`$regex` sur `search_text`…), les compteurs de la
  barre latérale. À décider comme objectif, pas à subir.
- **2c. Event sourcing.** L'état = un flux d'événements par
  environnement, des vues matérialisées. Les ajouts ne conflictent
  jamais par nature ; c'est la réécriture la plus profonde des cinq
  stores. Non recommandé ici.

---

## 7. Les ressources physiques par environnement (le vrai mur)

Le cloisonnement des données est du travail mécanique et testable. Ce
qui demande de vrais arbitrages, c'est ce qui n'existe qu'en un
exemplaire sur la machine.

### 7.1 L'inférence locale reste sérialisée, pour toujours

Sur ce Mac (24 Go, ~17,8 Go utilisables par le GPU), les modèles de
documents (~12 Go) et le modèle de code (~16 Go) ne tiennent pas
ensemble : c'est pour ça que `models.Switcher` existe. Aucun découpage
logiciel ne change ça. Donc :

- `gate.Gate` passe d'un sémaphore FIFO à une **file équitable par
  environnement** (tourniquet), sinon un document de 5 pages denses
  (~18 min mesurées) bloque tous les autres environnements.
- Une **priorité et un quota par environnement** (nombre de documents en
  attente, minutes d'inférence) deviennent nécessaires dès le deuxième
  environnement actif.
- À écrire noir sur blanc dans la documentation : **l'inférence est une
  ressource de machine, partagée et sérialisée**. La vraie réponse au
  débit est celle déjà évoquée par l'utilisateur (machine dédiée), ou des
  points d'accès modèles distincts par environnement (`--vlm-url` /
  `--llm-url` deviennent des réglages d'environnement, ce qui marche déjà
  aujourd'hui : rien dans le code n'est spécifique à `localhost`).

### 7.2 Une boîte mail par user, et une contrainte de vie privée à reformuler

Aujourd'hui : une boîte, `~/.jarvis/mail.json` en 0600, et CLAUDE.md
affirme que le mot de passe ne quitte pas la machine. À plusieurs users
sur un serveur, le fichier local d'un user ne veut plus rien dire.

Proposition : identifiants **par user**, stockés dans Atlas **chiffrés**
(AES-GCM) avec une clé qui n'y est jamais — `~/.jarvis/secret.key`, 0600,
sur l'hôte qui sert l'application. Atlas ne détient que du chiffré ; la
formulation de CLAUDE.md devient « le mot de passe de boîte n'est jamais
stocké en clair hors de l'hôte », ce qui reste vrai et vérifiable. Le tri
reste fait par le modèle local.

### 7.3 Tickets, git et déploiement : privilège d'un seul environnement

Un déploiement fusionne dans `main`, recompile et **remplace le processus
en service**. Avec plusieurs environnements, cela redémarre l'application
de tous. Deux options :

- **Recommandée pour commencer** : l'onglet Tickets, l'espace `/admin` et
  le déploiement n'existent que pour l'**environnement propriétaire de
  l'instance** (celui de Julian). Les autres environnements ont
  Documents / Emails / Notes / Tâches. Une ligne de plus dans la nav
  conditionnée par le rôle, et une vérification côté serveur (jamais
  seulement côté template).
- Plus tard : un dépôt et un processus par environnement (option B de
  D1), ce qui rend le déploiement local à un environnement — mais
  multiplie tout le reste.

À noter aussi : l'essai à blanc du déploiement et la relecture visuelle
lancent déjà une instance isolée sur des collections `*_deploycheck` /
`*_visualcheck` (jalons 30, 38). Ce mécanisme devra créer un
**environnement jetable**, pas seulement des collections jetables.

### 7.4 Dossiers

`--watch-dir`, `--out-dir`, `--work-dir`, `--worktrees-dir` deviennent
des racines **par environnement** (`<racine>/<env>/…`). Le dossier
surveillé, en particulier, est une porte d'entrée de documents : il doit
appartenir à un environnement et à un user, sinon ses documents n'ont pas
de propriétaire.

---

## 8. Sécurité : la liste de ce qui n'existe pas encore

À traiter **avant** d'ouvrir l'application à un second humain, pas après :

| Manque | Conséquence aujourd'hui |
|---|---|
| Aucune authentification | Quiconque atteint le port est administrateur |
| `/admin/tests/run` exécute `go test` | Exécution de code arbitraire pour qui atteint le port |
| Aucun CSRF sur les `POST`/`DELETE` HTMX | Un site tiers peut agir au nom de l'utilisateur connecté |
| Pas de TLS | Cookie de session en clair |
| Aucune expiration de session | Un jeton volé est valable pour toujours |
| Autorisation par préfixe d'URL (`/admin`) | Un préfixe n'est pas un contrôle d'accès ; il faut un rôle vérifié côté serveur, sur chaque handler |
| Identifiants devinables | Sans contrôle de portée : lecture des documents d'un autre environnement |
| Aucune limitation de débit | Connexion en force brute, saturation de la file des modèles |

Le journal du §6 (niveau 0) est aussi le début d'une piste d'audit :
qui a supprimé ce document, quand, depuis quelle session.

---

## 9. Migration des données existantes

Les données d'Atlas n'ont pas d'environnement. La règle du jalon 9 vaut
ici telle quelle : **une migration est un acte de déploiement délibéré,
jamais une lecture qui triche.**

1. Créer l'environnement `local` et le user Julian (propriétaire).
2. Une commande explicite (`jarvis migrate-tenancy`, `--dry-run` d'abord)
   estampille chaque `job`, `note`, `task`, `email`, `ticket` avec
   `env_id=local`, `owner`, `version=1`, et renomme les fichiers GridFS
   sous `local/…`.
3. **Les lectures refusent un enregistrement sans `env_id`** avec un
   message qui dit quoi lancer — plutôt que de le traiter comme global
   (ce qui serait précisément la fuite qu'on veut rendre impossible).
4. Bump des versions de schéma et des empreintes, comme
   `internal/store/schema_norm_test.go` l'exige déjà pour les
   enregistrements sur disque.
5. Idempotente et relançable, comme `MigrateComments`.

---

## 10. Les normes (tests) qui tiennent le refactoring

Dans l'esprit du jalon 9 — une norme est une fonction `Test…` ordinaire,
pas un outil séparé :

1. `TestStores_AreIsolatedBetweenEnvironments` — le contrat d'isolation
   imposé aux cinq stores, sur `FakeStore` **et** `MongoStore` (Atlas,
   `-tags=integration`).
2. `TestRoutes_AllRequireSessionUnlessDeclaredPublic` — `chi.Walk` contre
   une liste blanche explicite.
3. `TestAdminRoutes_RequireOwnerRole` — le préfixe d'URL ne suffit pas.
4. `TestConcurrentWriters_DoNotLoseData` — les quatre bugs de §2.4
   rejoués à deux écrivains.
5. `TestFakeStore_BehavesLikeMongoOnTargetedWrites` — déjà la direction
   prise au jalon 23, étendue aux versions.
6. `TestNoStoreIsReachableWithoutScope` — la garantie de §5.1, vérifiée
   par l'absence de constructeur exporté rendant un store non scopé.

---

## 11. Découpage en jalons

Ordre choisi pour qu'à aucun moment l'installation quotidienne de Julian
ne soit cassée, et pour que **les données soient cloisonnées avant que
quiconque d'autre puisse se connecter**.

| Jalon | Contenu | Livrable observable | Taille |
|---|---|---|---|
| **42** | `internal/tenancy` (Scope, EnvID, UserID), `For(scope)` sur les cinq stores, un seul environnement `local` câblé en dur, aucun changement de comportement | L'application est identique, la couture existe, 867 tests verts | Moyen |
| **43** | Cloisonnement réel : `env_id` écrit et filtré partout, GridFS re-préfixé, contrat d'isolation, migration `local` | Les données portent un environnement ; un test prouve l'aveuglement entre environnements | **Gros** |
| **44** | `users`, `envs`, `session`, connexion/déconnexion, cookie, middleware, CSRF, rôles, `/admin` verrouillé, amorçage mono-user (le Mac démarre toujours sans écran de connexion) | Deux users réels dans deux environnements, chacun ne voyant que le sien | **Gros** |
| **45** | Journal des changements (niveau 0) : décorateur, page « ma session », historique d'une entité | « Qui a changé quoi » visible | Moyen |
| **46** | Écritures sûres (niveau 1) : `Version`, `ErrConflict`, écritures ciblées généralisées, résolution de conflit dans l'interface | Deux users éditant la même note ne perdent rien ; les quatre bugs historiques sont des tests | Moyen |
| **47** | *(optionnel, selon D3)* brouillons de session (2b) — ou changesets complets (2a), qui est alors un jalon majeur à part entière | « Commiter » au sens fort | Moyen → Gros |
| **48** | Ressources par environnement : file équitable et quotas sur les modèles, boîte mail par user chiffrée, dossiers par environnement, tickets/déploiement réservés à l'environnement propriétaire | Deux environnements actifs sans se bloquer ni se marcher dessus | Moyen |
| **49** | Gestion des environnements : création, invitations, changement d'environnement dans la nav, environnement jetable pour l'essai à blanc et la relecture visuelle | Julian crée un environnement et y invite quelqu'un | Moyen |
| **50** | Exposition réseau : TLS, reverse proxy, limitation de débit, expiration, durcissement, et révision du déploiement (un déploiement redémarre tout le monde) | Accessible hors de la machine sans trou connu | Moyen |

Les jalons 42 à 46 sont le cœur. 42 est délibérément un jalon « qui ne
fait rien » : c'est celui qui rend les quatre suivants sûrs.

---

## 12. Ce que ça change dans CLAUDE.md

À mettre à jour **quand ce sera décidé**, pas avant :

- Les contraintes non négociables : les exceptions 2, 3 et 4 (documents,
  notes, emails dans Atlas) parlent d'un utilisateur unique. Elles
  deviennent « les données d'un environnement » et doivent dire ce qui
  cloisonne un environnement d'un autre.
- L'exception « le mot de passe de la boîte ne quitte pas la machine »
  (§7.2) doit être reformulée pour rester vraie.
- Une contrainte nouvelle : **l'inférence locale est une ressource de
  machine partagée et sérialisée entre environnements**.
- « Tourne entièrement en local » devient « peut être servi à plusieurs
  users », ce qui change la nature du projet plus que n'importe quelle
  ligne de code de ce plan. C'est probablement le point à valider en
  premier.

## 13. Ce que je ne recommande pas

- **Un `env_id` ajouté champ par champ dans les handlers.** 78 handlers,
  34 accès Mongo : la discipline finira par céder. Le point de §5.1 (le
  store non scopé inaccessible) n'est pas un raffinement, c'est ce qui
  rend l'option A défendable.
- **L'event sourcing (2c)** pour atteindre « pas de conflits ». Le coût
  est une réécriture des cinq stores, pour un gain que le niveau 1 offre
  à 95 % dans ce contexte.
- **Commencer par l'authentification.** Un écran de connexion sur des
  données non cloisonnées donne l'illusion de l'isolement. 43 avant 44.
- **Multiplier les processus tout de suite** (option B). Tant que les
  modèles locaux ne tiennent qu'en un exemplaire, N processus ne donnent
  pas N fois le débit — ils donnent N fois la consommation mémoire.
