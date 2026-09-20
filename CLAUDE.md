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
  **Exceptions explicites, toutes deux décidées par l'utilisateur, pas des
  fuites accidentelles :**
  1. **Inférence** (VLM, LLM) : toujours locale, sans exception — voir
     "Setup local complet" plus bas.
  2. **Interface web (`cmd/jarvisweb`, jalon 12) : les documents uploadés
     — PDF sources compris, pas seulement les résultats extraits — sont
     stockés dans MongoDB Atlas (cloud).** Décision explicite de
     l'utilisateur (élargit l'exception initiale, qui ne couvrait que les
     résultats), au nom de la simplicité de l'infrastructure ; l'utilisateur
     prévoit à terme plusieurs providers cloud européens + une copie locale
     à intervalle régulier (infrastructure non détaillée ici, à documenter
     quand elle sera précisée). La CLI (`jarvis`/`jarvis process`) et
     `internal/store` (JSON sur disque) restent, eux, 100% locaux et
     inchangés — cette exception ne concerne que le flux web.
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
- ~~Pas d'interface web tant que la chaîne CLI ne donne pas de bons
  résultats.~~ Levé : la CLI donne de bons résultats (jalons 1-7), une
  interface web a été demandée et construite (`cmd/jarvisweb`, cf.
  "Interface web" plus bas) sur le stack déjà prévu ici (net/http + chi +
  templ + HTMX).
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

