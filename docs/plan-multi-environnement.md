# Plan : environnements, users, sessions, changesets

**État : les jalons 42 à 51 sont écrits et livrés** (voir « État des
jalons » de CLAUDE.md pour ce que chacun a réellement produit, et le
journal git pour le détail commit par commit). Ce document reste la
référence des décisions et de la conception ; il n'est plus une intention.

Ce qui n'a pas pu être exercé depuis l'environnement de développement de
ce chantier, et reste à valider : les contrats Mongo contre Atlas
(`-tags=integration` avec `MONGO_URI`), le flux Google avec un vrai client
OAuth et un navigateur, et un parcours à deux comptes. Une chose du jalon
50 est volontairement inachevée et signalée comme telle : une boîte mail
*par user* (le chiffrement des identifiants est fait, le fan-out de la
relève non).

Les cinq arbitrages de structure sont tranchés en §3.

---

## 1. Ce qu'on construit

> L'environnement en local actuel c'est mon environnement. L'idée c'est
> d'avoir plusieurs environnements. Chaque environnement aura un ou
> plusieurs users. À chaque fois qu'un user se connecte il aura une
> session. La session doit pouvoir gérer et tracker les changements
> effectués par le user. Il faudra pouvoir commiter un changement sans
> créer de conflits.

| Notion | Ce que c'est |
|---|---|
| **Environnement** | Un espace cloisonné : ses documents, emails, notes, tâches, tickets. L'installation actuelle devient l'environnement `local` de Julian, propriétaire de l'instance. |
| **User** | Une personne (identifiée par son compte Google), membre d'un ou plusieurs environnements, avec un rôle. |
| **Session** | Ce qu'un user obtient en se connectant : identité, environnement courant, et le fil de ce qu'il a changé. |
| **Changement** | Une modification **en attente**, visible de son auteur seul, attribuée à (user, session, instant), commitée explicitement. |

---

## 2. État des lieux vérifié

23 900 lignes de Go applicatif, 21 100 de tests, **867 tests**, **78
handlers HTTP**, 5 stores MongoDB, **34 accès Mongo** dans `internal`.

### 2.1 Aucune identité, nulle part
Zéro occurrence de `session`, `cookie`, `middleware`, `csrf`, `authoriz`
dans `cmd/jarvisapp` et `internal/webapp`. Les 78 handlers servent tout
le monde ; la seule protection est `--addr 127.0.0.1:8090`.

### 2.2 La couture de persistance existe déjà
Cinq stores, chacun une interface + `FakeStore` + `MongoStore` :

| Paquet | Interface | Collections |
|---|---|---|
| `internal/webapp` | `Store` (`store.go:55`), 12 méthodes | `jobs` + GridFS `jobs_files` |
| `internal/notes` | `Store` (`notes.go:106`), 10 méthodes | `notes`, `tasks` |
| `internal/mail` | `Store` (`mail.go:145`) | `emails`, `email_files` |
| `internal/tickets` | `Store` (`ticket.go:174`) | `tickets` |
| `internal/agents` | `Store` | `agents` |

Aucun document ne porte de propriétaire. `internal/store` (JSON sur
disque) et la CLI `jarvis` restent mono-utilisateur local : hors
périmètre.

### 2.3 Les ressources physiques sont des singletons de processus
- **`gate.New(1)`** (`cmd/jarvisapp/main.go:279`) : un seul jeton sur les
  modèles locaux, partagé documents / tri des emails / agents. Il existe
  parce que llama.cpp échoue sous charge concurrente sur ce matériel
  (jalon 21 bis).
- **`models.Switcher`** bascule les profils `documents` (~12 Go) / `code`
  (~16 Go) au niveau de la **machine** : ils ne tiennent pas ensemble.
- **`mail.Service`** : une seule boîte, `~/.jarvis/mail.json`.
- **`tickets` + `workspace` + `deploy`** : un seul dépôt git, et un
  déploiement **remplace le processus en service** (`syscall.Exec`).
- **`internal/launcher`** décrit une machine.

