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
- **Jalon 3 — implémentation HTTP réelle du port VLM + câblage CLI : fait
  côté code.** Reste le test end-to-end contre un vrai serveur (voir
  ci-dessous).
  - `internal/vlm.HTTPClient` : implémente `Client` contre une API chat
    completions compatible OpenAI (le standard exposé par `llama.cpp
    server`). Testé via `httptest`, aucun modèle/GPU requis.
  - CLI : `jarvis parse --vlm-url URL --vlm-model NAME
    [--vlm-model-version V] [--dpi N] [--vlm-timeout D] <fichier.pdf>` —
    enchaîne triage puis, pour les pages sans texte fiable, rendu + VLM.
    `--vlm-url`/`--vlm-model` sans défaut silencieux (délibéré, pour la
    provenance).
  - **Modèle VLM choisi (argumenté, cf. échange avec l'utilisateur) :
    olmOCR-2-7B-1025 (AllenAI, fine-tune Qwen2.5-VL-7B), licence Apache
    2.0, quantization GGUF Q6_K (~6.25 Go), servi via `llama.cpp server`.**
    olmOCR-Bench ≈ 82.4, OmniDocBench ≈ 82.3 — meilleur score des 3
    candidats évalués (vs Nanonets-OCR-s 60.7 sur OmniDocBench, Qwen2.5-VL-3B
    sans score direct sur ces deux benchmarks). RAM machine : 24 Go,
    largement suffisant pour cette quantization.
  - **Test end-to-end réel : fait et validé**, contre un vrai
    `llama.cpp server` servant olmOCR-2-7B-1025 (Q6_K). Voir "Setup local
    du VLM" ci-dessous pour reproduire.
    - `native.pdf` (texte natif) : triage détecte `has_text_layer: true`,
      0 page envoyée au VLM — comportement attendu, coûteux en inférence
      évité.
    - `scanned_content.pdf` (nouvelle fixture, cf. corpus) : triage
      détecte `has_text_layer: false`, la page est rendue puis envoyée au
      VLM réel, qui retourne une transcription exacte du contenu source
      (`"Facture n. 2026-0042\nFournisseur: Acme SARL\nTotal: 123.45
      EUR"`), avec `model`/`model_version`/`prompt` correctement
      journalisés dans la sortie CLI.
    - Point notable : `scanned.pdf` (page vide, utilisée pour les tests
      de triage) fait dégénérer le VLM en génération répétitive sans
      jamais atteindre de token de fin — une page blanche n'a rien à
      décrire, ce n'est pas un bug du client HTTP. D'où l'ajout de
      `scanned_content.pdf` (image réaliste, point d'arrêt naturel) pour
      exercer le VLM correctement. Non bloquant pour la suite (les vrais
      documents ont du contenu), mais à garder en tête si jamais une page
      quasi-vide se présente en production : prévoir un timeout et
      accepter l'échec (cohérent avec la stratégie retenue).

### Corpus de fixtures (testdata/fixtures/, régénéré par
scripts/gen_fixtures.py)
- `native.pdf` — texte natif, 1 page.
- `scanned.pdf` — 1 page vide, aucun texte ni image (triage uniquement).
- `mixed.pdf` — 2 pages, texte natif + page vide.
- `scanned_content.pdf` — 1 page, image JPEG (rendu de native.pdf) sans
  texte natif ; simule une vraie page scannée avec du contenu à
  transcrire. Dépend de `pdftoppm` + `sips` (macOS) au moment de la
  génération uniquement — le fichier généré, lui, est indépendant de tout
  outil.

### Setup local complet (llama.cpp + olmOCR-2-7B-1025 + Qwen3-8B)
```
brew install llama.cpp   # fournit `llama-server`

# --- VLM (étage Parsing) ---
mkdir -p ~/models/olmocr2
BASE="https://huggingface.co/lmstudio-community/olmOCR-2-7B-1025-GGUF/resolve/main"
curl -L -o ~/models/olmocr2/olmOCR-2-7B-1025-Q6_K.gguf "$BASE/olmOCR-2-7B-1025-Q6_K.gguf"
curl -L -o ~/models/olmocr2/mmproj-olmOCR-2-7B-1025-F16.gguf "$BASE/mmproj-olmOCR-2-7B-1025-F16.gguf"

llama-server \
  -m ~/models/olmocr2/olmOCR-2-7B-1025-Q6_K.gguf \
  --mmproj ~/models/olmocr2/mmproj-olmOCR-2-7B-1025-F16.gguf \
  --host 127.0.0.1 --port 8080 \
  --ctx-size 8192 \
  --image-min-tokens 1024   # recommandé par llama.cpp pour les Qwen-VL (précision du grounding)

# --- LLM (étage Extraction), dans un second terminal, port différent ---
mkdir -p ~/models/qwen3-8b
curl -L -o ~/models/qwen3-8b/Qwen3-8B-Q5_K_M.gguf \
  "https://huggingface.co/unsloth/Qwen3-8B-GGUF/resolve/main/Qwen3-8B-Q5_K_M.gguf"

llama-server \
  -m ~/models/qwen3-8b/Qwen3-8B-Q5_K_M.gguf \
  --host 127.0.0.1 --port 8081 \
  --ctx-size 8192

# --- Pipeline complet, dans un troisième terminal ---
jarvis process \
  --vlm-url http://127.0.0.1:8080/v1 --vlm-model olmOCR-2-7B-1025 --vlm-model-version Q6_K \
  --llm-url http://127.0.0.1:8081/v1 --llm-model qwen3-8b --llm-model-version Q5_K_M \
  --doc-type facture \
  fichier.pdf

# Étages isolés, toujours disponibles si besoin :
jarvis triage fichier.pdf
jarvis parse --vlm-url http://127.0.0.1:8080/v1 --vlm-model olmOCR-2-7B-1025 fichier.pdf
```
Les poids (~13 Go au total pour les deux modèles) sont dans `~/models/`,
**hors du dépôt** (trop volumineux, non versionnables proprement).

Performances observées sur le MacBook Air M3 (24 Go), les deux serveurs
tournant simultanément :
- VLM (olmOCR-2-7B, Q6_K) : ~91 tokens/s en lecture de prompt, ~14.6
  tokens/s en génération.
- LLM (Qwen3-8B, Q5_K_M) : extraction JSON contrainte en quelques
  secondes une fois le modèle chargé (le premier appel inclut le
  chargement à froid, ~20-30s).
- Pipeline complet sur une page scannée (triage → VLM → LLM) : ~80s de
  bout en bout.
- **Jalon 4 — registre de types + dérivation JSON Schema + étage
  Extraction (fake LLM) : fait.**
  - `internal/schema` : `Field[T]{Value, Confidence, SourceSnippet}`
    (générique) porte la provenance de chaque valeur extraite —
    confiance + extrait source, comme exigé. `Derive(reflect.Type)
    (map[string]any, error)` dérive un JSON Schema par réflexion pure
    (aucune dépendance externe) : reconnaît `Field[T]` structurellement,
    gère struct/slice/pointeur(optionnel)/tags `json`+`desc`.
  - **Point ouvert assumé, pas résolu silencieusement : le bbox n'est PAS
    dans `Field[T]`.** Ni le texte de `internal/triage` (pdftotext texte
    simple) ni le Markdown de `internal/vlm` ne portent de coordonnées
    aujourd'hui. L'ajouter demande de faire évoluer ces deux étages
    (`pdftotext -bbox` pour le texte natif ; stratégie à définir côté
    VLM) — à traiter comme un jalon dédié si/quand nécessaire.
  - `internal/doctype` : `Registry` (générique, `Register[T]`), jeu de
    types **ouvert** (pas figé) — `Facture` enregistrée comme premier
    exemple ; ajouter "pièce d'identité", "correspondance", etc. ne
    touche ni `internal/schema` ni `internal/extraction`.
  - `internal/llm` : port `Client.Extract(ctx, ExtractRequest)
    (ExtractResult, error)` avec `FakeClient` (même forme que
    `internal/vlm.FakeClient`). Implémentation HTTP réelle (décodage
    contraint par JSON Schema via llama.cpp) : jalon 5.
  - `internal/extraction` : `LowConfidenceFields(json, seuil)` (fonction
    pure, marche récursive sur le JSON décodé, chemins du type
    `adresse.ville` / `lignes[1]`) + `Extractor.ExtractPages` (même
    forme que `parsing.Parser.ParsePages` : échec par page = marquage
    immédiat + continue, seule une annulation de contexte interrompt).
    Confiance basse = `NeedsReview`, **pas** un échec — distinct de
    `Failed` (erreur LLM ou JSON non conforme au schéma). Seuil par
    défaut 0.7 (`DefaultConfidenceThreshold`).
  - Pas de câblage CLI : comme pour le parsing au jalon 2, une commande
    `jarvis extract` sans vrai LLM branché n'aurait rien de significatif
    à faire. Arrivera au jalon 5.
- **Jalon 5 — implémentation HTTP réelle du port LLM + pipeline complet +
  câblage CLI : fait, validé end-to-end.**
  - `internal/llm.HTTPClient` : implémente `Client` contre l'API chat
    completions compatible OpenAI, avec `response_format: {"type":
    "json_schema", "json_schema": {...}}` (décodage contraint par le
    schéma dérivé de `internal/schema`) — extension supportée par
    llama.cpp server, confirmée empiriquement avant implémentation.
    Testé via `httptest`, aucun modèle/GPU requis.
  - **`internal/pipeline` (nouveau)** : comble un manque identifié au
    jalon 2 (le triage ne conservait que des stats, pas le texte).
    `Merge()` (fonction pure) combine le texte natif des pages "usable"
    et le Markdown VLM des autres pages en une liste unique triée par
    page ; une page non-usable dont le parsing VLM a échoué est exclue
    (pas de fallback silencieux). `Pipeline.Run()` enchaîne
    Triage → Parsing → Extraction en réutilisant tel quel chaque étage
    existant (aucun étage n'a eu besoin d'être modifié).
  - CLI : `jarvis process --vlm-url URL --vlm-model NAME --llm-url URL
    --llm-model NAME --doc-type NAME <fichier.pdf>` — pipeline complet,
    JSON sur stdout. Toujours pas de défaut silencieux sur les
    URLs/noms de modèle/doc-type.
  - **Modèle LLM d'extraction choisi (argumenté) : Qwen3-8B (Apache 2.0,
    GGUF Q5_K_M, ~5.85 Go), servi par une seconde instance
    `llama.cpp server` (port distinct du VLM).** Comparé à
    Qwen2.5-7B-Instruct (même famille que le VLM mais légèrement en
    retrait sur IFEval face à Llama3.1-8B) et Hermes-2-Pro-Mistral-7B
    (spécifiquement tuné JSON/function-calling, 81% JSON eval, mais base
    Mistral-7B-v0.1 plus datée, contexte 8192). Argument retenu : le
    décodage étant de toute façon contraint par notre JSON Schema (la
    validité JSON est garantie structurellement, pas besoin qu'un modèle
    soit "bon en JSON mode"), la qualité de raisonnement pour extraire
    les bonnes valeurs prime — d'où Qwen3-8B, génération la plus récente
    des trois.
  - **Test end-to-end réel : fait.** Les deux serveurs (`llama-server`
    VLM sur :8080, `llama-server` LLM sur :8081) tournent simultanément —
    ~13 Go de poids cumulés, confortable sur les 24 Go de la machine.
    - `native.pdf` : triage détecte le texte natif, `parsing: []` (VLM
      jamais appelé), extraction retourne les 3 champs de `Facture` avec
      confiance 1.0 et `source_snippet` exact, `needs_review: false`.
      ~59s (dominé par le chargement à froid du modèle LLM au premier
      appel).
    - `scanned_content.pdf` : triage détecte l'absence de texte, VLM
      transcrit fidèlement la page, extraction retourne les mêmes 3
      champs corrects à partir du Markdown VLM (pas du texte source
      original — validation que la chaîne complète fonctionne, pas
      seulement chaque étage isolément). ~82s.
  - Serveurs arrêtés proprement après le test (aucun processus résiduel).

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
- Stratégie bbox (position pixel dans la page) pour la provenance —
  voir jalon 4 ci-dessus.
- Schéma de sortie exact des futurs types de documents au-delà de
  Facture (pièce d'identité, correspondance, document technique...) —
  le mécanisme (registre + dérivation) est en place, chaque nouveau type
  s'ajoute au besoin.