### Interface web (cmd/jarvisweb)
```
# Une fois (regénère les templates après toute modif de .templ) :
go install github.com/a-h/templ/cmd/templ@latest   # ajoute $(go env GOPATH)/bin au PATH
make build-web

# Les deux serveurs llama.cpp du setup ci-dessus doivent tourner
# (VLM :8080, LLM :8081), puis (MONGO_URI = connexion Atlas, cf. jalon 12) :
./bin/jarvisweb \
  --vlm-url http://127.0.0.1:8080/v1 --vlm-model olmOCR-2-7B-1025 --vlm-model-version Q6_K \
  --llm-url http://127.0.0.1:8081/v1 --llm-model qwen3-8b --llm-model-version Q5_K_M \
  --mongo-uri "$MONGO_URI" \
  --out-dir ./data/results \
  --addr 127.0.0.1:8090

# Puis ouvrir http://127.0.0.1:8090 — upload d'un PDF, résultat affiché
# dès qu'il est prêt (poll HTMX automatique, pas de rechargement manuel).
```
Flags utiles : `--mongo-uri` (**requis**, jalon 12 — les jobs, PDF source
compris, sont persistés dans MongoDB), `--mongo-db`/`--mongo-collection`
(défauts `jarvis`/`jobs`), `--work-dir` (fichiers temporaires de
traitement, vide = répertoire temporaire système), `--out-dir` (copie
locale additionnelle facultative des résultats, comme pour `jarvis
process` — MongoDB reste la source de vérité), `--dpi`/`--vlm-timeout`/
`--llm-timeout` (mêmes défauts que la CLI). `make run-web` lance tout
avec les valeurs par défaut du setup ci-dessus (`MONGO_URI` doit être
exporté dans l'environnement).

**Hébergement plus tard :** le binaire est un serveur `net/http`
standard. Pour un déploiement distant, pointer `--vlm-url`/`--llm-url`
vers les serveurs `llama.cpp` alors accessibles et exposer `--addr`
derrière un reverse proxy (TLS, auth) — rien dans le code n'est
spécifique à `localhost`.
- **Jalon 4 — registre de types + dérivation JSON Schema + étage
  Extraction (fake LLM) : fait.**
  - `internal/schema` : `Field[T]{Value, Confidence, SourceSnippet}`
    (générique) porte la provenance de chaque valeur extraite —
    confiance + extrait source, comme exigé. `Derive(reflect.Type)
    (map[string]any, error)` dérive un JSON Schema par réflexion pure
    (aucune dépendance externe) : reconnaît `Field[T]` structurellement,
    gère struct/slice/pointeur(optionnel)/tags `json`+`desc`.
  - Bbox : voir jalon 7 ci-dessous — pas fait à ce stade-ci de l'historique
    (implémenté ensuite, jalons 1-6 sont restés dans l'ordre où ils ont
    été livrés).
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
- **Jalon 6 — stockage définitif : fait.**
  - `internal/store` :
    - `HashFile(path)` — SHA-256 du document source (provenance).
    - `BuildRecords(hash, path, docType, processedAt, pipeline.Result)`
      (fonction pure) assemble un `DocumentRecord` (résumé) et un
      `[]PageRecord` (un par page) à partir du résultat du pipeline —
      chaque `PageRecord` porte sa source (native/vlm), le modèle et le
      prompt de chaque étage traversé, le JSON extrait, et
      `needs_review`/`failed`.
    - `WriteRecords(dir, doc, pages)` écrit
      `dir/<hash>/document.json` + `dir/<hash>/page-<N>.json` (JSON
      indenté, lisible). Un rejeu du même document écrase ces fichiers —
      c'est l'état courant, pas l'historique.
    - `AppendRunLog(dir, hash, entry)` — **log de rejeu append-only**,
      `dir/<hash>/runs.jsonl` : une ligne par exécution
      (timestamp, pages totales/en revue/en échec), jamais écrasée,
      pour garder trace des tentatives successives sur un même document
      (utile si on rejoue avec un autre modèle/prompt).
  - CLI : `jarvis process ... --out-dir DIR` (optionnel, vide par défaut
    = comportement inchangé, stdout uniquement). Persistance faite
    seulement après un `pipeline.Run` réussi — un échec de niveau
    document n'a pas de résultat cohérent à écrire.
  - Testé : unitaire (fonctions pures + écriture disque via `t.TempDir()`)
    et intégration CLI (deux serveurs `httptest` en mémoire simulant VLM/
    LLM, aucun vrai modèle requis) + démo manuelle inspectée à l'œil.
  - Pas d'index cross-documents (ex. SQLite) : hors scope de ce jalon,
    et de toute façon prévu côté Atlas phase 2 si besoin (cf. plus bas).
- **Jalon 7 — bbox pour les pages à texte natif : fait (portée
  partielle, assumée).**
  - `internal/bbox` : port `Extractor.ExtractWords` avec `FakeExtractor`
    (tests) et `PdftotextBBoxExtractor` (réel, via `pdftotext -bbox` —
    XML poppler, mots positionnés en points PDF, indépendant du DPI de
    rendu). `FindSnippetBBox(words, snippet)` (fonction pure) localise le
    rectangle englobant d'un `source_snippet` : correspondance exacte
    d'une séquence contiguë de mots normalisés en priorité, repli sur
    l'enveloppe des tokens trouvés individuellement si la séquence exacte
    échoue, `ok=false` si rien n'est trouvé (jamais de bbox inventé).
  - `internal/extraction.AttachBBoxes(json, words)` enrichit le JSON déjà
    produit par le LLM (ajoute `"bbox"` à chaque champ `schema.Field` dont
    le snippet a été localisé) — n'est **pas** dans le JSON Schema envoyé
    au LLM : le LLM ne connaît pas les coordonnées, seul le texte qu'il a
    recopié permet de les retrouver après coup.
  - `pipeline.Pipeline.BBox` (optionnel, `nil` = désactivé) : enrichit
    uniquement les pages `SourceNative` (texte natif). Toute erreur
    (extracteur indisponible, snippet introuvable) dégrade
    silencieusement vers "pas de bbox" plutôt que de faire échouer le
    pipeline — cohérent avec le principe "bonus de provenance, jamais
    bloquant".
  - CLI : câblé par défaut dans `jarvis process` (`PdftotextBBoxExtractor{}`).
  - **Portée assumée, pas cachée : les pages passées par le VLM (scannées)
    n'ont toujours pas de bbox.** Un mot positionné suppose une couche
    texte native ; une page scannée n'en a pas par définition. Deux pistes
    pour combler ce trou plus tard, ni l'une ni l'autre entreprise ici :
    (a) une passe OCR dédiée (ex. Tesseract) sur le PNG déjà rendu pour
    l'étage Parsing, uniquement pour obtenir des coordonnées de mots (le
    VLM resterait la source du texte/Markdown) ; (b) un VLM "grounding"
    capable de renvoyer des coordonnées dans sa sortie (peu fiable pour
    la plupart des modèles document actuels, olmOCR-2 inclus). Décision à
    prendre si le besoin se présente sur un vrai document scanné.
  - Testé : unitaire (matching pur, fakes) et intégration (vrai
    `pdftotext -bbox` sur les fixtures, y compris le cas page vide) +
    bout en bout réel confirmé sur `native.pdf` via le test CLI existant
    (`cmd/jarvis/process_store_integration_test.go`, avec un LLM stubé
    mais un vrai `pdftotext`) : le bbox de `numero` est correctement
    localisé et persisté dans `page-1.json`.