### 2.4 Les conflits d'écriture sont déjà là, avec un seul utilisateur
Quatre pertes de données documentées dans CLAUDE.md, toutes du
read-modify-write de document entier : jalon 15 (`doc_type` perdu),
jalon 23 (`FakeStore.Update` effaçait miniature et avancement),
jalons 26-27 (tags posés **pendant** un traitement effacés à la fin),
jalon 39 (`SetTags` annulait une fin de traitement écrite dans la même
fenêtre). La réponse trouvée à la dure — **des écritures ciblées**
(`SetThumbnail`, `SetProgress`, `SetTags`, `SetComment` : `$set` d'un
seul champ) — est déjà la moitié du travail du §7. Le multi-user ne crée
pas ce problème, il le rend permanent.

---

## 3. Décisions prises

| # | Décision | Ce qui est retenu |
|---|---|---|
| **D1** | Nature d'un environnement | **Hybride** : locataire logique dans les données (un processus, une base, `env_id` partout), mais toute ressource physique (modèles, dépôt git, boîte mail, dossiers) devient une **ressource d'environnement déclarée** et non une variable de processus. Passer plus tard à N processus sera un choix d'exploitation, pas une réécriture. |
| **D2** | Périmètre des « changements » | **Les données** de l'application. Le cycle sur le code existe déjà (tickets → copie de travail → diff → relecture → fusion vérifiée → déploiement → retour arrière). |
| **D3** | « Commiter sans conflits » | **Vrai changeset** : mes modifications sont invisibles des autres jusqu'au commit ; le commit rebase contre l'état courant et signale les conflits champ par champ. Conception complète en §7. |
| **D4** | Authentification | **Google** (OpenID Connect). Détail en §6. |
| **D5** | Exposition réseau | **L'application reste locale** (`127.0.0.1`). Conséquences en §9 — dont une faille qui existe **déjà aujourd'hui** et que « rester en local » ne protège pas. |

### Deux précisions que D3 impose

**À qui appartient un changeset ?** « La session doit tracker les
changements » — mais un user sur son portable puis son téléphone attend
de retrouver ses modifications en attente. Retenu : **un changeset par
(environnement, user)**, partagé par ses sessions ; **chaque opération
enregistre la session** qui l'a produite, pour la traçabilité. La session
*enregistre*, le changeset *appartient au user*.

**Que peut-on mettre en attente ?** Pas tout, et c'est ce qui rend le
vrai changeset réalisable ici (§7.1) : les champs écrits par la
**machine** (statut, avancement, résultat d'extraction, miniature, tri
d'un email, fil d'un ticket) ne passent **jamais** par un changeset — un
commit ne doit pas retenir un résultat d'OCR. Seuls les champs écrits par
un **humain** sont mis en attente.

---

## 4. Le modèle

`internal/tenancy`, qui ne dépend de rien d'autre du projet (donc
importable par tous les stores sans cycle) :

```go
package tenancy

type EnvID string
type UserID string
type SessionID string

// Scope est le « qui/où » d'une opération. Jamais reconstruit depuis une
// URL ni un formulaire : il vient du cookie de session, résolu par le
// middleware. Session vide = travail de fond (pipeline, relève des
// emails, agents) : pas de changeset, écriture directe dans la base.
type Scope struct {
    Env     EnvID
    User    UserID
    Session SessionID
    Role    Role // membre | admin d'environnement | propriétaire de l'instance
}
```

Trois paquets nouveaux : `internal/envs`
(`Environment`, `Membership{Env, User, Role}`), `internal/users`
(`User{ID, Email, GoogleSub, ...}`), `internal/session`.

Chaque store garde son interface, avec une méthode de portée en plus :

```go
// Avant : jobStore.Get(ctx, id)
// Après : jobStore.For(scope).Get(ctx, id)
```

`For(scope)` retourne une vue dont **chaque filtre et chaque insertion
portent déjà `env_id`** — et, quand `scope.Session` est renseigné, qui
passe par l'overlay du changeset (§7.3).

---

## 5. La portée garantie par construction

Le risque de D1 est un filtre oublié dans un des 34 accès Mongo. Trois
garde-fous, dans l'esprit « norme = un test » du jalon 9 :

1. **Le store non scopé est inaccessible aux handlers.** Le `MongoStore`
   devient non exporté derrière un `Root` qui n'expose que
   `For(scope) Store`. Ce qu'on ne peut pas obtenir, on ne peut pas mal
   utiliser.
