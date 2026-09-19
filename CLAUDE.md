# Jarvis — pipeline local d'extraction de données PDF

## Contexte
Pipeline Go qui tourne entièrement en local (aucune donnée ne sort de la
machine, aucun appel à une API cloud) et qui extrait des données structurées
depuis des PDF. Sortie attendue : JSON validé contre un schéma, avec
provenance (page + bbox + extrait source) pour chaque valeur extraite.

- Documents : ouvert (factures, pièces d'identité, correspondance, documents
  techniques, etc.) — pas un jeu fermé de types, donc l'étage Extraction doit
  reposer sur un registre de schémas par type extensible, pas une poignée de
  structs figées.
- Langues : français + anglais.
- Scan vs natif : mélange des deux — les deux chemins (triage direct /
  passage par le VLM) seront réellement exercés.
- Volume : variable / imprévisible — pas de rythme fixe à optimiser.
- Matériel cible : MacBook Air M3 (2024). **Conflit avec la contrainte
  "vLLM" ci-dessous** — pas de GPU NVIDIA/CUDA sur Apple Silicon, vLLM ne
  peut pas tourner tel quel. Voir "Décisions en attente".

## Contraintes non négociables
- Langage : Go. Stack web (si besoin, plus tard uniquement) : `net/http` +
  `chi` + `templ` + `HTMX`.
- Aucune donnée ne sort de la machine ; les modèles sont servis localement.
  **Exception explicite et unique** : voir "Stockage phase 2" plus bas —
  seuls les résultats extraits validés pourront, plus tard, être envoyés à
  MongoDB Atlas. Les PDF sources et toute donnée intermédiaire ne quittent
  jamais la machine.
- TDD strict : chaque paquet a ses tests avant son implémentation. Pas de
  code non testé.
- Construire à partir de primitives ; éviter les frameworks lourds et les
  dépendances qu'on ne pourrait pas remplacer en une journée.
- Reproductibilité : chaque résultat stocke le hash du document source, le
  nom et la version des modèles utilisés, et le prompt.
- Erreurs Go explicites, jamais de `panic` dans le chemin nominal.
- Chaque valeur extraite porte un score de confiance ; les valeurs sous le
  seuil sont marquées pour revue humaine, jamais acceptées silencieusement.
- Journaliser assez pour pouvoir rejouer un document et comprendre pourquoi
  un champ est sorti faux.

## Architecture (3 étages strictement séparés, testables isolément)

1. **Triage** — détecte si le PDF a déjà une couche texte exploitable
   (via pdftotext/pdfium). Score explicite, pas d'heuristique cachée dans une
   condition. Si oui, on saute l'OCR.
2. **Parsing** — pour les pages sans texte fiable : rendu page → PNG 200 DPI,
   puis appel à un modèle VLM de document servi localement (llama.cpp
   server, API compatible OpenAI) qui renvoie du Markdown/HTML fidèle au
   layout.
3. **Extraction** — LLM texte local (llama.cpp server), décodage contraint
   par JSON Schema (grammars/guided decoding). Le schéma est dérivé de
   structs Go, via un registre extensible par type de document.

Le code Go orchestre ; chaque modèle est un service HTTP exposé comme un
port Go (interface) avec une implémentation HTTP et une implémentation fake
pour les tests. **Aucun test ne doit nécessiter de GPU.**

## Non-goals
- Pas d'interface web tant que la chaîne CLI ne donne pas de bons résultats.
- Pas de base vectorielle, pas de RAG — c'est un problème d'extraction, pas
  de recherche.
- Choix de modèle toujours argumenté (licence, empreinte VRAM, scores
  OmniDocBench/olmOCR-Bench), jamais décidé unilatéralement.
- Pas de logique d'extraction en dur par regex dans l'étage 3 sans le
  signaler explicitement comme décision assumée.

## Process de travail
- Découpage en jalons livrables, du plus petit au plus grand. Chaque jalon
  est validé par l'utilisateur avant de passer au suivant.
- TDD strict : tests écrits avant l'implémentation pour chaque paquet
  (rouge → vert), commit + push à chaque jalon terminé.