- **Jalon 8 — interface web (`cmd/jarvisweb`) : fait, validé end-to-end.**
  Stack tel que prévu dans ce document depuis le début : `net/http` +
  `chi` + `templ` + `HTMX`. Tourne en local aujourd'hui, pensé pour être
  hébergé tel quel plus tard (pas de dépendance à l'environnement local
  au-delà des URLs VLM/LLM passées en flags).
  - `internal/webapp` : `JobManager` suit les jobs en mémoire (perdus au
    redémarrage — acceptable, les résultats eux sont sur disque via
    `internal/store`). `Runner` est l'interface minimale
    (`Run(ctx, reg, path) (pipeline.Result, error)`) — `pipeline.Pipeline`
    la satisfait déjà par typage structurel, aucun adaptateur nécessaire.
    Chaque soumission tourne dans sa propre goroutine ; `Get`/`Submit`
    retournent des copies (jamais le pointeur interne) pour éviter tout
    accès concurrent aux champs d'un job en cours de traitement — un vrai
    data race a été détecté par `-race` et corrigé pendant le
    développement (copie prise avant tout partage entre goroutines,
    plutôt qu'après).
  - `cmd/jarvisweb` : formulaire d'upload (`GET /`), soumission
    (`POST /jobs`), suivi (`GET /jobs/{id}`). Poll HTMX : le fragment
    "pending"/"running" se ré-interroge lui-même
    (`hx-trigger="load delay:1.5s"` sur l'élément qu'il remplace via
    `hx-swap="outerHTML"`) ; le fragment terminal (`done`/`failed`) ne
    porte plus cet attribut, donc le polling s'arrête naturellement.
    L'affichage des champs extraits est **générique** : chaque page est
    aplatie en `FieldView` via `extraction.IsFieldNode` (exporté depuis
    `internal/extraction`, déjà utilisé par `LowConfidenceFields` et
    `AttachBBoxes` — pas une troisième implémentation de la même
    détection), donc un nouveau type de document enregistré s'affiche
    sans toucher au web.
  - HTMX vendoré localement (`cmd/jarvisweb/static/htmx.min.js`, embarqué
    dans le binaire via `//go:embed`) : aucun appel réseau au chargement
    de la page, cohérent avec "tourne entièrement en local".
  - Persistance optionnelle (`--out-dir`) : réutilise exactement
    `internal/store` (mêmes `document.json`/`page-N.json`/`runs.jsonl`
    que `jarvis process --out-dir`) via `JobManager.OnFinish`.
  - Testé : `internal/webapp` unitaire (dont concurrence, `-race`) ;
    `cmd/jarvisweb` unitaire (handlers via `httptest`, upload
    multipart, polling jusqu'à l'état terminal, doc-type inconnu,
    fichier manquant) ; **bout en bout réel** : serveur `jarvisweb`
    lancé en local, upload de `native.pdf` par `curl -F`, poll jusqu'à
    `status-done`, champs corrects affichés avec confiance/extrait
    source/bbox, persistance sur disque vérifiée. Une première tentative
    avec une mauvaise URL de serveur LLM a été observée dans
    `runs.jsonl` à côté de la tentative réussie — validation involontaire
    mais bienvenue du log de rejeu (jalon 6).
  - Setup et usage : voir "Interface web" ci-dessous.
- **Jalon 9 — normes de code testées, migrations de structs persistées,
  embedding comme mécanisme d'héritage : fait.**
  - **Norme = un test.** Chaque règle d'architecture qu'on veut faire
    respecter s'écrit comme une fonction `Test...` normale (`go test
    ./...` les fait tourner avec le reste) plutôt que comme un outil ou
    une CI séparée — cohérent avec "primitives, pas de dépendances
    lourdes". Deux normes en place aujourd'hui, dans
    `internal/store/schema_norm_test.go` :
    1. `TestDocumentRecordSchema_ChangeRequiresMigration` /
       `TestPageRecordSchema_ChangeRequiresMigration` — un fingerprint
       (`reflect`, noms+types des champs exportés, embeddings aplatis
       comme `encoding/json`) de la forme courante de chaque struct
       persisté est comparé à une valeur enregistrée pour
       `CurrentDocumentRecordVersion`/`CurrentPageRecordVersion`. Un
       changement de forme sans bump de version fait échouer le test avec
       un message qui dit précisément quoi faire (incrémenter la
       constante, ajouter le fingerprint, enregistrer une Migration).
       **Vérifié en conditions réelles** : un champ ajouté sans bump de
       version pendant le développement a bien fait échouer le test avec
       ce message, avant d'être retiré.
    2. `TestDocumentRecord_EmbedsRecordMeta` /
       `TestPageRecord_EmbedsRecordMeta` — vérifie que l'embedding décrit
       ci-dessous n'a pas été défait par erreur.
  - **Mécanisme de migration** (`internal/store/migration.go`) — répond
    aussi à "lier des fonctions à une struct" : un registre
    `map[int]Migration` par struct persisté (`documentMigrations`,
    `pageMigrations`), chaque `Migration{Description, Apply}` associant
    une version de départ à sa fonction de conversion.
    `Description` est obligatoire (`TestMigrations_HaveNonEmptyDescriptions`)
    — c'est le "quoi faire" en langage humain exigé par le brief.
    `RecordMeta.SchemaVersion` (voir embedding) est, **par fichier**,
    l'équivalent de ce que le brief décrit pour une base partagée
    (stocker quel code/schéma a produit un enregistrement) — quand la
    bascule Atlas (jalon séparé, toujours en attente) arrivera, le même
    principe s'y transpose : une collection dédiée stockant la version de
    schéma appliquée, migrée par un job explicite, pas à la volée.
  - **Migration = acte de déploiement délibéré, jamais une lecture qui
    triche.** `ReadDocumentRecord`/`ReadPageRecord` **refusent** un
    enregistrement dont `schema_version` est en retard (erreur explicite
    "lance `jarvis migrate` d'abord") plutôt que de le migrer en
    silence. Seule la commande `jarvis migrate --out-dir DIR [--dry-run]`
    réécrit les fichiers — décision explicite de l'utilisateur, cf.
    échange sur ce jalon.
  - **Héritage → embedding.** Go n'a pas d'héritage ; l'équivalent
    idiomatique est la composition par embedding (promotion de champs,
    pas de "super()" ni de dispatch polymorphe au-delà des interfaces).
    `RecordMeta{SourceHash, SourcePath, DocType, ProcessedAt,
    SchemaVersion}` est embeddée dans `DocumentRecord` et `PageRecord`,
    qui dupliquaient ces cinq champs avant ce jalon. `encoding/json`
    aplatit les champs d'un embedding anonyme sans tag propre : **le
    format JSON sur disque n'a pas changé** (vérifié en écrivant puis
    inspectant un enregistrement). Portée volontairement limitée à
    `internal/store`, où la duplication existait réellement — pas de
    base spéculative pour les futurs types de `internal/doctype` tant
    qu'un vrai besoin ne s'est pas montré.
  - Testé : unitaire pour chaque morceau (fingerprint, migration,
    lecture, embedding) + preuve empirique que chaque norme échoue
    vraiment sur une vraie violation (struct modifié sans bump de
    version) avant d'être validée à l'état correct.
  - **Ajouter une nouvelle norme plus tard** : une fonction `Test...`
    dans le paquet concerné (pas de paquet `archtest` séparé tant qu'une
    seule norme ne suffit pas à en justifier un), qui échoue avec un
    message actionnable. Portée actuelle : uniquement les deux normes
    ci-dessus (décision explicite de l'utilisateur) — pas de norme sur
    les ports (ex. "chaque interface a un Fake") pour l'instant.
- **Jalon 10 — corpus de factures synthétiques réalistes + validation
  bout en bout : fait.** Toujours 100% synthétique (aucune vraie facture
  d'aucune entreprise — ni légalement ni techniquement accessible ici),
  mais pensé pour stresser le pipeline bien au-delà des fixtures
  minimales des jalons précédents (3 lignes de texte).
  - **Corpus** (`scripts/gen_fixtures.py`, généré dans
    `testdata/fixtures/`) :
    - `facture_multiligne.pdf` — page dense, tableau de lignes (4
      articles, colonnes avec traits), trois montants proches (Total HT/
      TVA/Total TTC).
    - `facture_multipage.pdf` — 2 pages : numéro+fournisseur page 1,
      tableau+total page 2, **aucun champ commun aux deux pages**
      (délibéré, voir finding ci-dessous).
    - `facture_scannee_realiste.pdf` — rendu image de
      `facture_multiligne.pdf`, légèrement pivoté (~2.5°) via une
      matrice de transformation PDF (`cm`) plutôt qu'un outil externe —
      simule un scan pas parfaitement droit.
    - `facture_ambigue.pdf` — 5 montants proches (sous-total, remise,
      base TVA, TVA, total TTC) : teste la désambiguïsation.
    - `facture_format_europeen.pdf` — montant au format `1 234,56 EUR`
      (espace milliers, virgule décimale).
  - **Tests déterministes ajoutés** (triage/bbox/parsing, aucun modèle
    requis) : classification native/scanné correcte sur tout le corpus,
    mots positionnés extraits sur la page dense avec `Total TTC` et
    `Total HT` résolus à des positions distinctes, rendu PNG réussi sur
    toutes les pages y compris la version pivotée. La séparation des
    champs entre les deux pages de `facture_multipage.pdf` est vérifiée
    explicitement (prémisse du finding ci-dessous).
  - **Validation réelle** (les deux serveurs locaux, comme aux jalons
    3/5/8) — trois findings, aucun caché :
    1. **Désambiguïsation : correcte.** `facture_multiligne.pdf` →
       `total_ttc = 215.64` (pas 179.70 ni 35.94). `facture_ambigue.pdf`
       (5 montants candidats) → `total_ttc = 570.00`, le bon, avec
       confiance 1.0.
    2. **Champ absent de la page : correctement signalé, jamais
       inventé.** Sur `facture_multipage.pdf` page 1 (pas de total),
       `total_ttc` revient avec `confidence: 0`, `source_snippet: ""`,
       `needs_review: true` — le mécanisme de confiance fonctionne
       exactement comme conçu sur un vrai cas de champ manquant, pas
       seulement en test unitaire.
    3. **Limite architecturale confirmée : "un JSON par page" ne relie
       pas les champs entre pages.** Sur ce document, le résultat page 1
       a numéro+fournisseur mais pas le total ; le résultat page 2 (dont
       l'extraction a échoué par ailleurs, cf. finding 4) aurait eu le
       total mais pas le numéro/fournisseur. Aucun mécanisme actuel ne
       fusionne les deux en un enregistrement Facture complet. Attendu
       compte tenu de la décision jalon 1, mais maintenant observé sur
       un cas réel plutôt que déduit en théorie — **décision à
       reprendre si des documents réels ont ce problème** (options :
       fusionner les extractions de toutes les pages d'un document avant
       un unique appel LLM, ou agréger après coup les champs non-nuls
       across pages).
    4. **Qwen3-8B peut générer un nombre de tokens très élevé et très
       variable avant de conclure, avec un schéma JSON contraint pourtant
       minuscule (3 champs).** `facture_multiligne.pdf` : 636 tokens
       générés, ~40s. `facture_ambigue.pdf` : 2614 tokens générés,
       ~200s, pour arriver à la même réponse en 3 champs — a fait
       expirer deux tentatives (90s puis 240s) avant d'aboutir avec un
       timeout de 600s. Explication la plus probable : le "thinking
       mode" de Qwen3 (activé par défaut dans son template de chat)
       n'est pas désactivé par `internal/llm.HTTPClient`, et la
       génération de raisonnement semble davantage s'étendre sur un
       contenu ambigu (5 montants candidats) que sur un contenu
       simplement dense (`facture_multiligne.pdf`, qui n'a qu'un seul
       total plausible malgré 3 montants affichés). Non corrigé dans ce
       jalon (pas demandé) — piste pour plus tard : désactiver le mode
       réflexion (`enable_thinking: false` ou équivalent
       `/no_think` selon ce que le template llama.cpp expose pour Qwen3)
       dans `HTTPClient`, ou relever `--llm-timeout` par défaut pour
       tolérer la variance observée.
  - Serveurs arrêtés proprement après chaque run (aucun processus
    résiduel).
- **Jalon 11 — correctifs des findings 3 et 4 du jalon 10 : fait, validé
  end-to-end sur les fixtures qui avaient révélé les problèmes.**
  - **Finding 4 (latence Qwen3) — confirmé et corrigé.** La piste notée
    au jalon 10 (`/no_think`) a été vérifiée empiriquement avant
    implémentation : même requête, même réponse correcte, 2614 tokens/
    ~200s → 156 tokens/~10s. `llm.HTTPClient.DisableThinking bool`
    (mécanisme générique, off par défaut, testé isolément) ; activé par
    défaut dans `cmd/jarvis`/`cmd/jarvisweb` puisque Qwen3 est le modèle
    d'extraction retenu. `--llm-timeout` par défaut relevé 120s → 180s
    en filet de sécurité (le vrai correctif reste `/no_think`).
    **Revalidé sur `facture_ambigue.pdf` via `jarvis process` réel :
    ~12s au lieu d'un timeout après 600s, même réponse (570.00).**
  - **Finding 3 (champs éclatés entre pages) — corrigé par fusion
    post-extraction (option retenue par l'utilisateur).** `un JSON par
    page` reste inchangé ; `extraction.MergePages(results, threshold)`
    ajoute une vue agrégée : pour chaque champ (`schema.Field`, y
    compris imbriqué, via `IsFieldNode`), garde la valeur à la
    confiance la plus haute à travers les pages. `pipeline.Result.
    Merged` (nouveau champ, calculé dans `Pipeline.Run`) ; surfacé dans
    la sortie `jarvis process` (`"merged"`) et dans l'UI web (section
    "Résumé document" avant le détail par page). Persisté dans
    `store.DocumentRecord.MergedExtraction` — **premier vrai exercice du
    mécanisme de migration du jalon 9**, pas seulement testé en
    isolation : `CurrentDocumentRecordVersion` 1 → 2, fingerprint mis à
    jour, `Migration` enregistrée (purement additive : un enregistrement
    v1 n'a pas la clé `merged_extraction`, elle décode simplement à nil).
    Le test de norme a échoué comme prévu avant la mise à jour du
    fingerprint, confirmant qu'il détecte vraiment les changements non
    accompagnés. **Revalidé sur `facture_multipage.pdf` via `jarvis
    process` réel : `merged.json` a désormais les 3 champs (numéro +
    fournisseur de la page 1, total de la page 2), `needs_review:
    false`.**
  - Option non retenue documentée pour mémoire : un seul appel LLM par
    document (texte concaténé) aurait réglé le problème plus
    directement mais serait revenu sur la décision "un JSON par page"
    et risquait le dépassement de contexte sur un document long.
  - Testé : unitaire pour chaque morceau (DisableThinking, MergePages y
    compris récursif sur des champs imbriqués, persistance,
    fingerprint/migration) + les deux revalidations réelles ci-dessus.
- **Jalon 12 — jobs web persistés dans MongoDB (`cmd/jarvisweb`) : code
  préparé et testé sans base réelle, connexion Atlas à valider dès
  l'accès donné par l'utilisateur.**
  - **Portée de l'exception vie privée** : voir "Contraintes non
    négociables" en tête de document — décision explicite de
    l'utilisateur, documents sources compris, uniquement pour le flux
    web. CLI et `internal/store` inchangés.
  - `internal/webapp.Store` : nouveau port
    (`Create`/`Get`/`Update`), même principe que tous les autres ports du
    projet. `FakeStore` (en mémoire, mutex) pour tous les tests métier —
    répond directement à la question de l'utilisateur ("comment mocker
    Mongo ?") : on ne mocke jamais le driver, on mocke notre interface.
    `MongoStore` (driver officiel `go.mongodb.org/mongo-driver/v2`) pour
    la prod — connecté et `Ping`é dès la construction (`NewMongoStore`),
    pour échouer au démarrage plutôt qu'au premier job. `Update` ne
    réécrit que statut/résultat/erreur via `$set` (jamais `Content`,
    déjà en base depuis `Create` — évite de retransmettre le PDF source
    à chaque transition de statut).
  - `Job.Path` (chemin déjà écrit sur disque) devient `Job.Content
    []byte` (les octets du PDF, persistés tels quels). Le pipeline reste
    basé sur des chemins de fichiers (`pdftotext`/`pdftoppm` en ont
    besoin) : `JobManager` matérialise `Content` dans un fichier
    temporaire sous `WorkDir` le temps du traitement, puis le supprime
    systématiquement (succès ou échec).
  - `cmd/jarvisweb` : upload lu en mémoire (plus d'écriture disque à la
    réception) ; `--mongo-uri` requis (mode local supprimé, décision
    explicite de l'utilisateur — "remplace complètement"),
    `--mongo-db`/`--mongo-collection` avec défauts raisonnables.
    `--out-dir` reste disponible comme **copie locale additionnelle**
    (`internal/store`, inchangé) — MongoDB est la source de vérité, la
    copie locale un filet de secours optionnel.
  - **Non testé contre une vraie base à ce stade** — `MongoStore` compile
    et son code a été relu avec soin (API du driver v2 vérifiée via `go
    doc`, pas devinée), mais seul `go run` contre une URI invalide a été
    vérifié (échec rapide et clair, cf. ci-dessous). Tests d'intégration
    réels et vérification bout en bout à faire une fois l'accès Atlas
    donné.
  - Un vrai data race a été détecté par `-race` et corrigé pendant le
    développement — dans le code de **test** cette fois (deux jobs
    soumis au même `fakeRunner` partagé, écriture concurrente non
    synchronisée d'un champ de suivi) : corrigé avec un mutex sur la
    fake, cohérent avec la discipline déjà appliquée au `JobManager`
    lui-même au jalon 8.
  - Testé : `FakeStore` (contrat Create/Get/Update), `JobManager`
    entièrement revu (matérialisation du contenu, nettoyage du fichier
    temporaire y compris en cas d'échec de traitement, résilience à un
    échec de `Store.Update` intermédiaire), handlers HTTP (upload en
    mémoire, `Store` remplacé par une fake). `go build` réel de
    `jarvisweb` + lancement contre une URI Mongo injoignable : échoue
    immédiatement avec un message explicite plutôt qu'un timeout
    silencieux ou un crash.

## Atelier de code (cmd/codebrowser) — travail parallèle, outil de
développement
Demandé explicitement par l'utilisateur en parallèle du jalon 12 : "une
page en local qui me permet de naviguer dans le code et dans les tests
en mode Smalltalk / Glamorous Toolkit" — navigateur de classes (types,
héritage, méthodes) + page de tests par catégorie, exécutables
localement. **Sa page de contrôle de la qualité de code et de
l'architecture logicielle**, distincte du pipeline d'extraction PDF
lui-même : aucun modèle VLM/LLM requis, aucune donnée du pipeline
touchée, pure analyse statique + exécution de `go test`.
- `internal/codemap` : `Analyze(dir string, patterns ...string)
  (*Model, error)` via `go/packages` + `go/types` (aucun code du module
  analysé n'est exécuté). Construit, pour chaque type déclaré de chaque
  package chargé : ses champs, ses méthodes **déclarées directement**
  (pas les méthodes promues par embedding — se retrouvent en suivant
  `Embeds`, exactement comme dans un vrai navigateur de classes),
  `Embeds`/`EmbeddedBy` (l'équivalent Go de "superclasses"/
  "sous-classes", cf. jalon 9) et `Implements`/`ImplementedBy` (calculé
  par `types.Implements` sur toutes les paires type-concret/interface du
  jeu chargé, valeur et pointeur).
- `internal/testmap` : `Discover(moduleDir string) ([]Category, error)`
  — parcourt les `*_test.go` (`go/parser`, pas de type-checking,
  suffisant pour repérer des fonctions `TestXxx(*testing.T)`), une
  catégorie par package. Marque chaque test unitaire/integration selon
  la contrainte de build de son fichier. **Piège rencontré et corrigé** :
  `ast.CommentGroup.Text()` retire silencieusement les commentaires-
  directives (`//go:build ...`) de son résultat — il faut lire
  `cg.List[i].Text` (le commentaire brut) pour les détecter, pas
  `cg.Text()`. Détecté par un test qui échouait vraiment
  (`TestDiscover_FindsKnownTestsInRealModule`), pas trouvé par relecture.
- `internal/testrunner` : `Run(ctx, Options) ([]PackageResult, error)`
  exécute `go test -json` (format sondé empiriquement avant d'écrire le
  parseur) et agrège les événements JSONL en résultats structurés par
  package puis par test (statut, durée, sortie complète en cas
  d'échec). Un échec de test n'est pas une erreur de `Run` (distingué
  via `*exec.ExitError`) — seul un échec de l'outillage lui-même
  (`go` introuvable, sortie illisible) en est une.
- `cmd/codebrowser` : serveur `net/http` + `chi` + `templ` + `HTMX`,
  même stack que `cmd/jarvisweb`. Le `Model` (types) et les `Category`
  (tests) sont calculés une fois au démarrage puis mis en cache dans
  `Server` (protégé par un `sync.RWMutex`) — `go/packages.Load` re-type-
  check tout le module à chaque appel, trop lent pour le refaire à
  chaque page vue. Bouton "↻ Rescanner" (`POST /refresh`) pour recalculer
  explicitement après une modification du code. Les derniers résultats
  de tests connus sont conservés à travers un rescan (rescanner le code
  ne relance pas les tests).
  - `GET /classes` : liste des packages/types (barre latérale) + détail
    du type sélectionné (champs, méthodes, `Embeds`/`EmbeddedBy`,
    `Implements`/`ImplementedBy`, chacun cliquable en HTMX vers
    `GET /classes/detail?pkg=...&name=...`).
  - `GET /tests` : une carte par catégorie (package), liste des tests
    avec statut (jamais lancé / pass / fail), durée, et pour un échec la
    trace complète affichée en place. Boutons "Lancer" par test, par
    catégorie, et "Tout lancer" (avec un variant "avec integration"
    ajoutant `-tags=integration`) — tous via `POST /tests/run` avec
    `pkg`/`name`/`integration` en query, HTMX remplace uniquement le
    fragment concerné (une ligne, une carte, ou toute la liste).
  - `--module-dir` (vide = répertoire courant), `--addr` (défaut
    `127.0.0.1:8091`, distinct de `jarvisweb` sur `:8090`).
- **Validé manuellement, bout en bout, serveur réel lancé en local** :
  `GET /classes/detail?...name=DocumentRecord` affiche correctement
  `RecordMeta` sous "Embed" (la relation d'héritage du jalon 9, visible
  dans le navigateur) ; `POST /tests/run?pkg=...` (catégorie) et
  `POST /tests/run?pkg=...&name=...` (test unique) exécutent vraiment
  `go test` et renvoient un statut `pass` correct pour des tests connus
  bons.
- Usage :
  ```
  make run-codebrowser       # ou : make build-codebrowser && ./bin/codebrowser
  # puis ouvrir http://127.0.0.1:8091 (redirige vers /classes)
  ```

## Décisions tranchées
- Granularité des résultats : **un JSON par page** (pas de fusion
  automatique au niveau document pour l'instant).
- Stratégie d'échec d'étage : **échec immédiat**, pas de retry — le
  document/la page est marqué en échec et passe en revue humaine.
- Stockage (phase 1) : **fichiers JSON sur disque**, un par page, + logs
  — implémenté au jalon 6 (`internal/store`, flag `--out-dir`).

- **Moteur de serving des modèles : llama.cpp server** (binaire natif,
  accélération Metal, API compatible OpenAI, JSON Schema via grammars).
  Remplace vLLM partout dans ce document et dans le code — vLLM ne tourne
  pas sur Apple Silicon (pas de CUDA). Les ports Go (interfaces VLM/LLM)
  restent inchangés : seule l'implémentation HTTP cible change de backend.
- **Stockage phase 2 : MongoDB Atlas — portée élargie au jalon 12.**
  Décision initiale (jalon 3) : seuls les résultats extraits validés
  pourraient être synchronisés vers Atlas, jamais les PDF sources.
  **Révisée explicitement par l'utilisateur au jalon 12** : les documents
  uploadés via `cmd/jarvisweb` (PDF sources compris) sont désormais aussi
  stockés dans Atlas — voir "Contraintes non négociables" en tête de
  document pour la formulation à jour. Toute inférence (VLM, LLM) continue
  de tourner en local via llama.cpp, sans exception. La CLI et
  `internal/store` (JSON sur disque, `jarvis process --out-dir`) restent
  100% locaux, non concernés par cette bascule.

## Décisions en attente
- Stratégie bbox pour les pages **scannées** (VLM) — voir jalon 7
  ci-dessus. Résolu pour le texte natif ; pas pour les pages VLM.
- Schéma de sortie exact des futurs types de documents au-delà de
  Facture (pièce d'identité, correspondance, document technique...) —
  le mécanisme (registre + dérivation) est en place, chaque nouveau type
  s'ajoute au besoin.
Les findings 3 et 4 du jalon 10 (fusion multi-pages, latence Qwen3) sont
résolus — voir jalon 11.
- Validation réelle de `MongoStore` contre Atlas (jalon 12) — accès à
  donner par l'utilisateur. Tant que ce n'est pas fait : le code compile
  et est testé via `FakeStore`, mais n'a jamais parlé à une vraie base.
- Infrastructure MongoDB multi-provider + copie locale périodique
  (mentionnée par l'utilisateur au jalon 12) — pas détaillée, hors
  scope du code applicatif pour l'instant.