2. **Un contrat d'isolation imposé aux cinq stores.**
   `internal/notes/contract_test.go` et `internal/mail/contract_test.go`
   font déjà tourner le **même** contrat sur `FakeStore` et `MongoStore`
   (leçon du jalon 23 : le faux mentait). On y ajoute : écrire dans
   l'environnement A, vérifier que **chaque** méthode de lecture, liste,
   compte, mise à jour et suppression est aveugle depuis B.
3. **Une norme sur les routes.** `chi.Walk` énumère les routes réellement
   montées ; le test échoue si l'une n'est ni dans la liste blanche
   publique (`/login`, `/auth/*`, `/static/*`) ni couverte par le
   middleware de session.

Deux détails : un identifiant d'une autre portée répond **404, jamais
403** (un 403 confirme l'existence) ; les fichiers GridFS passent de
`<job>/<nom>` à **`<env>/<job>/<nom>`**, pour qu'une lecture mal scopée ne
puisse pas tomber sur les octets d'un autre environnement même si
l'identifiant fuit.

---

## 6. Authentification Google, sur une application locale

Le point délicat n'est pas OIDC, c'est **OIDC sur une application qui
tourne sur `127.0.0.1` et qui doit continuer à démarrer depuis le Dock**.

**Le flux.** Client OAuth de type *Desktop app* (ou *Web* avec
`http://127.0.0.1:8090/auth/callback` en URI de redirection : Google
autorise `http` sur l'adresse de boucle locale, contrairement à tout
autre hôte). Code d'autorisation + **PKCE**, échange du code
**côté serveur** contre le jeton, puis lecture du profil.

**Pas de vérification de signature JWT à écrire.** Le jeton d'identité
est obtenu par un appel direct de Jarvis au point de terminaison de
Google, en TLS : la spécification OIDC (Core §3.1.3.7) admet dans ce cas
la validation TLS du serveur **à la place** du contrôle de signature.
Donc pas de JWKS, pas de RSA à la main, pas de dépendance nouvelle — on
vérifie `iss`, `aud`, `exp` et `email_verified`, et c'est tout. Cohérent
avec « construire à partir de primitives » : ~100 lignes de `net/http`,
sans `golang.org/x/oauth2`.

**La session.** Cookie portant un jeton aléatoire de 32 octets dont seul
le SHA-256 est stocké (un dump de la base n'est pas un trousseau de clés
vivantes) ; `HttpOnly`, `SameSite=Lax`, `Secure` dès qu'il y a du TLS.
Expiration explicite, prolongée à l'usage.

**Qui a le droit d'entrer.** Un compte Google ne donne rien par
lui-même : il faut une `Membership`. L'e-mail du **propriétaire de
l'instance** est un réglage (config du lanceur), jamais deviné. Tout
autre e-mail sans appartenance reçoit « demande l'accès à
l'administrateur », pas un environnement vide.

**Trois conséquences à assumer, aucune n'est bloquante mais aucune n'est
gratuite :**

1. **Se connecter demande Internet.** Une application jusqu'ici capable
   de tout faire hors ligne ne pourra plus ouvrir de session si Google
   est injoignable. **Filet retenu** : au démarrage, le lanceur inscrit
   dans son journal une URL de secours à jeton unique
   (`/auth/local?token=…`, valable une fois, quelques minutes) qui ouvre
   une session propriétaire sans passer par Google. C'est le seul chemin
   d'authentification local, et il est délibérément limité au possesseur
   du journal — c'est-à-dire de la machine.
2. **Le secret du client OAuth** vit dans `~/.jarvis/` en 0600, jamais
   dans Atlas, jamais en argument de ligne de commande (visible dans
   `ps`) — même règle que `MONGO_URI` aujourd'hui.
3. **CLAUDE.md devra le dire** : une nouvelle sortie de données vers un
   tiers (l'identité, vers Google). Pas de document, pas de contenu — mais
   à écrire noir sur blanc comme les quatre exceptions existantes.

---

## 7. Le changeset (D3)

C'est le cœur du chantier. Objectif : **mes modifications vivent dans mon
changeset, mes lectures voient `base + mes opérations`, les autres ne les
voient pas, et le commit rebase contre l'état courant.** Le modèle est
celui d'ENVY / Monticello — et de l'atelier de code déjà demandé dans
cet esprit.

### 7.1 Ce qui est mis en attente, et ce qui ne l'est jamais

C'est la décision qui rend le reste faisable. Le partage est déjà lisible
dans les noms de méthodes existants :

| Écrit par un humain → **mis en attente** | Écrit par la machine → **direct dans la base** |
|---|---|
| `SetTags`, `SetComment`, réattribution de type, suppression d'un document | `finish`, `save`, `progressRecorder`, `Thumbnail`, `RecoverOrphaned`, `SearchText`, `Format` |
| note : titre, corps, tags, épinglage, liens | tri d'un email (`SetTriage`), relève (`Save`, `LastUID`) |
| tâche : titre, échéance, priorité, fait/pas fait, liens | fil d'un ticket (`AppendEvent`), plan, diff, relecture, état de déploiement |
| ticket : titre, besoin, critères, commentaires | prompt en vigueur d'un agent (effet immédiat sur les traitements) |

Deux règles qui découlent de ce tableau :

- **Une action n'est pas un changement.** Relancer une extraction,
  importer un fichier, lancer l'analyse d'un ticket ont des effets hors
  base (modèles, disque, git) : elles s'exécutent **immédiatement**, elles
  ne se commitent pas. Seules les **données** sont mises en attente. La
  suppression d'un document est une donnée (elle se commite) ; la purge
  GridFS n'a lieu qu'au commit.
- **Le travail de fond n'a pas de changeset.** `scope.Session == ""` →
  écriture directe. Vérifié par une norme : aucun composant de fond ne
  reçoit un store surchargé d'overlay.

### 7.2 Le modèle d'opération

```go
// Op est une modification en attente, au grain du champ — pas du
// document. Deux users qui touchent des champs différents de la même
// note ne conflictent jamais : c'est là que « sans conflit » se gagne.
type Op struct {
    ID      string
    Kind    string    // "job" | "note" | "task" | "ticket"
    Target  string    // identifiant de l'entité ("" si création)
    Field   string    // "tags", "body", "due"...
    Action  Action    // Set | Add | Remove | Create | Delete
    Value   any
    BaseVer int       // version de l'entité au moment de la saisie
    Session SessionID // qui l'a produite (traçabilité)
    At      time.Time
}

// Changeset : les opérations en attente d'un user dans un environnement.
type Changeset struct {
    Env  EnvID
    User UserID
    Ops  []Op // append-only ($push atomique, comme le fil des tickets)
}
```

Append-only par `$push`, exactement comme `tickets.AppendEvent` et
`runs.jsonl` (jalons 6 et 26) : deux mécanismes déjà en place, pour la
même raison — une écriture concurrente ne peut pas perdre une opération.

### 7.3 L'overlay : comment les lectures voient les changements en attente

Le problème réel du vrai changeset dans cette application : la recherche
et les listes sont faites **par Mongo** (`$regex` sur `search_text`,
filtres de date, tri). Une note dont le titre est modifié en attente ne
serait pas trouvée par son nouveau titre, et serait trouvée par l'ancien.

**La solution tient dans une observation sur le code existant** :
`webapp.FakeStore.List` filtre déjà **en Go** — statut, format, bornes de
date, recherche — et `notes`/`mail` font pareil. Ce prédicat existe donc,
et il est déjà couvert par les tests de contrat qui tournent sur le faux
**et** sur Mongo.

Le plan :

1. **Extraire le prédicat** : `func (q ListQuery) Matches(j Job) bool`,
   utilisé par `FakeStore` (aucun changement de comportement, les tests
   de contrat le prouvent) **et** par l'overlay.
2. **L'overlay est un décorateur en Go, en mémoire.** La requête de base
   tourne dans Mongo comme aujourd'hui ; les opérations en attente d'un
   user (peu nombreuses) sont appliquées après :
   - `Get(id)` : entité de base + opérations visant `id`.
   - `List(q)` : résultats de base → opérations appliquées → **re-filtrés
     avec `q.Matches`** → plus les entités **créées** en attente, filtrées
     par le même prédicat → tri → limite.
   - `Count(q)` : même chemin.
3. **Le désaccord Mongo/Go est possible** (casse, accents, métacaractères
   — `internal/notes` utilise déjà `regexp.QuoteMeta`). C'est précisément
   ce que le contrat commun Fake/Mongo surveille, et il existe déjà.

Coût honnête : la limite de liste s'applique après filtrage en mémoire,
donc une requête doit demander un peu plus large à la base quand des
opérations sont en attente. Sur des collections de cette taille, sans
importance ; à documenter comme une limite connue plutôt qu'à optimiser
d'avance.

### 7.4 Commit, rebase, conflits

```
Pour chaque opération, dans l'ordre :
  relire la version courante de l'entité
  si BaseVer == version courante            -> applicable
  si le champ visé n'a pas changé depuis    -> applicable (fast-forward)
  sinon                                     -> CONFLIT sur cette opération
```

- **Aucun conflit** : toutes les opérations sont appliquées, chacune par
  un `$set` ciblé conditionné sur la version (§8), dans une transaction
  Mongo ; le changeset est vidé ; une entrée par opération part au
  journal (§8).
- **Conflits** : rien n'est appliqué en silence. L'interface les présente
  **opération par opération** — la mienne, la sienne, qui et quand —
  exactement comme la revue de diff d'un ticket. Trois issues par
  conflit : garder la mienne (rebase sur la nouvelle version), garder la
  sienne (abandonner l'opération), fusionner à la main. Les opérations
  sans conflit peuvent être commitées sans attendre la résolution des
  autres.
- **Jamais d'écrasement muet** : même règle que « les valeurs sous le
  seuil sont marquées pour revue humaine, jamais acceptées
  silencieusement ».

### 7.5 Ce que le user voit

- Un compteur permanent dans la nav / la barre latérale : « 3 changements
  non commités ».
- Une page **Changements** : les opérations en attente groupées par
  entité, en diff (avant → après), avec commit/abandon par opération et
  pour l'ensemble.
- Sur chaque entité modifiée, un repère « modifié, non commité ».

### 7.6 Les pièges à traiter explicitement

- **Un changeset oublié.** Une note rédigée il y a trois semaines, jamais
  commitée, invisible de tous : le compteur permanent est la réponse
  minimale ; un rappel dans la barre latérale au-delà de N jours est à
  prévoir. Les opérations **survivent** à la fin de session (elles
  appartiennent au user) — jamais jetées en silence.
- **Une entité supprimée par un autre** pendant que j'ai des opérations
  dessus : conflit, traité comme les autres.
- **Deux sessions du même user** partagent le changeset (décision §3) ;
  chaque opération garde sa session d'origine.

---

## 8. Journal et écritures versionnées

Le changeset a besoin des deux — ils ne sont pas une alternative à D3,
ils en sont le socle.

**Versions.** Chaque entité porte `Version int`. Toute écriture est
conditionnelle :

```go
res := coll.UpdateOne(ctx,
    bson.D{{"_id", id}, {"env_id", env}, {"version", expected}},
    bson.D{{"$set", fields}, {"$inc", bson.D{{"version", 1}}}})
if res.MatchedCount == 0 { return ErrConflict } // jamais un succès silencieux
```

C'est ce qui rend `BaseVer` du §7.2 vérifiable, et ce qui protège les
écritures de la machine entre elles. Test dédié : **rejouer les quatre
bugs du §2.4 avec deux écrivains concurrents** — ils sont documentés, ils
font des tests de régression tout prêts.

**Journal.** Une collection `changes`, append-only, alimentée par un
décorateur autour de chaque store scopé (pas de journalisation dispersée
dans 78 handlers) : `{env, user, session, at, kind, target, op, fields,
before, after}` — seulement les champs touchés, pas l'entité entière.
C'est l'histoire **commitée** (le changeset étant l'état **en attente**),
la piste d'audit du « qui a supprimé ce document », et la source de la
page « ma session ».

---

## 9. Sécurité : ce que « reste en local » ne protège pas

D5 réduit beaucoup le périmètre — TLS, reverse proxy et limitation de
débit attendent. **Mais deux choses ne sont pas des questions de
réseau**, et l'une existe déjà aujourd'hui :

### 9.1 Une faille présente aujourd'hui, que « rester en local » n'empêche pas

Un navigateur autorise la soumission d'un formulaire HTML **inter-origine**
vers `http://127.0.0.1:8090` sans requête préalable : la réponse est
illisible par l'attaquant, mais **l'effet a lieu**. Aucune route de
Jarvis ne porte de jeton anti-CSRF ni ne vérifie l'origine. Vérifié dans
le code, pas supposé :

**Cas 1 — exécution des tests du dépôt.** `POST /admin/tests/run` prend
`pkg`/`name`/`integration` en paramètres d'URL et lance `go test`
(`internal/testrunner`). Un formulaire hostile suffit à le déclencher.

**Cas 2 — le plus grave : un ticket injecté, développé et déployé.**
La chaîne est entièrement activée par défaut dans l'usage quotidien :

1. `POST /tickets` lit `title`, `need`, `acceptance`, `agent` avec
   `r.FormValue` (`cmd/jarvisapp/tickets.go:21`) — exactement ce qu'un
   formulaire inter-origine peut envoyer. Le ticket est créé en
   `Draft`.
2. `RunPickUp` (défaut `--ticket-pickup 5m`) prend le plus ancien
   brouillon dès qu'aucun ticket n'est actif et lance l'analyse.
3. `--ticket-default-agent claude` : le travail part à Claude Code, avec
   accès en écriture à une copie de travail du dépôt.
4. `--ticket-autopilot` (défaut **vrai**) valide le plan sans humain,
   puis **déploie** si la vérification de Jarvis passe et si la relecture
   accepte : fusion dans `main`, recompilation, essai à blanc,
   remplacement du processus.

Autrement dit : **une page web visitée pendant que Jarvis tourne peut
faire écrire, fusionner dans `main` et déployer du code décrit par un
tiers**, sans qu'aucun humain ne voie passer le ticket. L'attaque est
aveugle (l'attaquant ne lit pas les réponses) et doit franchir la
vérification complète et la relecture — mais la relecture est un modèle
local dont le jalon 36 a justement montré qu'il pouvait conclure
« acceptable » sur un diff faux, et un ticket d'apparence anodine passe
sans peine.

Le pilote automatique n'est pas le défaut : c'est une décision assumée.
Le défaut, c'est que **la file de tickets est ouverte en écriture depuis
n'importe quel site**.

**À faire tout de suite, sans écrire une ligne de code** (l'un ou l'autre
suffit à couper la chaîne à l'étape 2 ou 3) :

```
--ticket-pickup 0          # plus de relève automatique : un ticket injecté reste un brouillon inerte
--ticket-autopilot=false   # le plan attend la validation humaine
```

**Le correctif, petit et sans dépendance** : refuser toute méthode non
idempotente dont l'en-tête `Sec-Fetch-Site` n'est pas `same-origin` (à
défaut, contrôler `Origin`), plus un jeton de session dans les
formulaires. C'est le **jalon 42** : il ne dépend d'aucune décision de ce
document et vaut avant le premier user supplémentaire.

### 9.2 L'espace Admin doit être un rôle, pas un préfixe d'URL

`/admin/tests/run` exécute du code, `/admin/refresh` relit le dépôt, les
tickets écrivent dans une copie de travail git et un déploiement remplace
le processus. Un préfixe d'URL n'est pas un contrôle d'accès : il faut un
rôle vérifié **côté serveur, dans chaque handler** (jamais seulement dans
le template qui masque le lien).

Ce qui attend D5 sans risque : TLS, reverse proxy, limitation de débit.
Ce qui n'attend pas : CSRF, rôles, expiration de session, 404-pas-403.

---

## 10. Ressources physiques par environnement

### 10.1 L'inférence reste sérialisée, pour toujours
Les modèles de documents (~12 Go) et de code (~16 Go) ne tiennent pas
ensemble sur 24 Go : aucun découpage logiciel ne change ça. Donc
`gate.Gate` passe d'un sémaphore FIFO à une **file équitable par
environnement** (tourniquet) — sinon un document de 5 pages denses
(~18 min mesurées) bloque tous les autres environnements — avec priorité
et quota par environnement. `--vlm-url` / `--llm-url` deviennent des
réglages d'environnement, ce qui marche déjà : rien dans le code n'est
spécifique à `localhost`.

### 10.2 Une boîte mail par user
Identifiants **par user**, dans Atlas **chiffrés** (AES-GCM) avec une clé
qui n'y est jamais (`~/.jarvis/secret.key`, 0600, sur l'hôte). Atlas ne
détient que du chiffré ; la contrainte de CLAUDE.md devient « le mot de
passe de boîte n'est jamais stocké en clair hors de l'hôte », ce qui reste
vrai et vérifiable. Le tri reste fait par le modèle local.

### 10.3 Tickets, git et déploiement : un seul environnement privilégié
Un déploiement redémarre l'application de **tous** les environnements.
Retenu : Tickets, `/admin` et le déploiement n'existent que pour
l'**environnement propriétaire de l'instance**. Les autres ont Documents,
Emails, Notes, Tâches. À noter : l'essai à blanc du déploiement et la
relecture visuelle (jalons 30, 38) devront créer un **environnement
jetable**, pas seulement des collections `*_deploycheck` / `*_visualcheck`.

### 10.4 Dossiers
`--watch-dir`, `--out-dir`, `--work-dir`, `--worktrees-dir` deviennent
des racines par environnement (`<racine>/<env>/…`). Le dossier surveillé
en particulier est une porte d'entrée de documents : il doit appartenir à
un environnement **et** à un user, sinon ses documents n'ont pas d'auteur.

---

## 11. Migration des données existantes

Règle du jalon 9, telle quelle : **une migration est un acte de
déploiement délibéré, jamais une lecture qui triche.**

1. Créer l'environnement `local` et le user Julian (propriétaire), lié à
   son compte Google.
2. `jarvis migrate-tenancy` (`--dry-run` d'abord) estampille chaque
   `job`, `note`, `task`, `email`, `ticket` avec `env_id=local`, `owner`,
   `version=1`, et renomme les fichiers GridFS sous `local/…`.
3. **Les lectures refusent un enregistrement sans `env_id`**, avec un
   message qui dit quoi lancer — plutôt que de le traiter comme global,
   ce qui serait exactement la fuite qu'on veut rendre impossible.
4. Bump des versions de schéma et des empreintes, comme
   `internal/store/schema_norm_test.go` l'exige déjà.
5. Idempotente et relançable, comme `MigrateComments`.

---

## 12. Les normes (tests) qui tiennent le refactoring

1. `TestStores_AreIsolatedBetweenEnvironments` — contrat d'isolation
   imposé aux cinq stores, sur `FakeStore` **et** `MongoStore` (Atlas).
2. `TestRoutes_AllRequireSessionUnlessDeclaredPublic` — `chi.Walk` contre
   une liste blanche explicite.
3. `TestAdminRoutes_RequireOwnerRole` — le préfixe d'URL ne suffit pas.
4. `TestMutatingRoutes_RejectCrossSiteRequests` — la faille du §9.1 reste
   fermée : chaque route non idempotente refuse une requête sans origine
   propre, celle de création de ticket en tête.
5. `TestConcurrentWriters_DoNotLoseData` — les quatre bugs du §2.4
   rejoués à deux écrivains.
6. `TestOverlay_PendingOpsAreInvisibleToOtherUsers` — le contrat du
   changeset : visible de son auteur, invisible des autres, visible de
   tous après commit. Imposé à chaque type d'entité couvert.
7. `TestListQuery_MatchesAgreesWithMongo` — le prédicat Go du §7.3 et le
   filtre Mongo donnent le même résultat.
8. `TestBackgroundWork_NeverUsesOverlay` — `scope.Session == ""` écrit
   toujours dans la base.
9. `TestNoStoreIsReachableWithoutScope` — la garantie du §5.1.

---

## 13. Découpage en jalons

Ordre choisi pour qu'à aucun moment l'installation quotidienne ne soit
cassée, et pour que **les données soient cloisonnées avant que quiconque
d'autre puisse se connecter**.

| Jalon | Contenu | Livrable observable | Taille |
|---|---|---|---|
| **42** | **Contrôle d'origine** sur toute méthode non idempotente (§9.1) | Une page tierce ne peut plus créer de ticket ni lancer `go test` | **Petit, urgent** |
| **43** | `internal/tenancy` (Scope, EnvID, UserID, SessionID), `For(scope)` sur les cinq stores, un seul environnement `local` câblé en dur, aucun changement de comportement | L'application est identique, la couture existe, 867 tests verts | Moyen |
| **44** | Cloisonnement réel : `env_id` écrit et filtré partout, GridFS re-préfixé, contrat d'isolation, migration `local`, refus des enregistrements non estampillés | Un test prouve l'aveuglement entre environnements | **Gros** |
| **45** | Google OIDC (PKCE, redirection sur la boucle locale), `users`/`envs`/`session`, cookie, middleware, rôles, `/admin` par rôle, URL de secours du propriétaire, démarrage depuis le Dock préservé | Deux comptes Google réels dans deux environnements, chacun aveugle à l'autre | **Gros** |
| **46** | Journal (§8) : décorateur, page « ma session », historique d'une entité | « Qui a changé quoi », visible | Moyen |
| **47** | Versions et écritures conditionnelles (§8), écritures ciblées généralisées | Les quatre bugs historiques sont des tests verts | Moyen |
| **48** | Changeset **sur les notes et tâches seulement**, de bout en bout : `Op`, `Changeset`, overlay (prédicat extrait de `FakeStore`), commit/rebase, page Changements | Je modifie une note, personne ne le voit, je commite | **Gros** |
| **49** | Changeset étendu aux documents (tags, commentaire, type, suppression) et aux tickets | Le même cycle sur tout ce qui est éditable | Moyen |
| **50** | Ressources par environnement (§10) : file équitable et quotas, boîte mail par user chiffrée, dossiers par environnement, tickets/déploiement réservés au propriétaire | Deux environnements actifs sans se bloquer | Moyen |
| **51** | Gestion des environnements : création, invitations, changement d'environnement dans la nav, environnement jetable pour l'essai à blanc et la relecture visuelle | Julian crée un environnement et y invite quelqu'un | Moyen |
| *(différé)* | TLS, reverse proxy, limitation de débit — quand D5 changera | — | — |

Le 42 est devant parce qu'il ne dépend de rien et qu'il ferme une porte
ouverte aujourd'hui. Le 43 est délibérément un jalon « qui ne fait
rien » : c'est celui qui rend les suivants sûrs. Le 48 est découpé par
**type d'entité** et non par couche, pour qu'il soit complet et
utilisable sur son périmètre — du plus petit au plus grand, comme les 41
précédents.

---

## 14. Ce que ça changera dans CLAUDE.md

À écrire jalon par jalon, quand le code existera :

- Les exceptions 2, 3 et 4 (documents, notes, emails dans Atlas) parlent
  d'un utilisateur unique : elles deviennent « les données d'un
  environnement », et doivent dire ce qui cloisonne un environnement d'un
  autre.
- **Une exception nouvelle** : l'identité part chez Google à chaque
  connexion (§6). Ni document, ni contenu — mais une sortie de données
  vers un tiers, à documenter comme les quatre autres.
- La contrainte sur le mot de passe de la boîte mail, reformulée (§10.2).
- **Une contrainte nouvelle** : l'inférence locale est une ressource de
  machine, partagée et sérialisée entre environnements.
- « Tourne entièrement en local » devient « peut servir plusieurs users
  sur une machine locale » — D5 limite la portée du changement, mais ne
  l'annule pas.

## 15. Ce que je ne recommande pas

- **Un `env_id` ajouté à la main dans les handlers.** 78 handlers, 34
  accès Mongo : la discipline cédera une fois. Le §5.1 (store non scopé
  inaccessible) n'est pas un raffinement, c'est ce qui rend D1
  défendable.
- **Mettre les actions dans le changeset** (relancer une extraction,
  importer, lancer un agent). Effets hors base, rien à rebaser : elles
  s'exécutent tout de suite (§7.1).
- **Mettre les écritures de la machine dans le changeset.** Un commit qui
  retient un résultat d'OCR serait absurde, et le premier redémarrage le
  perdrait.
- **Commencer par l'authentification.** Un écran de connexion sur des
  données non cloisonnées donne l'illusion de l'isolement : 44 avant 45.
- **L'event sourcing** pour atteindre « pas de conflits ». Le grain du
  champ (§7.2) donne le même résultat pratique pour une fraction du coût.