## État des jalons
- **Jalon 1 — squelette CLI + étage Triage : fait.**
  - `internal/triage` : `TextExtractor` (port) avec `FakeExtractor` (tests
    unitaires, aucune dépendance externe) et `PdftotextExtractor` (impl
    réelle, testée séparément sous le tag `integration`, nécessite
    `pdftotext`/poppler-utils sur le PATH).
  - Score de triage explicite et configurable (`Thresholds` : caractères
    min/page, ratio imprimable, ratio de lettres, seuil document) — voir
    `internal/triage/score.go`.
  - CLI : `jarvis triage <fichier.pdf>` → JSON sur stdout
    (`has_text_layer`, `score`, `reasons`, détail par page).
  - Fixtures dans `testdata/fixtures/` (native/scanné/mixte), générées de
    façon déterministe par `scripts/gen_fixtures.py`.
  - `make test` (unitaire, aucun binaire externe requis) et
    `make test-integration` (nécessite `pdftotext`) passent tous les deux.
- **Jalon 2 — ports modèles + étage Parsing (fake VLM) : fait.**
  - `internal/vlm` : port `Client` (`ParsePage(ctx, PageImage) (ParseResult, error)`)
    avec `FakeClient` (résultats préconfigurés par page, enregistre les
    appels reçus pour les assertions de test). `ParseResult` porte déjà
    `Model` (nom+version) et `Prompt`, pour la reproductibilité — même en
    fake, le contrat est là. Implémentation HTTP réelle (llama.cpp) :
    jalon 3.
  - `internal/parsing` : port `Renderer` (rendu PNG d'une page) avec
    `FakeRenderer` (tests unitaires) et `PdftoppmRenderer` (impl réelle via
    `pdftoppm -singlefile`, testée sous tag `integration`).
    `PagesNeedingParsing(triage.Result) []int` relie triage → parsing
    (pages non "usable"). `Parser.ParsePages` orchestre rendu + VLM par
    page : un échec (rendu ou VLM) marque la page `Failed` avec son
    `Error` et n'interrompt pas les autres pages (conforme à la stratégie
    d'échec retenue) ; seule une erreur de niveau document (contexte
    annulé) interrompt le traitement.
  - DPI par défaut : 200 (imposé par le brief), configurable via
    `Parser.DPI`.
  - Pas de câblage CLI pour cette étage : sans serveur VLM réel branché,
    une commande `jarvis parse` n'aurait rien de significatif à faire.
    Elle arrivera au jalon 3 avec l'implémentation HTTP.
- Jalon 3 (à venir) : implémentation HTTP réelle du port VLM (llama.cpp
  server), test end-to-end triage→parsing sur le corpus, câblage CLI.

## Décisions tranchées
- Granularité des résultats : **un JSON par page** (pas de fusion
  automatique au niveau document pour l'instant).
- Stratégie d'échec d'étage : **échec immédiat**, pas de retry — le
  document/la page est marqué en échec et passe en revue humaine.
- Stockage (phase 1) : **fichiers JSON sur disque**, un par page, + logs.

- **Moteur de serving des modèles : llama.cpp server** (binaire natif,
  accélération Metal, API compatible OpenAI, JSON Schema via grammars).
  Remplace vLLM partout dans ce document et dans le code — vLLM ne tourne
  pas sur Apple Silicon (pas de CUDA). Les ports Go (interfaces VLM/LLM)
  restent inchangés : seule l'implémentation HTTP cible change de backend.
- **Stockage phase 2 : MongoDB Atlas, contrainte de confidentialité
  assouplie EXPLICITEMENT et seulement pour cet usage précis.** Portée de
  l'exception, telle qu'acceptée par l'utilisateur : seuls les **résultats
  extraits validés** (le JSON final) peuvent, plus tard, être synchronisés
  vers Atlas. Les **PDF sources et toute donnée intermédiaire (pages
  rendues en PNG, Markdown issu du VLM) ne quittent jamais la machine** et
  ne sont jamais envoyés à un service cloud. Toute inférence (VLM, LLM)
  continue de tourner en local via llama.cpp. Phase 1 reste 100% JSON sur
  disque ; la bascule vers Atlas est un jalon séparé, à valider
  explicitement le moment venu.

## Décisions en attente
- Schéma de sortie exact par type de document (dépend du registre de types
  à concevoir, cf. jalon dédié).
