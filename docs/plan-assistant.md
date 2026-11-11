# Jarvis, assistant personnel — plan et décisions

Demandé (11/11/2026) : « Jarvis doit être mon assistant personnel, capable
de prendre toutes les tâches dont j'ai besoin. Une combinaison de modèle
local et de Claude : le modèle local pour les tâches simples, Claude en
orchestrateur. À jour 24 h/24, disponible sur mon iPhone, avec accès à
MongoDB pour stocker ses analyses ; je lui donnerai des connecteurs pas à
pas. Sur l'iPhone et le Mac, je veux lui parler comme à un humain. »
Puis : « Écris le plan, et tu peux commencer. Je te donne toute la liberté
dont tu as besoin. »

## Décisions prises sous cette délégation (à confirmer ou corriger)

L'utilisateur a délégué les choix. Ceux qui engagent ses données sont
listés ici pour qu'il puisse les défaire — aucun n'est irréversible.

| Sujet | Décision | Pourquoi | Comment la défaire |
|---|---|---|---|
| Ce que Claude peut lire | Notes, tâches, documents, emails — **seulement ce qu'il demande par un outil**, jamais un envoi en masse. | Un orchestrateur qui ne lit rien ne peut rien faire. | `--assistant-sources` (liste des sources ouvertes) ; `--assistant=false`. |
| Activation | **Désactivé tant qu'aucune clé d'API n'est fournie** (`~/.jarvis/assistant.json` ou `ANTHROPIC_API_KEY`). | Rien ne part sans un geste de l'utilisateur. | Retirer la clé. |
| Canal | **API d'Anthropic** (clé d'API, facturée à l'usage), pas le CLI `claude -p` des tickets. | Une conversation avec des outils de Jarvis demande la boucle d'outils de l'API ; le CLI est un agent de code. | Le port `claudeapi.Client` isole le choix. |
| Modèle | `claude-opus-5-5`, effort `medium`. | Défaut recommandé ; le modèle est une option. | `--assistant-model`, `--assistant-effort`. |
| Dépense | Plafond quotidien de jetons (2 M par défaut). | Un assistant 24 h/24 sans plafond est un chèque en blanc. | `--assistant-daily-tokens`. |
| Écritures | Les écritures internes (créer une tâche, retenir un fait) passent directement, journalisées. **Toute action vers l'extérieur demandera une confirmation** (jalon 58). | Une action interne se défait ; un email envoyé, non. | — |
| Dépendance | SDK officiel `anthropic-sdk-go`, derrière un port de Jarvis. | Recommandation d'Anthropic ; le port le rend remplaçable en un jour. | Réécrire `claudeapi.SDKClient`. |

Cela ouvre l'**exception n° 9** aux contraintes de CLAUDE.md (jusqu'ici :
« documents, emails, notes et identifiants ne sont jamais donnés à
Claude »). Les identifiants, eux, ne lui sont toujours jamais donnés.

## Forme cible

```
iPhone (PWA, Siri) ─┐
Mac (navigateur)    ─┼─ réseau privé ─▶ jarvisapp, sur une machine toujours allumée
                                          ├─ boucle de l'assistant (Claude, outils)
                                          ├─ outils = les services de Jarvis
                                          ├─ modèles locaux (file gate)
                                          └─ MongoDB Atlas
```

## Principes

1. **Claude décide, Go exécute.** La boucle est écrite dans Jarvis
   (`internal/assistant`) : chaque appel d'outil passe par du code testé,
   qui porte la portée (environnement, user), la journalisation et les
   plafonds. Claude n'a jamais d'accès direct à la base.
2. **Un connecteur = des outils + un niveau par outil.**

   | Niveau | Exemples | Comportement |
   |---|---|---|
   | Lecture | chercher une note, lire un email | libre |
   | Écriture interne | créer une tâche, retenir un fait | exécutée, journalisée |
   | Action extérieure | envoyer un email, passer un ordre, déployer | **confirmation sur le téléphone, toujours** |

3. **Ce que lit un outil est une donnée, jamais une instruction.**
   L'assistant lira des emails et des pages écrits par des tiers. Tant
   qu'il n'a que des outils de lecture et d'écriture interne, un texte
   piégé ne peut rien faire sortir ; dès qu'un outil agit vers
   l'extérieur, la confirmation humaine est le garde-fou — c'est pourquoi
   elle arrive **avant** le premier connecteur d'action.
4. **Modèle local = outil de l'orchestrateur.** Claude appelle `classify`,
   `extract`, `summarize` ; le modèle local fait la lecture en volume,
   dans la file `gate`. Par source, on pourra choisir : contenu brut,
   résumé local seulement, ou jamais.
5. **Historique en ajout seul.** Les messages de Claude sont rejoués tels
   quels (blocs de réflexion compris) : l'API le demande, et c'est déjà la
   règle du fil des tickets.
6. **Mémoire sans base vectorielle** (non-goal maintenu) : des faits
   courts, relus à chaque conversation, et la recherche de Jarvis par
   outils.

## Jalons

Les numéros 55 et 56 restent réservés à Polymarket
(`docs/plan-polymarket.md`).

- **57 — Cœur de l'assistant** *(fait)* : `internal/claudeapi` (port,
  client SDK, faux), `internal/assistant` (conversations dans Atlas,
  boucle d'outils, outils de lecture sur notes/tâches/documents/emails,
  `add_task`, mémoire, plafond quotidien), onglet **Assistant** (fil de
  discussion, étapes visibles, utilisable sur téléphone).
- **58 — Confirmations et identité** *(fait)* : une action met le tour en
  pause jusqu'au oui/non de l'utilisateur (l'attente survit à un
  redémarrage) ; les écritures de l'assistant sont signalées « via
  l'assistant » dans l'onglet Activité ; neuf outils de plus, dont deux
  actions à confirmer (`delete_task`, `create_ticket`).
- **59 — Délégation au modèle local** : outils `summarize_locally`,
  `extract`, `classify` ; politique par source (brut / résumé / jamais).
- **60 — Toujours allumé, à distance** : services `launchd`/`systemd` à la
  place du lanceur du Dock, Tailscale (`tailscale serve` donne le HTTPS que
  Google et les notifications exigent), authentification activée, PWA
  (manifeste, icône, mise en page téléphone de toute l'application).
- **61 — Proactivité** : un flux d'événements unique (email arrivé, tâche
  due, marché qui bouge, horaire) ; un événement peut lancer l'assistant ;
  notifications Web Push.
- **62 — Voix** : raccourci Siri (dicter → `POST` → réponse lue), puis
  `whisper.cpp` sur l'hôte pour la voix dans la PWA.
- **63+ — Connecteurs, un par un** : agenda d'abord ; client MCP en Go
  pour brancher des connecteurs existants sans les réécrire.

## Questions ouvertes pour l'utilisateur

1. **Hôte** : la machine dédiée (i9, 64 Go) à la maison, comme supposé ici ?
   Le MacBook Air se met en veille : il ne peut pas être le 24 h/24.
2. **Sources sensibles** : faut-il fermer certaines sources à Claude dès
   maintenant (pièces d'identité, par exemple) en attendant la politique
   par source du jalon 59 ?
3. **Clé d'API** : compte API Anthropic à créer (distinct de l'abonnement
   utilisé par le CLI des tickets).
