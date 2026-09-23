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
  2. **Interface web (`cmd/jarvisapp`, ex-`cmd/jarvisweb` avant la fusion
     du jalon 13, exception ouverte au jalon 12) : les documents uploadés
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

### Corpus de fixtures (testdata/fixtures/, régénéré par scripts/gen_fixtures.py)
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

### Application web unifiée (cmd/jarvisapp)
**Un seul binaire, un seul port** — remplace depuis le jalon 13 les deux
anciens binaires `cmd/jarvisweb` (upload/suivi) et `cmd/codebrowser`
(navigateur de code/tests), fusionnés sous une nav commune (Importer ·
Documents · Tickets · Classes · Modèle · Tests · Architecture — renommée au jalon 22,
Modèle ajouté au jalon 24, Tickets au jalon 26, Architecture au jalon 29). Voir "État des jalons" plus bas pour le détail de la
fusion, et "Atelier de code" pour le détail du navigateur de code/tests
lui-même (moteurs inchangés par la fusion).

**Usage quotidien recommandé : le lanceur** (`cmd/jarvis-launcher`,
jalon 14) démarre VLM + LLM + `jarvisapp` en un geste et ouvre le
navigateur — voir "État des jalons" pour le détail. `make package-app`
produit `dist/Jarvis.app`, à glisser dans le Dock. Ce qui suit est la
séquence manuelle, utile pour déboguer étape par étape.
```
# Une fois (regénère les templates après toute modif de .templ) :
go install github.com/a-h/templ/cmd/templ@latest   # ajoute $(go env GOPATH)/bin au PATH
make build-app

# Les deux serveurs llama.cpp du setup ci-dessus doivent tourner
# (VLM :8080, LLM :8081), puis (MONGO_URI = connexion Atlas, cf. jalon 12) :
./bin/jarvisapp \
  --vlm-url http://127.0.0.1:8080/v1 --vlm-model olmOCR-2-7B-1025 --vlm-model-version Q6_K \
  --llm-url http://127.0.0.1:8081/v1 --llm-model qwen3-8b --llm-model-version Q5_K_M \
  --mongo-uri "$MONGO_URI" \
  --out-dir ./data/results \
  --addr 127.0.0.1:8090

# Puis ouvrir http://127.0.0.1:8090 — Upload (upload d'un PDF, résultat
# affiché dès qu'il est prêt, poll HTMX automatique), Classes/Tests
# (navigateur de code, cf. "Atelier de code" plus bas).
```
Flags utiles : `--mongo-uri` (**requis**, jalon 12 — les jobs, PDF source
compris, sont persistés dans MongoDB), `--mongo-db`/`--mongo-collection`
(défauts `jarvis`/`jobs`), `--work-dir` (fichiers temporaires de
traitement, vide = répertoire temporaire système), `--out-dir` (copie
locale additionnelle facultative des résultats, comme pour `jarvis
process` — MongoDB reste la source de vérité), `--dpi`/`--vlm-timeout`/
`--llm-timeout` (mêmes défauts que la CLI), `--module-dir` (racine du
module pour le navigateur de code, vide = répertoire courant),
`--watch-dir`/`--watch-interval` (ingestion automatique de PDF déposés
dans un dossier, jalon 16 ; vide = désactivée), `--concurrency` (pages
traitées en parallèle par document, VLM et extraction — défaut 4, aligné
sur les slots par défaut de `llama-server` ; 1 = séquentiel, jalon 21).
`make run-app` lance tout avec les valeurs par défaut du setup ci-dessus
(`MONGO_URI` doit être exporté dans l'environnement).

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
- **Jalon 13 — fusion en une seule application web (`cmd/jarvisapp`) :
  fait.** Demandé explicitement par l'utilisateur, préalable à l'épopée
  "ingestion automatique + bibliothèque de documents" (jalons 14+) :
  "regrouper toutes les pages qu'on a créées d'une seule application
  WEB" avant d'ajouter la suite.
  - `cmd/jarvisweb` (upload/suivi, jalons 8-12) et `cmd/codebrowser`
    (navigateur de code/tests, travail parallèle du jalon 12) retirés,
    remplacés par `cmd/jarvisapp` : un seul binaire, un seul port
    (`:8090`), une seule nav (Upload · Classes · Tests). Toute la
    logique métier sous-jacente est réutilisée telle quelle — aucun
    changement dans `internal/webapp`, `internal/codemap`,
    `internal/testmap`, `internal/testrunner` : le jalon 13 est de la
    plomberie de présentation, pas une réécriture.
  - `cmd/jarvisapp/templates` fusionne les deux jeux de templates
    (`layout.templ` commun, `upload.templ`+`job.templ` ex-jarvisweb,
    `classes.templ`+`tests.templ` ex-codebrowser) sous un seul
    `package templates` — deux collisions de noms révélées par le
    compilateur en les rassemblant : `FieldView` (renommé
    `FieldInfoView` côté classes, pour ne pas écraser le `FieldView`
    d'un champ extrait côté job) et `statusLabel` (renommé
    `testStatusLabel` côté tests, pour ne pas écraser le `statusLabel`
    d'un statut de job).
  - `Layout(title, active string)` : nouveau paramètre `active` pour
    surligner l'onglet courant dans la nav (`header nav a.active`),
    absent des deux layouts d'origine.
  - CSS des deux anciens layouts fusionné dans un seul `<style>`, sans
    doublon (`table`/`td,th`/`.muted`/`button` etc. étaient définis à
    l'identique des deux côtés) ; le correctif lisibilité du jalon
    "atelier de code" (`color-scheme: light` + couleur de texte
    explicite, cf. plus bas) est repris pour l'ensemble de l'appli — la
    page Upload utilisait encore l'ancien `color-scheme: light dark`
    bugué, non corrigée jusqu'ici faute d'avoir été signalée.
  - Testé : les tests HTTP de l'ex-`cmd/jarvisweb` portés tels quels
    (upload, statut de job, poll). Nouveaux tests pour le câblage propre
    à la fusion — pas une re-vérification des moteurs déjà testés en
    isolation (`internal/codemap`, `internal/testmap`,
    `internal/testrunner` ont leurs propres suites) : rendu de
    `/classes` et `/classes/detail` à partir d'un `Model` injecté
    directement (relation `Embeds` visible), rendu de `/tests` à partir
    de `Category` injectées, et une vraie invocation de `go test -json`
    sur un module synthétique via `POST /tests/run?pkg=...` pour valider
    le cache de résultats (`resultKey`/`recordResults`).
  - `go build ./...` + suite complète (`gofmt`, `go vet`, `go test
    ./... -race -tags=integration`) verts.
- **Jalon 14 — lanceur (`cmd/jarvis-launcher`) : fait, validé
  end-to-end.** Démarre en un geste VLM + LLM + `jarvisapp`, pour un
  raccourci dans le Dock ("comme ça je peux créer un raccourci dans ma
  barre d'app").
  - Pensé pour tourner **sans terminal** (double-clic depuis le Dock) :
    aucune dépendance à une variable d'environnement shell ou à un flag
    pour la configuration — tout vit dans `~/.jarvis/launcher.json`
    (créé avec les valeurs par défaut de `internal/launcher.
    DefaultConfig`, dérivées des chemins déjà documentés dans "Setup
    local complet", au premier lancement). Toute la sortie va dans
    `~/.jarvis/logs/{launcher,vlm,llm,jarvisapp}.log`, seul moyen de
    déboguer un lancement sans terminal.
  - **`MONGO_URI` n'est jamais deviné.** Résolu dans l'ordre : variable
    d'environnement (pratique en lancement depuis un terminal) puis, sur
    macOS, une boîte de dialogue native (`osascript display dialog`) —
    demandée une seule fois, la valeur saisie est aussitôt écrite dans
    le fichier de config (permissions restreintes à `0600`, le fichier
    contenant l'URI en clair). Hors macOS sans variable
    d'environnement : erreur explicite, pas de blocage silencieux.
  - `internal/launcher` (le cœur testable — TDD, comme partout ailleurs
    dans ce projet) :
    - `Config`/`DefaultConfig`/`LoadConfig`/`SaveConfig`/`EnsureConfig` :
      aucune valeur cachée — `DefaultConfig` est une fonction pure
      (chemins dérivés de `homeDir`/`repoDir` donnés, jamais lus depuis
      l'environnement), le fichier une fois écrit est la seule source de
      vérité relue ensuite.
    - `PortOpen`/`WaitHealthy` : évite de démarrer un second serveur si
      quelque chose écoute déjà sur le port (permet de mélanger lancement
      manuel et lanceur sans conflit), puis attend un vrai `200` sur
      `/health` (endpoint confirmé empiriquement sur un `llama-server`
      réel avant d'écrire le code, `{"status":"ok"}`) plutôt qu'un
      `sleep` fixe.
    - `ArgsForVLM`/`ArgsForLLM`/`ArgsForJarvisApp`/`ResolvePath` :
      construction pure des arguments — testée explicitement, pour
      qu'un flag manquant ou mal placé casse un test plutôt que d'être
      découvert en re-tapant les commandes à la main.
    - `StartProcess` : démarre un sous-processus avec stdout/stderr
      redirigés vers son fichier de log, sans bloquer.
    - `ParseOSAScriptTextReturned` : extraction pure de la réponse
      `osascript` (format `"button returned:OK, text returned:..."`),
      séparée de l'appel réel pour être testable sans déclencher une
      vraie boîte de dialogue.
  - `cmd/jarvis-launcher/main.go` : orchestration (non testée
    unitairement, comme les autres `main.go` de ce projet — la logique
    testable est déjà dans `internal/launcher`) — vérifie l'existence
    des poids modèles et des binaires (`llama-server` sur le PATH,
    `bin/jarvisapp` déjà construit) avant de démarrer quoi que ce soit,
    démarre VLM/LLM/jarvisapp (en réutilisant ce qui tourne déjà),
    attend que chacun soit prêt, ouvre le navigateur (`open`, macOS),
    puis bloque jusqu'à un signal d'arrêt ou jusqu'à ce qu'un des
    processus qu'il a lui-même démarrés s'arrête de façon inattendue —
    dans les deux cas, termine (`SIGTERM`) uniquement les processus
    qu'il a démarrés, jamais ceux détectés déjà en cours.
  - **Empaquetage `.app` macOS** (`packaging/macos/Info.plist` +
    `make package-app`) : structure minimale standard (pas d'outil
    tiers), non signée — premier lancement via clic droit > Ouvrir dans
    le Finder (Gatekeeper), ensuite glissable dans le Dock.
  - **Limite connue, assumée** : `jarvis-launcher` est un simple
    exécutable Unix enveloppé dans un `.app`, pas une vraie application
    Cocoa (pas de menu ni de gestion d'événements `NSApplication`). Le
    comportement du "Quit" depuis le menu contextuel du Dock peut donc
    être incohérent selon macOS — un arrêt forcé depuis le Moniteur
    d'activité reste le filet de secours si `SIGTERM` n'est pas délivré
    proprement. Non résolu ici (demanderait une vraie intégration Cocoa,
    hors scope "primitives, pas de framework lourd").
  - **Validé en conditions réelles, premier lancement inclus** :
    `~/.jarvis/launcher.json` inexistant supprimé avant le test,
    `MONGO_URI` exporté (pour éviter de déclencher la boîte de dialogue
    pendant un test automatisé) ; le lanceur a créé la config, détecté
    les deux `llama-server` VLM/LLM déjà en cours (réutilisés sans
    doublon), démarré `jarvisapp`, attendu qu'il soit prêt, et
    `curl http://127.0.0.1:8090/` a répondu `200` — le tout en moins de
    2s puisque VLM/LLM tournaient déjà. Fichier de config vérifié en
    `0600` après correctif (la première version l'écrivait en `0644`,
    trouvé avant commit).
  - Testé : suite `internal/launcher` complète (`-race`), + la
    validation réelle ci-dessus pour `cmd/jarvis-launcher` lui-même.
- **Jalon 15 — classification automatique à l'upload + correctif
  MongoDB : fait, validé end-to-end.** Demandé explicitement :
  "uploader un document et laisser le modèle trouver le type. Quand il
  a trouvé le type alors il essaye d'extraire les données."
  - **`internal/classify`** (nouveau port) : `Classifier.Classify(ctx,
    text, candidates) (Result, error)`, `Result{DocType, Confidence}`
    (`DocType` vide si rien ne correspond — jamais un type deviné).
    `LLMClassifier` réutilise le LLM d'extraction déjà en place (aucun
    nouveau modèle choisi) avec un **JSON Schema dont l'énumération de
    `doc_type` est construite dynamiquement** à partir des candidats du
    registre (`doctype.Registry.Registrations()`, nouvelle méthode) —
    décodage contraint à un nom de type connu ou `"unknown"`, jamais du
    texte libre ; une vérification défensive supplémentaire ignore quand
    même toute valeur hors de cette liste (jamais confiance aveugle
    dans une sortie de modèle). Texte envoyé borné à 4000 caractères
    (`DefaultMaxTextLength`) — la classification n'a besoin que d'un
    signal, pas du document entier.
  - **`pipeline.Pipeline.RunAuto(ctx, path)`** (nouveau, à côté de `Run`
    qui reste inchangé pour la CLI où le type est déjà connu) :
    factorise Triage+Parsing dans un helper `prepare()` partagé par les
    deux, classifie le texte fusionné de toutes les pages, puis
    extrait seulement si un type a été trouvé. `Result` gagne `DocType`
    et `ClassificationConfidence`. Aucun type reconnu = `Result` sans
    erreur, juste `Extraction` vide — cohérent avec "jamais de valeur
    inventée", pas un échec.
  - **`internal/webapp`** simplifié en conséquence : `Runner.Run(ctx,
    reg, path)` devient `Runner.RunAuto(ctx, path)` (plus de type
    imposé par l'appelant) ; `JobManager.Submit` perd son paramètre
    `docType` ; `Job.DocType` est vide jusqu'à la fin du traitement,
    renseigné depuis `result.DocType` dans `finish()`. Le registre n'est
    plus une dépendance de `JobManager` du tout (il vit désormais
    uniquement dans `pipeline.Pipeline`, côté classification).
  - **`cmd/jarvisapp`** : page Upload sans sélecteur de type — juste le
    fichier, un texte informatif ("Types reconnus aujourd'hui : ...").
    `job.templ` distingue maintenant trois issues pour un job terminé :
    type classifié (résultat affiché comme avant, plus la confiance de
    classification), aucun type reconnu (message explicite, pas de
    tableau vide déroutant), échec. **Chaque état affiche désormais
    "✓ Enregistré dans MongoDB (id: ...)"** dès la soumission — en
    réponse directe à l'utilisateur qui avait l'impression que les
    documents n'atteignaient pas la base.
  - **Bug réel trouvé et corrigé en testant un vrai upload bout en
    bout** (pas en relecture) : `MongoStore.Update` ne réécrivait jamais
    `doc_type` dans son `$set` — un reliquat du jalon 12, où le type
    était fixé une fois pour toutes à la soumission (choisi par
    l'utilisateur) et n'avait donc jamais besoin d'être mis à jour.
    Avec la classification automatique, `DocType` n'est connu qu'à la
    fin du traitement : `Update` l'oubliait, donc `GET /jobs/{id}`
    relisait toujours `doc_type: ""` depuis Mongo malgré une
    classification réussie (visible dans `result_json`, qui lui était
    bien à jour) — un upload de `facture_multiligne.pdf` (une vraie
    facture) affichait "Aucun type de document reconnu" à chaque
    rechargement de la page. Confirmé reproductible sur le serveur réel
    (3 tentatives, toujours faux), puis isolé : un appel direct à
    `LLMClassifier`/`Pipeline.RunAuto` donnait le bon résultat —
    la classification elle-même n'était jamais en cause, seule la
    persistance de son résultat l'était. Corrigé (`doc_type` ajouté au
    `$set`), reproduit en échec puis en succès avec un test dédié
    (`internal/webapp/mongo_store_test.go`, `-tags=integration`, ignoré
    si `MONGO_URI` n'est pas exporté) avant de considérer le correctif
    validé — **premiers tests automatisés de `MongoStore` contre une
    vraie base Atlas** (jusqu'ici seulement testé manuellement, cf.
    jalon 12).
  - **Validé en conditions réelles** : upload de `facture_multiligne.pdf`
    (vraie facture) → classifiée `facture` (confiance 1.00), champs
    extraits corrects, `doc_type` persisté et relu correctement depuis
    Mongo après le correctif. Upload d'un document non-facture généré
    pour l'occasion (rapport technique) → `Aucun type de document
    reconnu`, aucune extraction tentée, document tout de même enregistré.
    Les jobs de test créés pendant cette investigation ont été supprimés
    de la collection `jobs` réelle après coup (identifiants notés,
    supprimés un par un) pour ne pas polluer les données de
    l'utilisateur.
  - Testé : `internal/classify` (schéma à énumération dynamique, prompt,
    troncature, hallucination traitée en défense, erreurs LLM/JSON),
    `internal/pipeline` (`RunAuto` : classification puis extraction,
    type inconnu, Classifier/Registry manquants, erreurs), `internal/
    webapp` (`Submit` sans docType, `DocType` renseigné après coup),
    `cmd/jarvisapp` (upload sans sélecteur, rendu des trois issues,
    confirmation de persistance), `internal/webapp/mongo_store_test.go`
    (réel, cf. ci-dessus). Suite complète verte (`gofmt`, `go vet`, `go
    test ./... -race -tags=integration`, `MONGO_URI` exporté).
- **Jalon 16 — ingestion automatique par dossier surveillé : fait,
  validé end-to-end.** "Avoir un job qui tourne à chaque fois qu'un
  document est uploadé dans un folder." Réutilise entièrement le jalon
  15 (classification + extraction conditionnelle) — l'ingestion par
  dossier n'ajoute que la détection du nouveau fichier, pas un second
  chemin de traitement.
  - **`internal/watch.Watcher`** (nouveau) : sonde un dossier à
    intervalle régulier (`Interval`, défaut 5s) — **pas de dépendance de
    notification système** (`fsnotify` ou équivalent) : un simple
    `os.ReadDir` périodique suffit et reste remplaçable en un jour,
    cohérent avec "primitives, pas de dépendances lourdes".
  - **Pas d'état persistant nécessaire pour savoir quels fichiers sont
    "nouveaux"** : chaque fichier `*.pdf` trouvé est immédiatement
    déplacé (`os.Rename`, atomique) dans un sous-dossier `.processing/`
    avant même d'être lu — donc un fichier encore présent au sondage
    suivant est forcément nouveau, sans avoir à mémoriser une liste de
    fichiers déjà vus (qui n'aurait pas survécu à un redémarrage). Une
    fois traité, direction `processed/` (succès) ou `failed/` (échec —
    jamais retraité en boucle, jamais perdu). Une collision de nom dans
    `processed/`/`failed/` (même fichier redéposé plus tard) est
    suffixée par un horodatage plutôt que d'écraser la trace
    précédente.
  - `Watcher.OnFile(ctx, filename, content) error` est le seul point de
    contact avec le reste du système : câblé dans `cmd/jarvisapp` sur
    exactement le même `JobManager.Submit` que l'upload web — un
    document ingéré par le dossier suit rigoureusement le même chemin
    (Mongo → classification → extraction conditionnelle) et apparaît
    de façon identique dans le suivi des jobs, aucune logique dupliquée.
  - `cmd/jarvisapp` : `--watch-dir` (vide = désactivé, pas de dossier
    surveillé par défaut) et `--watch-interval` (défaut 5s). Lancé dans
    une goroutine dédiée au démarrage, en parallèle du serveur HTTP.
  - **Validé en conditions réelles** : un PDF déjà présent dans le
    dossier au démarrage est traité immédiatement (pas d'attente du
    premier intervalle) ; un second PDF déposé pendant que le serveur
    tourne est détecté au sondage suivant (3s dans ce test) — les deux
    finissent `status: done`, `doc_type: facture` correctement
    persisté (revalidant au passage le correctif Mongo du jalon 15),
    et les deux fichiers se retrouvent dans `processed/`. Jobs et
    fichiers de test supprimés après coup.
  - Testé : `internal/watch` (fichiers PDF traités et déplacés,
    fichiers non-PDF et sous-dossiers ignorés, échec de `OnFile` envoyé
    vers `failed/`, collision de nom non écrasante, arrêt propre sur
    annulation de contexte, traitement immédiat au démarrage) + la
    validation réelle ci-dessus pour le câblage dans `cmd/jarvisapp`.
- **Jalon 17 — bibliothèque de documents : fait, validé end-to-end.**
  "Une page dans laquelle je puisse naviguer dans tous mes documents par
  date [...] prévisualiser [...] ajouter des tags, changer le type [...]
  une barre de recherche [...] voir les éléments extraits." Un
  "document" de cette bibliothèque **est** un `webapp.Job` — même
  donnée que le suivi d'upload, juste une seconde porte d'entrée
  (naviguer par date/recherche plutôt que suivre l'upload qui vient de
  se terminer). Aucun nouveau modèle de données dupliqué.
  - **`internal/webapp.Store`** gagne `List(ctx, ListQuery)
    ([]Job, error)` (`ListQuery{Search, Limit}, Limit=0 ->
    DefaultListLimit=200`) — implémenté dans `FakeStore` (tri/filtre en
    mémoire) et `MongoStore` (`$regex` insensible à la casse sur
    `filename`/`doc_type`/`tags`, tri `created_at` décroissant ; pas
    d'index plein texte dédié, la collection est petite et `$regex`
    reste remplaçable en un jour si le volume grandit). `Job.Tags
    []string` (nouveau champ, purement organisationnel, aucune
    incidence sur le traitement) ; `Update` (Mongo) l'inclut désormais
    dans son `$set`.
  - **`pipeline.Pipeline.RunWithType(ctx, docType, path)`** (nouveau,
    à côté de `Run`/`RunAuto`) : résout `docType` via `Registry` puis
    délègue à `Run` — répond à "changer le type sur la base des types
    existants" en relançant une vraie extraction avec le type choisi,
    pas juste en réétiquetant le document. `webapp.Runner` gagne cette
    méthode dans son interface (satisfaite par `pipeline.Pipeline` par
    typage structurel, comme `RunAuto`).
  - **`JobManager`** gagne trois méthodes : `List` (délègue à
    `Store.List`), `SetTags(ctx, id, tags)` (charge, remplace `Tags`,
    persiste), `Reprocess(ctx, id, docType)` (recharge le job, passe en
    `StatusRunning`, **rematérialise `Content` déjà en base** — jamais
    redemandé à l'utilisateur — puis appelle `runner.RunWithType` de
    façon asynchrone, exactement comme `Submit`/`run` : mêmes
    transitions de statut, même `finish()`, aucune logique dupliquée).
  - **`cmd/jarvisapp`** : nouvelles routes `GET /documents` (liste +
    recherche via `?q=`), `GET /documents/{id}` (détail),
    `GET /documents/{id}/pdf` (octets bruts, `Content-Type:
    application/pdf`, prévisualisable directement dans un `<embed>`),
    `POST /documents/{id}/tags`, `POST /documents/{id}/reprocess`. Nav
    partagée : nouvel onglet "Documents".
  - **Réutilisation délibérée plutôt que duplication** : la page de
    détail affiche le statut/l'extraction via `templates.JobFragment`
    tel quel — celui déjà utilisé par le flux Upload. Un changement de
    type déclenche une ré-extraction asynchrone dont le suivi (pending
    → running → done, avec polling HTMX) passe par la route **existante**
    `GET /jobs/{id}` : aucun mécanisme de polling supplémentaire écrit
    pour la bibliothèque.
  - **Correctif d'affichage trouvé en testant réellement une
    ré-extraction** : `JobFragment` affichait "classification 0.00" à
    côté d'un type réattribué manuellement — `RunWithType` ne classifie
    jamais, `ClassificationConfidence` reste donc à sa valeur zéro, ce
    qui se lisait comme "le système n'est pas sûr que ce soit une
    facture" alors que l'utilisateur venait justement de le choisir
    explicitement. Corrigé : le libellé affiche "type assigné
    manuellement" quand `ClassificationConfidence == 0`, "classification
    X.XX" sinon — heuristique pragmatique (une vraie classification
    automatique à confiance exactement 0.00 est possible en théorie
    mais rejetée par construction : `RunAuto` ne retient un `DocType`
    que si le classifieur a positivement désigné un candidat).
  - **Validé en conditions réelles, bout en bout** : upload d'une
    facture réelle (`facture_ambigue.pdf`) → apparaît dans
    `GET /documents`, trouvable par recherche sur son nom de fichier ;
    aperçu PDF (`GET /documents/{id}/pdf`) confirmé octets bruts
    corrects (`%PDF-1.4...`) ; tags ajoutés (`urgent, a-verifier`) puis
    retrouvés par recherche sur le tag ; changement de type vers
    "facture" (le seul déjà enregistré, donc revalidant surtout le
    mécanisme plutôt qu'un vrai changement) déclenche une vraie
    ré-extraction LLM qui retrouve les mêmes valeurs correctes connues
    de ce document depuis le jalon 10 (`fournisseur: "Fournitures Bureau
    Plus"`, `numero: "2026-0055"`, `total_ttc: 570`). Document de test
    supprimé de la collection `jobs` réelle après coup.
  - Testé : `internal/webapp` (`FakeStore.List` — tri, filtre
    filename/doc_type/tags, sensibilité à la casse, limite ;
    `JobManager.Reprocess`/`SetTags`/`List`), `internal/webapp/
    mongo_store_test.go` (réel : `List` filtré+trié, `Update` persiste
    `Tags`), `internal/pipeline` (`RunWithType` : succès, type inconnu,
    Registry manquant), `cmd/jarvisapp` (les cinq routes, y compris
    filtrage par recherche et fragment de polling retourné par
    `reprocess`) + la validation réelle ci-dessus. Suite complète verte
    (`gofmt`, `go vet`, `go build`, `go test ./... -race
    -tags=integration`, `MONGO_URI` exporté).
- **Jalon 18 — suppression de documents + recherche élargie au contenu :
  fait, validé end-to-end.** Deux demandes : "pouvoir supprimer un
  document" et "une plus grande barre de recherche qui peut aussi
  chercher dans les documents".
  - **Suppression** : `Store.Delete(ctx, id) error` (nouveau, implémenté
    dans `FakeStore` et `MongoStore` — erreur explicite si `id`
    n'existe pas, jamais un succès silencieux sur rien à supprimer) ;
    `JobManager.Delete` délègue directement. **Ne touche pas à la copie
    locale additionnelle (`--out-dir`)** — celle-ci reste un filet de
    secours indépendant, jamais purgé automatiquement (décision
    assumée, pas creusée davantage faute de demande explicite).
    `DELETE /documents/{id}` (bouton `hx-delete` + `hx-confirm` côté
    liste ET détail — confirmation navigateur avant une action
    irréversible) répond avec l'en-tête `HX-Redirect: /documents` plutôt
    qu'un fragment : que l'appel vienne de la liste ou du détail, HTMX
    fait naviguer vers `/documents`, qui reflète alors l'état à jour —
    plus simple que retirer une ligne en place et gérer différemment
    selon l'origine de l'appel.
  - **Recherche élargie au contenu** : `pipeline.Result` gagne
    `SearchText string` — le texte du document (natif ou Markdown VLM,
    toutes pages confondues, via `concatPageTexts` déjà utilisé pour la
    classification, jalon 15) — renseigné par `Run` **et** `RunAuto`,
    y compris quand aucun type n'est reconnu ou que l'extraction échoue
    (dès que Triage+Parsing ont abouti : le texte d'un document existe
    indépendamment de sa classification). Recopié dans `Job.SearchText`
    à la fin du traitement (`finish()`, même mécanisme que `DocType`
    depuis le jalon 15) et persisté (`MongoStore` : champ `search_text`,
    inclus dans le `$set` de `Update`) ; `ListQuery.Search` /
    `Store.List` (`FakeStore` et `MongoStore`, `$or` regex) l'incluent
    désormais en plus de filename/doc_type/tags — toujours pas d'index
    plein texte dédié (même décision assumée qu'au jalon 17).
  - **Grande barre de recherche** : nouvelle classe CSS `.search-bar`
    (pleine largeur, police et padding agrandis) remplace le petit champ
    du jalon 17 ; placeholder mis à jour pour indiquer que le contenu du
    document est aussi cherché.
  - **Validé en conditions réelles** : upload d'une facture réelle
    (`facture_multiligne.pdf`) → trouvée par recherche sur "Republique"
    (l'adresse du fournisseur, dans le corps du PDF, absente du nom de
    fichier/type/tags) et sur "Cartouche" (une ligne d'article) ;
    absente d'une recherche sur un mot ne figurant pas dans le
    document. `DELETE /documents/{id}` confirmé : en-tête
    `HX-Redirect: /documents` reçu, document introuvable ensuite (404
    sur le détail, absent de la recherche).
  - Testé : `internal/webapp` (`FakeStore.Delete`/`.List` sur
    `SearchText`, `JobManager.Delete`), `internal/webapp/
    mongo_store_test.go` (réel : `Delete`, `List` filtré sur
    `search_text`), `internal/pipeline` (`Run`/`RunAuto` renseignent
    `SearchText`, y compris document non classifié), `cmd/jarvisapp`
    (route `DELETE`, en-tête `HX-Redirect`, 404 sur id inconnu) + la
    validation réelle ci-dessus. Suite complète verte.
- **Jalon 19 — quatre nouveaux types de document : fait, validé
  end-to-end.** Proposition d'une liste de candidats (facture n'étant
  qu'un premier exemple, cf. brief initial "pièce d'identité,
  correspondance, document technique, etc."), l'utilisateur en a choisi
  quatre — délibérément un sous-ensemble qui inclut le cas le plus
  difficile pour la classification automatique (jalon 15) : deux types
  dont les champs ressemblent beaucoup à Facture.
  - `internal/doctype` : `Devis`, `BonCommande`, `PieceIdentite`,
    `Correspondance` (un fichier chacun, même forme que `Facture`) —
    aucun changement à `internal/schema` ni `internal/extraction`,
    exactement comme promis par la conception du registre depuis le
    jalon 4.
    - `Devis{Numero, Fournisseur, MontantTotal, DateValidite}`
    - `BonCommande{Numero, Fournisseur, Client, MontantTotal}`
    - `PieceIdentite{Nom, Prenom, DateNaissance, NumeroDocument,
      DateExpiration}`
    - `Correspondance{Expediteur, Destinataire, Date, Objet}`
  - **Descriptions volontairement écrites pour se distinguer
    explicitement** (le texte vu par le LLM de classification) :
    Facture = "paiement pour des biens/services **déjà livrés**",
    Devis = "proposition de prix **avant achat, pas encore payée**",
    BonCommande = "émis **par le client**, avant facturation". Sans
    cette distinction explicite dans les descriptions, la classification
    aurait pu confondre les trois (champs quasi identiques : numéro,
    fournisseur, montant).
  - **Un test existant s'est révélé brittle** en ajoutant ces types :
    `TestPipeline_RunAuto_ClassifiesThenExtracts` présumait que le
    registre par défaut ne contenait qu'un seul type (`facture`) —
    corrigé pour vérifier seulement que le candidat facture figure bien
    parmi les candidats envoyés au classifieur, sans présumer du nombre
    total. Un rappel utile : les tests qui dépendent de
    `doctype.NewDefaultRegistry()` ne doivent jamais présumer de sa
    taille exacte, elle grandira encore.
  - **Validé en conditions réelles, les quatre types, y compris le cas
    difficile** : quatre documents synthétiques générés pour
    l'occasion (mêmes primitives que `scripts/gen_fixtures.py`, non
    ajoutés au corpus permanent) — un devis, un bon de commande, une
    pièce d'identité, une correspondance. **Classification correcte à
    chaque fois** (`devis` confiance 1.00, `bon_commande` confiance
    1.00 — le devis et le bon de commande n'ont pas été confondus
    l'un avec l'autre ni avec facture — `piece_identite` confiance
    1.00, `correspondance` confiance 0.95), **extraction exacte pour
    chaque champ** des quatre types, aucune hallucination. Jobs de
    test supprimés de la collection `jobs` réelle après coup.
  - Testé : un fichier de test par type dans `internal/doctype` (miroir
    de `facture_test.go` : registration présente + description
    non-vide + schéma dérive sans erreur avec les propriétés
    attendues) + la validation réelle ci-dessus (le vrai test de ce
    jalon — la classification en conditions réelles, pas seulement la
    dérivation de schéma).
- **Jalon 20 — jobs orphelins après redémarrage + timestamp de début :
  fait, validé end-to-end.** Déclenché par un vrai incident signalé par
  l'utilisateur ("j'ai uploadé un nouveau document, et c'est vraiment
  très long !").
  - **Root cause trouvée en investiguant l'incident réel** (pas en
    relecture) : le document en question (`Devis 160-2026.pdf`, un vrai
    devis de l'utilisateur — bon test du jalon 19 en usage réel) était
    bloqué `status: running` avec un `finished_at` **déjà dans le
    passé** — signe d'une ré-extraction (`Reprocess`, jalon 17)
    interrompue par un redémarrage du serveur (probable pendant mes
    propres cycles de rebuild du jalon 19) : la goroutine qui aurait dû
    la terminer appartenait à l'ancien process, disparue avec lui.
    `JobManager` ne persiste aucun état de reprise, donc un tel job
    reste bloqué "running" **pour toujours**, avec un fragment qui
    continue de sonder dans le vide — ce que l'utilisateur percevait
    comme "très long" était en fait un job qui n'allait jamais aboutir.
    Corrigé pour ce document précis en relançant manuellement une
    ré-extraction (résultat correct obtenu : `devis`, tous les champs
    exacts) avant de corriger la cause.
  - **`ListQuery.Status Status`** (nouveau filtre, `FakeStore` +
    `MongoStore`) permet de retrouver les jobs dans un état exact.
  - **`JobManager.RecoverOrphaned(ctx) (int, error)`** — appelé une
    fois au démarrage de `cmd/jarvisapp` (avant d'accepter des
    requêtes) : tout job encore `StatusPending`/`StatusRunning` à ce
    moment-là appartenait par construction à un process précédent (ce
    process vient de démarrer) et ne peut jamais aboutir — marqué
    `StatusFailed` avec un message explicite ("interrompu par un
    redémarrage du serveur — relance-le"), plutôt que laissé bloqué
    silencieusement.
  - **`Job.StartedAt`** (nouveau champ, distinct de `CreatedAt`) :
    l'instant où le traitement **en cours** a commencé, réinitialisé à
    chaque tentative (`Submit` et `Reprocess` le renseignent
    séparément) — répond directement à "il faudrait ajouter un
    timestamp de début". Affiché dans `job.templ` : "démarré à
    HH:MM:SS" pendant pending/running, "démarré à HH:MM:SS, terminé en
    X.Xs" une fois terminé (succès, échec, ou type non reconnu) — donne
    enfin un repère visible pour remarquer un job resté "running" trop
    longtemps, exactement ce qui manquait pour ce diagnostic-ci.
  - **Validé en conditions réelles** : un job "orphelin" simulé
    (inséré directement en base avec `status: running`) a été détecté
    et marqué en échec dès le démarrage suivant de `jarvisapp`, avec le
    message explicatif visible sur sa page ; un vrai upload a bien
    affiché "démarré à HH:MM:SS" pendant le traitement puis "démarré à
    HH:MM:SS, terminé en 26s" une fois fini. Données de test supprimées
    de la collection `jobs` réelle après coup.
  - Testé : `internal/webapp` (`FakeStore.List` filtré par statut exact,
    `JobManager.RecoverOrphaned` — jobs pending/running marqués échec, done
    laissé intact, aucun orphelin = 0 ; `Submit`/`Reprocess` renseignent
    `StartedAt`, un `Reprocess` lui donne une valeur fraîche et
    postérieure à la première tentative), `internal/webapp/
    mongo_store_test.go` (réel : `List` filtré par statut) + la
    validation réelle ci-dessus.
  - **Analyse de la rapidité demandée séparément** (piste principale :
    paralléliser le traitement par page, VLM et LLM, puisque
    `llama-server` expose déjà 4 slots parallèles alors que
    `internal/parsing.Parser.ParsePages` et `internal/extraction.
    Extractor.ExtractPages` traitent les pages séquentiellement) —
    proposée à l'utilisateur, retenue : voir jalon 21.
- **Jalon 21 — traitement des pages en parallèle (VLM + extraction) :
  fait, validé end-to-end.** Suite directe de l'analyse de rapidité du
  jalon 20, retenue par l'utilisateur ("oui, les deux").
  - **`parsing.Parser.Concurrency`** et **`extraction.Extractor.
    Concurrency`** (nouveaux champs, même principe dans les deux
    paquets) : 0 ou 1 (par défaut) = séquentiel, comportement strictement
    inchangé par rapport aux jalons précédents (branché vers l'ancienne
    boucle simple, pas juste "goroutine unique" — zéro risque
    additionnel pour qui n'active pas l'option). `> 1` borne un pool de
    goroutines (sémaphore par canal) à ce nombre ; les résultats sont
    écrits par index (jamais un `append` concurrent) pour préserver
    l'ordre des pages malgré l'exécution parallèle. `Pipeline.
    Concurrency` (nouveau) transmet la même valeur aux deux étages.
  - **`--concurrency`** (CLI `jarvis process`/`jarvis parse` et
    `cmd/jarvisapp`), défaut **4** — aligné sur `n_slots = 4`, la valeur
    par défaut de `llama-server` observée dans ses logs de démarrage
    dans ce projet (aucun flag `--parallel` explicite dans le setup
    documenté ici). Contrairement à DPI/timeouts (déjà par défaut non
    nuls), ce n'est pas un paramètre de provenance : un défaut non nul
    est cohérent avec le reste de la CLI.
  - **Un vrai data race trouvé par `go test -race` en écrivant les
    tests de ce jalon**, pas en relecture : `llm.FakeClient` et
    `vlm.FakeClient` accumulaient `Calls` via un `append` non protégé —
    inoffensif tant que rien n'appelait ces fakes en parallèle, ce qui
    n'arrivait jamais avant ce jalon. Un test de parallélisme du jalon
    21 (`TestPipeline_Run_ConcurrencyThreadedToParsing`) l'a immédiatement
    fait échouer avec `-race`. Corrigé (mutex sur `Calls` dans les deux
    fakes) — même classe de bug que les races déjà trouvées et
    corrigées aux jalons 8 et 12, cette fois dans du code de test
    partagé plutôt que applicatif.
  - Les tests de parallélisme (dans les trois paquets) suivent le même
    schéma : un client fake qui piste son nombre d'appels **réellement
    simultanés** via un compteur atomique, échoue si la borne configurée
    est dépassée, et le test vérifie après coup que le maximum observé a
    bien atteint ≥ 2 — preuve positive qu'un vrai parallélisme a eu
    lieu, pas seulement qu'aucune erreur n'est remontée (un bug qui
    resterait strictement séquentiel malgré `Concurrency > 1`
    passerait un test qui se contenterait de vérifier l'absence
    d'erreur).
  - **Validé en conditions réelles, avec mesure** : un document natif de
    4 pages généré pour l'occasion, traité par `jarvis process --doc-type
    facture` contre les deux vrais serveurs — `--concurrency 1` :
    **~51s** ; `--concurrency 4` : **~35.5s** (environ 30% plus rapide).
    Résultats extraits strictement identiques entre les deux runs (diff
    vide), confirmant que la parallélisation ne change aucune valeur
    extraite. Gain réel mais **inférieur au x4 théorique** : la
    classification (jalon 15) reste un appel séquentiel avant
    l'extraction, et les 4 "slots" de `llama-server` partagent le même
    GPU Metal sous-jacent — pas 4 machines indépendantes, juste un
    meilleur pipelining des requêtes. Attendu, pas un signe de bug.
  - Testé : `internal/parsing`/`internal/extraction` (comportement
    séquentiel inchangé à `Concurrency` ≤ 1, parallélisme réel et borne
    respectée à `Concurrency` > 1, ordre des résultats préservé, contexte
    annulé retourne toujours une erreur même en mode parallèle),
    `internal/pipeline` (`Concurrency` bien transmis aux deux étages,
    pas seulement déclaré sur le champ) + la mesure réelle ci-dessus.
- **Jalon 21 bis — correction après retest sur de vrais documents scannés
  : fait, validé end-to-end.** Demandé explicitement ("Peux tu réessayer
  avec tous les documents qu'on a dans MongoDB pour voir si ça
  fonctionne") après l'upload d'un document réel resté "très long". Deux
  problèmes réels trouvés, distincts de tout ce que la validation
  synthétique du jalon 21 avait pu révéler — celle-ci ne portait que sur
  un document 100% texte natif, donc sans aucun appel VLM réel en
  parallèle, et avec un contenu par page trop court pour révéler quoi
  que ce soit côté LLM non plus.
  - **Finding 1 — `Pipeline.Concurrency` unique appliqué au VLM était
    dangereux.** Sur `Devis 160-2026.pdf` (5 pages scannées, tableaux de
    prix denses), `--concurrency 4` a fait échouer le serveur VLM :
    `HTTP 500 "failed to process mtmd chunk"` sur la première page,
    puis timeouts en cascade sur les suivantes (le serveur, débordé,
    n'a jamais répondu dans le délai). Isolé en testant une page seule,
    hors pipeline, hors concurrence : **156s pour une seule page**, déjà
    au-delà de l'ancien défaut `--vlm-timeout` de 120s. Deux causes
    distinctes, pas une seule : le VLM (appels multimodaux) ne supporte
    pas la concurrence sur ce matériel, et une page dense peut
    légitimement dépasser l'ancien timeout même seule.
  - **Finding 2 — la concurrence côté LLM aussi, mais seulement sur du
    contenu dense produit par le VLM.** Avec le VLM repassé à 1 (donc
    plus de finding 1), `--llm-concurrency 4` a fait échouer l'extraction
    sur 4 des 5 pages : `HTTP 500 "Context size has been exceeded"`.
    Vérifié isolément : la même page, envoyée seule au LLM sans aucune
    concurrence, s'extrait sans erreur en 31.8s. Donc pas une page trop
    grosse pour le contexte du serveur (8192 tokens/slot, confirmé via
    `/props` et `/slots` — largement suffisant pour ~900 tokens de
    contenu) : une vraie contention sous charge, propre à ce matériel,
    qui n'était simplement jamais apparue sur le document synthétique
    tout-texte-natif utilisé au jalon 21 (pages bien trop courtes pour
    la révéler).
  - **Correctif : `Pipeline.Concurrency` (un seul champ) scindé en
    `VLMConcurrency` et `LLMConcurrency` (`internal/pipeline/
    pipeline.go`), tous deux à défaut **1** (séquentiel) désormais —
    et non plus 4 pour le LLM.** `--concurrency` remplacé par
    `--vlm-concurrency`/`--llm-concurrency` dans `cmd/jarvis process`,
    `cmd/jarvis parse` (VLM seul) et `cmd/jarvisapp`. `--vlm-timeout`
    par défaut relevé **120s → 240s** aux trois mêmes endroits (même
    logique que le jalon 11 sur `--llm-timeout`, "filet de sécurité",
    le vrai enseignement restant "ne pas paralléliser à l'aveugle sur du
    matériel non caractérisé"). Le gain de ~30% mesuré au jalon 21 sur
    du texte natif court reste valable pour qui le sait et relève
    explicitement `--llm-concurrency` en connaissance de son profil de
    contenu — mais ce n'est plus la valeur par défaut, faute de savoir
    caractériser à l'avance "contenu assez dense pour poser problème".
    Nouveau test `TestPipeline_Run_VLMAndLLMConcurrencyAreIndependent`
    (`internal/pipeline/pipeline_test.go`) : preuve que les deux champs
    sont bien indépendants (`VLMConcurrency=1` reste séquentiel pendant
    que `LLMConcurrency=3` parallélise sur le même `Run`), pas seulement
    documentée.
  - **Finding 3 (trouvé en revalidant les findings 1 et 2) — une page en
    échec VLM disparaissait silencieusement de `Result.Extraction`, sans
    aucun signal.** `Merge()` (jalon 5) exclut délibérément une page dont
    le parsing VLM a échoué — décision correcte en soi ("pas de fallback
    silencieux sur un texte vide ou inventé"). Mais en aval, ni la sortie
    CLI (`jarvis process`) ni l'UI web (`cmd/jarvisapp/viewmodel.go`,
    `ResultView.Pages`) ne construisent leurs vues à partir d'autre chose
    que `Result.Extraction` — qui ne contenait alors tout simplement
    aucune entrée pour cette page. Concrètement : sur `Devis
    160-2026.pdf`, la page 2 (un tableau de lignes de devis, ~1100€ de
    postes) a réellement échoué une fois au VLM lors d'un run complet du
    pipeline (probablement transitoire — la même page, testée seule
    juste après, a réussi sans erreur), et le document s'est quand même
    affiché comme un succès classifié, avec 4 pages sur 5 sans qu'aucune
    trace de la page manquante n'apparaisse nulle part. Corrigé :
    `pipeline.withVLMFailures` (`internal/pipeline/merge.go`) complète
    `Result.Extraction` avec une entrée `Failed: true` (raison VLM
    incluse) pour toute page exclue par `Merge`, appelé depuis `Run` et
    `RunAuto` juste après l'étage Extraction — cohérent avec la façon
    dont un échec d'extraction LLM est déjà signalé ailleurs. Testé :
    `internal/pipeline/merge_test.go` (`withVLMFailures` isolément) +
    `TestPipeline_Run_VLMFailureOnOnePage_StillSurfacedInExtraction`
    (bout en bout, avec assertion que le LLM n'est jamais appelé pour la
    page manquante — toujours "jamais de valeur inventée").
  - **Revalidé en conditions réelles, sur le document qui avait
    initialement révélé le problème** : `Devis 160-2026.pdf` (5 pages
    scannées) traité de bout en bout avec `VLMConcurrency=1`,
    `LLMConcurrency=1`, `--vlm-timeout 240s`, via le pipeline réel
    contre les deux serveurs — classification `devis` (confiance 0.99,
    cohérent avec le type assigné manuellement au jalon 20),
    ~1068s (~18 min, cohérent avec 5 pages denses à 150-230s/page côté
    VLM), 5 pages extraites, aucune erreur.
  - **Leçon retenue pour la suite** : une validation "réelle" qui ne
    couvre qu'un seul profil de contenu (ici : texte natif court) n'est
    pas une validation réelle du système — elle valide ce profil-là.
    Le parallélisme suffisamment sûr pour être un défaut doit être
    revalidé sur le pire profil de contenu attendu (page scannée dense),
    pas seulement sur le cas le plus simple à générer synthétiquement.
- **Jalon 22 — refonte de l'interface web (bibliothèque, vue détail,
  texte OCR et analyse LLM conservés) : fait, validé sur les documents
  réels, en attente de validation utilisateur.** Demandé : "redesign
  the different views in a more modern layout", miniatures dans
  Documents, vue détail avec l'aperçu à gauche et les informations à
  droite, et "keep the OCR version of the document and the LLM analysis
  in the page (and in the DB)".
  - **Ce qui manquait réellement en base** (vérifié avant de coder) :
    l'analyse LLM était déjà entièrement persistée (`result_json` :
    JSON par page, fusion, modèle, prompt), le Markdown VLM aussi
    (`Result.Parsing`). **Le texte des pages natives, lui, n'existait
    que concaténé dans `search_text`, sans frontière de page.**
    `pipeline.Result.Pages []PageContent` (nouveau) conserve le texte de
    chaque page tel qu'envoyé au LLM, avec sa provenance (natif/VLM) —
    renseigné sur tous les chemins de `Run`/`RunAuto` dès que
    Triage+Parsing ont abouti (y compris extraction en échec ou type non
    reconnu). Persisté automatiquement via `result_json`, aucune
    migration. Un résultat antérieur n'a que le Markdown VLM : la vue le
    dit explicitement (ré-extraire capture le reste) plutôt que
    d'afficher un onglet vide.
  - **Miniatures** : `JobManager.Thumbnail(ctx, id)` rend la page 1 à
    `ThumbnailDPI` (40) via le port `parsing.Renderer` déjà existant
    (`PdftoppmRenderer`, aucun nouvel outil), **à la demande** puis la
    persiste via `Store.SetThumbnail` (nouveau, `$set` ciblé, jamais via
    `Update` — cf. ci-dessous). Les documents existants en obtiennent
    une au premier affichage, sans migration. Un échec de rendu n'est
    jamais mémorisé. `GET /documents/{id}/thumbnail` (`Cache-Control`
    long, la miniature d'un document ne change pas).
  - **`ListQuery.SummaryOnly`** : la liste chargeait jusqu'à 200 PDF
    complets (+ résultats) pour n'afficher que des métadonnées.
    `MongoStore` projette désormais sans `content`/`result_json`/
    `thumbnail` ; la `FakeStore` retire aussi ces champs, pour qu'un
    appelant qui en dépendrait par erreur échoue dès les tests unitaires.
    Opt-in, pas par défaut : `RecoverOrphaned` relit des jobs puis les
    repasse à `Update`, qui écraserait `result_json` avec un job
    tronqué — même raison pour laquelle la miniature a son propre
    `SetThumbnail`.
  - **UI** (`cmd/jarvisapp/templates`) : jeu de variables CSS avec thème
    clair **et** sombre (toutes les couleurs via variables — le bug
    "blanc sur blanc" de l'ex-codebrowser ne peut pas revenir), polices
    système (rien chargé depuis Internet). Importer : zone de
    glisser-déposer (l'input fichier recouvre la zone, aucun JS). Documents :
    grille de cartes avec miniature (`loading="lazy"`), type, statut,
    tags. Détail : `iframe` du PDF à gauche, volet droit (`DocumentPanel`,
    `GET /documents/{id}/panel`) à quatre onglets — **Données** (fusion,
    barre de confiance, extrait source), **Texte OCR** (par page, source +
    modèle), **Analyse LLM** (par page : champs, modèle, prompt, JSON
    brut), **Infos** (tags, changement de type, triage, suppression). Le
    volet se sonde lui-même pendant le traitement et la ré-extraction le
    remplace en place (plus de `JobFragment` sur la page détail).
    **La sortie du VLM est affichée comme du texte, jamais interprétée
    comme du HTML** (elle contient des `<table>` : c'est la sortie d'un
    modèle qui lit un PDF arbitraire) — testé explicitement. Le bouton
    "Rescanner le code" n'apparaît plus que sur Classes/Tests.
  - **Finding sur les données réelles** : `Devis 160-2026.pdf` tel que
    stocké dans Mongo date du run parallèle d'avant le jalon 21 bis
    (08:41) — 5 pages VLM en échec (`Context size has been exceeded`),
    aucune extraction. La revalidation du 21 bis était passée par la
    CLI, pas par l'UI : l'enregistrement n'a jamais été retraité.
    L'ancienne vue le masquait, la nouvelle l'affiche. À ré-extraire
    (~18 min, VLM séquentiel).
  - Testé : `internal/pipeline` (`Pages` sur Run mixte natif+VLM,
    extraction en échec, RunAuto non classé), `internal/webapp`
    (`SetThumbnail`, `SummaryOnly`, `Thumbnail` : rendu unique puis
    servi depuis le store, job inconnu, erreur de rendu non persistée,
    pas de Renderer), `internal/webapp/mongo_store_test.go` (réel,
    Atlas : miniature qui survit à `Update`, projection `SummaryOnly`),
    `cmd/jarvisapp` (miniature servie, grille, deux volets dans l'ordre,
    texte OCR échappé, provenance LLM, résultat antérieur, volet qui
    sonde/ne sonde plus). Suite complète verte (`gofmt`, `go vet`,
    `go test ./... -race -tags=integration`, `MONGO_URI` exporté).
    Validation visuelle : binaire lancé sur :8091 contre la vraie
    collection, captures Chrome headless des quatre vues (l'aperçu PDF
    n'est pas rendu en headless — pas de lecteur PDF intégré).
  - **Mesures VLM préliminaires (piste "rapidité", non implémentée)** —
    page 2 du Devis, dense : la **génération** domine (~2500 tokens de
    HTML de tableau fidèle, 250-370s), la lecture de l'image beaucoup
    moins (4158 tokens / ~60-90s à 200 DPI, 1587 tokens / ~21s à 1288 px).
    Réduire l'image dégrade la transcription ("toiture" → "toilette",
    une ligne de sous-total mal placée) pour un gain < 15% : **200 DPI
    conservé**. Le prompt natif d'olmOCR ne réduit pas la sortie. Le
    débit de génération chute run après run (10 → 6.8 tokens/s sur 4
    runs consécutifs) : **throttling thermique du MacBook Air (sans
    ventilateur)** — à contrôler dans toute comparaison. Pistes restantes :
    quantization plus légère (Q4_K_M, génération limitée par la bande
    passante mémoire) ; bbox des pages scannées via Apple Vision
    (prototype : 586 mots positionnés en 2.4s sur la même page, accents
    corrects, sur le Neural Engine donc sans concurrence avec le VLM).
- **Jalon 23 — pages enregistrées au fur et à mesure : fait, validé
  end-to-end (cf. mesure ci-dessous).** Question de l'utilisateur : "et
  si on envoyait les pages une par une ?" — c'était déjà le cas côté VLM
  (une requête par page, séquentiel depuis le 21 bis) ; ce qui manquait
  était d'**enregistrer et afficher chaque page dès qu'elle est lue**, au
  lieu d'attendre la fin du document (~18 min sans rien voir sur le
  Devis). Le temps total ne change pas, le temps avant de voir quelque
  chose passe de la durée du document à celle d'une page.
  - **`parsing.Parser.OnPage`** et **`extraction.Extractor.OnPage`** :
    rappel après chaque page terminée (échecs compris), avant la page
    suivante en séquentiel ; **sérialisé par le Parser/Extractor en mode
    parallèle** (mutex) — l'appelant, qui écrit en base, n'a pas à être
    sûr en concurrence. Testé en parallèle avec une map non protégée
    dans le rappel : `-race` détecterait un appel concurrent.
  - **`pipeline.Progress`** (`internal/pipeline/progress.go`) : étape
    (`parsing`/`classification`/`extraction`), `PageCount`,
    `ParseTotal/ParseDone`, `ExtractTotal/ExtractDone`, texte déjà
    disponible (`Pages`, même forme que `Result.Pages` : natif dès le
    triage, VLM au fil de l'eau) et `ParseFailures`. Émis après le
    triage puis après chaque page lue/extraite. **Chaque événement est un
    instantané indépendant** (tranches copiées) — testé : un événement
    conservé ne change pas quand les suivants arrivent.
  - **Signatures** : `RunAuto(ctx, path, onProgress)` et
    `RunWithType(ctx, docType, path, onProgress)` (nil accepté) —
    paramètre explicite plutôt qu'une valeur cachée dans le contexte.
    `Run` (CLI) inchangé. `webapp.Runner` suit.
  - **Persistance** : `Store.SetProgress(ctx, id, *Progress)` — écriture
    ciblée (`progress_json`, `$unset` pour nil), jamais via `Update`,
    même raison que `SetThumbnail`. `JobManager` enregistre chaque étape
    (`progressRecorder` ; un échec d'écriture est journalisé sans
    interrompre le traitement), efface l'avancement au début de chaque
    ré-extraction, et le **conserve ensuite** : un job interrompu par un
    redémarrage (`RecoverOrphaned`) garde les pages déjà lues. Exclu des
    listes `SummaryOnly`.
  - **La `FakeStore` mentait sur `Update`** (trouvé en écrivant les
    tests de ce jalon, introduit au jalon 22) : elle remplaçait le job
    entier, effaçant la miniature que `MongoStore.Update` ne touche pas.
    Corrigé — `FakeStore.Update` préserve désormais `Content`,
    `Thumbnail` et `Progress` comme Mongo, verrouillé par
    `TestFakeStore_Update_PreservesThumbnailAndProgress`. Sans ce
    correctif, un `finish()` effaçant l'avancement serait passé
    inaperçu en test et aurait fonctionné en production par hasard.
  - **UI** : le volet d'un document en cours affiche l'étape ("Lecture
    OCR (VLM) 2/5", "Classification…", "Extraction des données (LLM)
    3/5"), une barre d'avancement et le **texte déjà lu** ; la carte de
    suivi de la page Importer affiche le même compteur et un lien "Suivre
    dans le document". Un document interrompu sans résultat garde son
    texte partiel dans l'onglet Texte OCR.
  - Testé : `internal/parsing`/`internal/extraction` (rappel par page,
    ordre, échecs, sérialisation en parallèle), `internal/pipeline`
    (séquence exacte d'événements sur un document mixte natif+VLM avec
    échec VLM, instantanés indépendants, `RunWithType` sans étape de
    classification), `internal/webapp` (Fake + Mongo réel : aller-retour,
    survie à `Update`, effacement ; `JobManager` : avancement visible
    pendant le traitement, effacé à la relance, conservé après
    `RecoverOrphaned`), `cmd/jarvisapp` (volet en cours : étape,
    compteur, texte déjà lu ; carte d'upload ; document interrompu). Suite
    complète verte (`-race -tags=integration`, `MONGO_URI` exporté).
  - **Validé en conditions réelles** (binaire lancé sur :8091 contre la
    collection `jobs_test`, vrais serveurs VLM/LLM) : pages 1-2 du Devis
    (scannées, denses). Upload 23:02:00 → **page 1 lisible dans le volet à
    23:05:01 (2 min 54 s)**, page 2 terminée à 23:08:58, extraction,
    terminé à 23:09:29 (7 min 25 s) — l'avancement stocké est resté
    cohérent à chaque étape et conservé après la fin. Job de test supprimé.
  - **Finding (hors périmètre, non corrigé) : la page 2 a dépassé le
    `--vlm-timeout` de 240 s** (`context deadline exceeded`), sur une
    machine chaude après une soirée de benchmarks — cette même page a
    pris 252 à 463 s aux mesures du jalon 22 selon l'état thermique. Le
    défaut du 21 bis est donc trop juste pour une page dense sur ce Mac.
    Le suivi page par page l'a rendu visible immédiatement (erreur
    affichée sur la page concernée pendant que le reste continuait).
    **Défaut `--vlm-timeout` relevé 240 s → 600 s** (CLI `process`/
    `parse` et `cmd/jarvisapp`, accord de l'utilisateur) : marge au-dessus
    du pire cas mesuré (463 s), toujours un filet contre une génération
    qui ne s'arrêterait pas (cf. page blanche, jalon 3). Pistes de fond
    inchangées : Q4_K_M, et/ou la machine dédiée évoquée par
    l'utilisateur. Détail cosmétique relevé au
    passage : le message d'erreur VLM est préfixé deux fois
    (`vlm: vlm: ...`).
- **Jalon 24 — Classes en fiches UML + code coloré, modèle de données
  navigable, code des tests : fait, en attente de validation
  utilisateur.** Demandé : "une représentation plus graphique des objets
  avec le détail du code pour les générer [...] le code couleur par
  défaut de Visual Studio Code [...] retirer tout ce qui est répétitif
  github...Jarvis [...] une représentation graphique du modèle de données
  dans lequel je pourrais naviguer [...] voir le code du test avec aussi
  le code couleur".
  - **`internal/highlight`** (nouveau, bibliothèque standard seule —
    `go/scanner`) : coloration Go aux couleurs de VS Code Dark+
    (mots-clés `#569cd6`, contrôle `#c586c0`, types `#4ec9b0`, fonctions
    `#dcdcaa`, variables `#9cdcfe`, chaînes `#ce9178`, nombres `#b5cea8`,
    commentaires `#6a9955`). Classification lexicale comme la grammaire
    TextMate de VS Code (pas de jetons sémantiques) : nom après `type`/
    `func`, appel suivi de `(`, sélecteur après un package importé
    (alias compris, imports détectés dans la source), type connu hors
    position de nom (`Result *Result` : le premier est un champ). La
    concaténation des jetons reproduit exactement la source (testé).
    Les blocs de code restent sombres quel que soit le thème de l'app
    (le rendu VS Code reconnaissable) ; tabulations affichées en 4
    espaces (une tabulation dans un `<pre>` s'alignait sur un taquet
    décalé par la gouttière des numéros de ligne — trouvé sur capture).
  - **`internal/codemap`** : types écrits courts (`[]extraction.Result`,
    non qualifiés dans leur propre package — plus de chemin d'import
    complet), texte source (doc comprise) + fichier:ligne de chaque type
    et méthode, `FieldInfo.Refs/Many/Optional` (types du module
    référencés par un champ, à travers pointeurs/tranches/maps/génériques,
    cardinalité), `Model.ModulePath` + `ShortPath`, `Package.ImportNames`.
    **Trou comblé au passage** : les méthodes d'une interface n'étaient
    jamais listées (portées par le type sous-jacent, pas par le type
    nommé) — fiches et diagramme vides pour toute interface.
  - **`internal/testmap`** : `TestFunc.Source` (doc comprise) et
    `Imports` (noms d'import du fichier, pour la coloration).
  - **`internal/diagram`** (nouveau, fonction pure sur `codemap.Model`) :
    voisinage d'un type sur N sauts — ce qu'il contient à droite, ce qui
    le référence à gauche (couche = distance signée), ordre des colonnes
    par barycentre, flèches partant de la ligne du champ, arrivées
    réparties sur le bord de la cible (sinon convergence en un point et
    cardinalités superposées — trouvé sur capture), lignes masquées
    au-delà d'une limite sauf celles qui portent une flèche. Aucune
    bibliothèque de graphes ni JS externe : SVG rendu côté serveur, petit
    script de zoom/déplacement inline.
  - **UI** : **Classes** — barre latérale en chemins relatifs avec
    filtre, fiche UML (stéréotype, visibilité +/-, types de champs
    cliquables), relations (embed, implémente, implémenté par, référencé
    par), code source coloré de la déclaration et de chaque méthode.
    **Modèle** (nouvel onglet, `GET /model`) — diagramme centré sur un
    type, clic = recentrer, profondeur 1-3, interfaces implémentées en
    option, fiche UML du type central à droite. Type par défaut : celui
    qui contient le plus d'autres **structs** (`pipeline.Result` sur
    Jarvis) — ni le plus référencé (`schema.Field`, une brique), ni
    l'orchestrateur relié au plus d'interfaces (`Pipeline`), deux
    premiers choix écartés après capture. **Tests** — chemins relatifs,
    chaque test dépliable sur son code coloré (bouton ▶ hors du
    `<summary>` pour ne pas plier/déplier en lançant).
  - Testé : unitaire pour chaque paquet (`highlight` : classes et cas
    pièges ; `codemap` sur le module Jarvis lui-même ; `testmap` sur un
    module synthétique ; `diagram` sur un modèle construit à la main :
    côtés, cardinalités, ancrage sur la ligne du champ, profondeur,
    interfaces, non-chevauchement, limite de lignes, arrivées réparties,
    largeur d'en-tête) + `cmd/jarvisapp` (rendu des trois pages, liens,
    échappement du code, type par défaut). Suite complète verte
    (`-race -tags=integration`). Validation visuelle : binaire sur :8091
    (collection `jobs_test` — une instance sur la vraie collection
    marquerait en échec les jobs en cours de l'application principale au
    démarrage), captures Chrome headless des trois pages ; quatre défauts
    trouvés ainsi et corrigés (indentation, arrivées de flèches, type par
    défaut ×2, chevauchement d'en-tête).
- **Jalon 25 — tous les types de fichiers (remplacement de Google
  Drive) : fait, validé end-to-end (cf. mesure ci-dessous).** Demandé :
  "gérer également d'autres types de fichier [...] txt, doc, docx, ppt,
  pptx, excel (tous les types), etc. [...] Vérifie que la preview
  fonctionne pour chaque type de document." Décisions de l'utilisateur
  (questions posées) : **LibreOffice installé** (`brew install --cask
  libreoffice`, MPL, local) ; types en plus : images, texte et données,
  e-mails `.eml`, et **tout le reste stocké tel quel** ; **tous les
  documents lisibles passent par classification + extraction** comme
  les PDF.
  - **Principe : une version PDF par fichier.** `internal/formats`
    (nouveau) : `Detect(nom, premiers octets)` → famille (`pdf`, `word`,
    `slides`, `sheet`, `csv`, `text`, `html`, `image`, `email`, `other`)
    + MIME ; l'extension fait foi, sauf une signature PDF ; sans
    extension connue, le contenu tranche (PDF, image, texte UTF-8 ou
    Windows-1252, sinon stocké). Port `Converter` → `Rendition{PDF,
    Preview}` ; `LocalConverter` : LibreOffice (profil dédié — sinon
    échec silencieux si l'utilisateur a LibreOffice ouvert ; une instance
    à la fois ; délai 3 min) pour Word/présentations/tableurs (+ aperçu
    HTML de toutes les feuilles), CSV mis en tableau par Jarvis puis
    PDF, texte ré-encodé en UTF-8 puis PDF (import "Text (encoded)"),
    e-mail et page web **convertis via leur texte, jamais leur HTML**
    (LibreOffice irait chercher les images distantes — pixels de suivi,
    données hors de la machine) ; `sips` pour les images, **normalisées
    en page A4** (2400 px à 205 DPI — sinon une photo de 4000 px devient
    une page de 1,4 m rendue à 200 DPI pour le VLM), avec aperçu JPEG
    pour HEIC/TIFF/BMP. Le pipeline (triage → VLM → LLM), les miniatures
    et la recherche fonctionnent sur la version PDF **sans avoir été
    modifiés**. Deux pièges LibreOffice trouvés en testant : un `.html`
    s'ouvre en "Writer/Web", sans export Word/PDF (import `HTML
    (StarWriter)` imposé) ; LibreOffice **sort en 0 même quand la
    conversion échoue** ("source file could not be loaded") — c'est
    l'absence du fichier produit qui fait foi, testé sur un docx tronqué.
  - **Deux paquets confiés à des agents en parallèle** (proposition de
    l'utilisateur, chacun dans un worktree isolé, spécification d'API
    imposée, TDD, stdlib seule) puis intégrés : `internal/email`
    (RFC 5322/MIME écrit à la main — `textproto`/`multipart`
    abandonnent tout le message sur une ligne malformée ; base64/QP,
    RFC 2047/2231, jeux de caractères Latin-1/9/Windows-1252 en tables,
    noms de pièces jointes réduits à leur dernier composant, 17 `.eml`
    de test, fuzzing) et `internal/textview` (décodage UTF-8/UTF-16/
    Windows-1252, détection texte/binaire, CSV à séparateur deviné — `;`
    des exports Excel français sans confondre les décimales `12,50`).
  - **Stockage : fichiers dans GridFS** (bucket `<collection>_files`,
    même driver) — la limite de 16 Mo d'un document MongoDB aurait
    bloqué tout fichier lourd. `Store.WriteFile/ReadFile` (original,
    version PDF, aperçu), **`Get` ne charge plus le fichier** (le volet
    d'un document en cours le retéléchargeait depuis Atlas toutes les
    2 s), `Delete` retire aussi les fichiers ; les jobs d'avant ce jalon
    (PDF dans le champ `content`) restent lisibles sans migration.
    `Job` gagne `Format`, `MIME`, `Size` et `SourceHash` (SHA-256, la
    provenance du brief — la copie locale `--out-dir` l'utilise).
    `ListQuery.Format` (filtre par type ; un job sans format est un PDF).
  - **File d'attente globale** (`JobManager.Concurrency`, défaut 1) :
    déposer 20 fichiers d'un coup ne lance plus 20 traitements
    simultanés sur les modèles (cf. jalon 21 bis) ; les jobs attendent
    en `pending`, `StartedAt` au démarrage réel. La conversion n'a lieu
    qu'une fois : une ré-extraction réutilise la version PDF. Les
    fichiers seulement stockés se terminent sans conversion ni pipeline.
  - **Import** : plusieurs fichiers d'un coup, tous types, 512 Mio par
    fichier. **Dossier surveillé** : tous les fichiers sauf cachés,
    verrous d'Office (`~$`) et téléchargements en cours.
  - **Aperçus** (vue détail, volet gauche) : PDF fidèle (PDF, Word,
    présentations), tableau HTML de toutes les feuilles (tableurs), CSV
    en tableau, texte, page web, e-mail (en-têtes, corps, pièces
    jointes), image (JPEG de conversion pour HEIC/TIFF), fiche +
    téléchargement pour le reste. Les aperçus natifs affichent du
    contenu fourni par l'utilisateur : **servis avec une CSP stricte**
    (`default-src 'none'` + `sandbox` — ni script ni ressource distante)
    **dans une iframe en bac à sable**. Téléchargement de l'original
    partout (nom UTF-8 conservé, RFC 5987). Bibliothèque : badge
    d'extension, icône pour les fichiers sans miniature, filtres par type.
  - **Corpus de test** `testdata/formats/` (22 fichiers, un par format :
    docx, doc, odt, rtf, pptx, ppt, odp, xlsx, xls, ods, csv
    Windows-1252, txt, md, json, html, jpg, png, heic, tiff, eml
    multipart avec pièce jointe et en-têtes encodés, zip, pdf), généré
    par `scripts/gen_format_fixtures.sh` à partir de sources écrites à
    la main (aucun document personnel ; les formats vérifiés avec
    `file`). Piège du script lui-même trouvé par les tests : le CSV
    Windows-1252 importé avec le jeu de caractères UTF-8 (option 76 du
    filtre CSV) cassait les accents des tableurs générés.
  - Testé : unitaire (`formats` détection ; `webapp` stockage des
    fichiers, file d'attente, conversion une seule fois, fichiers
    stockés, miniatures, filtre par format ; `cmd/jarvisapp` import
    multiple, téléchargement, aperçu de chaque famille avec CSP,
    image/HEIC, vue détail par famille, grille ; `watch`) ;
    intégration : **conversion réelle des 16 formats convertibles** avec
    le texte attendu dans chaque PDF, aperçus des tableurs, images en
    page A4 sans couche texte, docx tronqué → erreur explicite ;
    `MongoStore` réel (**fichier de 17 Mio** via GridFS, écrasement,
    suppression, job ancien à contenu inline).
  - **Validé en conditions réelles** (binaire sur :8091, collection
    `jobs_test`, vrais VLM/LLM) : **les 22 fichiers du corpus importés en
    un seul envoi**, traités un par un par la file (00:15 → 00:30, 15
    min), **aucun échec**. Pour chacun : original téléchargeable, PDF
    servi (sauf le zip, voulu), aperçu natif (texte, Markdown, JSON, CSV,
    page web, e-mail, 3 tableurs), image (HEIC servi en JPEG), miniature
    (sauf le zip). **Les 13 déclinaisons de la même facture** (docx, doc,
    odt, rtf, pdf, pptx, ppt, odp, html, eml, json et les 4 images lues
    par le VLM, HEIC compris) **classées `facture` avec les mêmes valeurs
    exactes** (Atelier Dubois SARL, FAC-2026-0917, 2317,20). Relevé
    bancaire et liste de courses : aucun type, correct (type non
    enregistré). Captures Chrome headless de chaque famille (le PDF en
    iframe n'y est pas rendu : vérifié en extrayant le texte des PDF
    servis). Données de test supprimées ensuite.
  - **Deux défauts trouvés par cette validation, corrigés** : (1) **en
    mode sombre, les aperçus natifs étaient illisibles** — la page
    d'aperçu déclarait `color-scheme: light dark` dans une iframe à fond
    blanc, le navigateur passait le texte en clair (cellules du CSV,
    en-têtes de l'e-mail invisibles) ; couleurs claires imposées, testé.
    (2) Le pixel de suivi de l'e-mail de test, **bien bloqué par la
    CSP**, s'affichait comme une image cassée : les images distantes sont
    masquées.
  - **Constat, non corrigé** : le décodage d'un HEIC par `sips` passe par
    le matériel graphique — **49 s quand le VLM occupe le GPU**, moins
    d'une seconde sinon. Sans effet en usage normal (la file d'attente
    ne convertit qu'un fichier à la fois, quand aucun autre traitement ne
    tourne) ; à garder en tête si la file passe à plusieurs documents
    simultanés.
- **Jalons 26-27 — tickets + agent d'analyse local : faits, validés sur
  un vrai ticket, en attente de validation utilisateur.** Demandé :
  "que mon application s'auto-écrive : un outil de ticketing où je crée
  un ticket [...] et un agent LLM local le prend, l'analyse, le développe,
  le teste et le déploie." Proposition faite (cycle, garde-fous, choix du
  modèle, découpage en jalons 26-29) et décisions de l'utilisateur :
  **deux validations humaines** (le plan, puis le diff avant
  déploiement ; déploiement automatique éventuellement plus tard pour les
  petits tickets) ; commencer par les jalons 26-27.
  - **Faisabilité vérifiée avant de coder** : le Qwen3-8B déjà servi
    appelle correctement un outil via llama.cpp (API compatible OpenAI,
    `tools`/`tool_calls`) en ~4 s. Mais le Mac est plein (0,08 Go libre
    sur 24 Go avec VLM + LLM chargés) : le modèle de code du jalon 28
    tournera sur la machine dédiée évoquée par l'utilisateur (i9, 64 Go ;
    piste à mesurer : un modèle de code "à experts" type
    Qwen3-Coder-30B-A3B, ~3 G paramètres actifs par jeton, envisageable
    même sur CPU — choix à argumenter et valider, comme les autres).
  - **`internal/tickets`** (nouveau) : `Ticket` (titre, besoin, critères
    d'acceptation, statut, plan, fil d'événements), machine à états
    (`brouillon → analyse → plan_a_valider → plan_valide`, révision et
    relance possibles, annulé = clos), fil **en ajout seul** (`$push`
    atomique : un `Update` ne peut pas perdre une étape écrite par
    l'agent entre-temps), `FakeStore` + `MongoStore` (collection
    `tickets`). `Manager` : création, analyse en arrière-plan, révision
    (l'agent reçoit son plan précédent et les commentaires écrits
    depuis), validation, annulation, reprise au démarrage (analyse
    interrompue → échec).
  - **`internal/gate`** (nouveau) : file d'accès aux modèles **partagée**
    entre documents (`JobManager.Gate`) et tickets — les deux
    n'appellent jamais le LLM en même temps (cf. jalon 21 bis).
  - **`internal/agent`** (nouveau) : boucle d'agent écrite en Go (pas de
    framework — primitives, testable avec un modèle factice). **Aucun
    terminal** : quatre outils en lecture seule (`list_files`,
    `read_file`, `search`, `propose_plan`), **confinés au dépôt** (refus
    de `..`, chemins absolus, `.git`, binaires/données, et d'un lien
    symbolique vers l'extérieur — testé). Budget de contexte : le
    serveur a 8192 jetons, les anciennes sorties d'outils sont compactées
    (la plus récente reste intacte), sorties tronquées, limite d'étapes
    puis dernière chance où seul `propose_plan` est offert. Contexte du
    projet = début de CLAUDE.md jusqu'à "État des jalons" (4 000 car.).
    `HTTPModel` : format d'appels d'outils compatible OpenAI. Flags
    `--agent-url`/`--agent-model` (défaut : le LLM actuel — il suffira
    de pointer vers la machine dédiée), `--agent-context-chars`,
    `--tickets-collection`.
  - **UI** : onglet **Tickets** (création, liste avec statuts) ; page
    d'un ticket : besoin, critères, actions selon l'étape (lancer
    l'analyse, valider, demander une révision, annuler), **plan rendu
    échappé** (sortie d'un modèle : seuls les titres `##` deviennent des
    `<h4>`, testé avec un `<script>`), fil en direct (chaque action de
    l'agent, sa sortie repliée), commentaires.
  - **Validé sur un vrai ticket** ("Afficher le nombre de pages dans la
    grille des documents", 3 critères) — et **deux passes** : la
    première a révélé trois défauts **du harnais, pas du modèle** :
    l'agent a demandé 8 fois `templates/documents.html` (inexistant, le
    vrai est `documents.templ`) en recevant une erreur brute, jusqu'à la
    limite d'étapes (~18 min, plan vague) ; lire un dossier renvoyait un
    message absurde ; rien n'arrêtait un appel identique répété.
    Corrigés (TDD) : fichier introuvable → **fichiers au nom proche
    suggérés**, dossier → son contenu, **appel répété non réexécuté**
    (le modèle est prévenu, visible dans le fil), et le prompt explique
    les conventions (identifiants anglais, `.templ`). **Seconde passe sur
    le même ticket : 2 min 34 s, bon fichier trouvé, plan concret**
    (`PageCount` sur `DocumentRow`, affichage, cas zip). Il garde une
    erreur qu'une revue humaine voit aussitôt (propose de *calculer* les
    pages avec `pdfinfo` alors qu'elles sont connues au traitement) —
    exactement le rôle de la validation du plan. Leçon : **avec un
    modèle modeste, la qualité des outils (messages d'erreur qui
    orientent, garde-fous contre les boucles) pèse autant que le
    modèle.**
  - **Bug trouvé au passage, corrigé partout** : les heures étaient
    affichées en UTC (MongoDB rend les dates en UTC) — fil des tickets,
    liste, bibliothèque, vue détail, suivi, e-mails : heure locale, testé
    avec un fuseau fixé.
  - **Bug ancien trouvé par un test devenu intermittent, corrigé** :
    `JobManager` réécrivait le job à partir de sa copie prise au
    démarrage du traitement — des **tags posés pendant le traitement
    étaient effacés** à la fin, sans signal (plusieurs minutes de fenêtre
    pour un scan). La file d'attente du jalon 25 avait changé le minutage
    et rendu le test `TestHandleDocumentTags...` intermittent. Reproduit
    de façon déterministe (`TestJobManager_TagsSetDuringProcessingSurvive`)
    puis corrigé : le traitement écrit ses champs sur la version à jour du
    job (`save`). L'ancien test passe 100 fois sur 100.
  - Testé : `tickets` (machine à états, Fake, Mongo réel sur Atlas,
    Manager : étapes et plan, échec, révision, transitions, file
    partagée, reprise) ; `gate` ; `agent` (outils et confinement,
    boucle : enquête puis plan, prompt, réponse texte, limite d'étapes,
    erreur du modèle, compaction, appel répété, format HTTP) ; `webapp`
    (file partagée) ; `cmd/jarvisapp` (pages, création, analyse, plan
    échappé, fil, volet qui se sonde, actions, 409 sur transition
    refusée, heures locales). Suite complète verte (`-race
    -tags=integration`). Tickets de test supprimés.
  - **Suite prévue** : jalon 28 (l'agent développe dans une copie
    isolée — branche `ticket/<id>`, outils d'écriture confinés, tests
    exécutés sans `MONGO_URI`, diff soumis à revue), jalon 29
    (déploiement : fusion, reconstruction, redémarrage supervisé,
    retour arrière si l'application ne répond plus). Modèle de code sur
    la machine dédiée.
- **Jalon 28 — l'agent développe (POC) : mécanique faite et éprouvée sur le
  vrai système ; le développement réel échoue avec Qwen3-8B, comme
  anticipé.** Cadre posé par l'utilisateur : "on est sur le POC", modèle
  lent accepté, une vraie machine sera préparée — donc POC avec le
  Qwen3-8B déjà servi, aucun nouveau modèle téléchargé sans choix
  argumenté.
  - **`internal/workspace`** (nouveau) : copie de travail git par ticket
    (branche `ticket/<id>`, worktree hors du dépôt, `~/.jarvis/worktrees`),
    diff par rapport à main (fichiers nouveaux compris), commit sur la
    branche (auteur "Agent Jarvis"), suppression ; identifiants filtrés
    (ni `../`, ni option git). Toutes les commandes git sont lancées par
    Jarvis, jamais par l'agent ; main n'est jamais modifié ici. Testé sur
    un vrai dépôt git temporaire. **`Checker`** : vérifications (templ,
    gofmt, vet, build) et tests **dans un environnement vidé** (aucun
    secret — `MONGO_URI` compris —, `GOPROXY=off`), délai borné, **tout le
    groupe de processus tué au délai** (sinon le binaire de test lancé
    par `go test` survit) — testé, y compris un test qui dort une minute.
    Limite assumée et documentée : pas de vrai bac à sable (même
    utilisateur, réseau local possible) — le code écrit par l'agent
    s'exécute avant la revue humaine ; isolation réelle (conteneur/VM)
    prévue sur la machine dédiée. Vérification complète sur une copie
    neuve de main : 2 s de vérifications, 9 s de tests.
  - **`internal/agent`** : `DevTools` (lecture + `write_file`,
    `edit_file` à extrait exact et unique avec **lignes réelles proches
    montrées** si l'extrait est introuvable, `run_checks`, `run_tests` à
    motif de paquet relatif validé, `finish`) ; interdits : `go.mod`/
    `go.sum` (pas de dépendance nouvelle), `*_templ.go` (générés),
    CLAUDE.md, tout ce qui sort de la copie. **Boucle d'agent factorisée**
    (analyse et développement partagent compaction, refus des appels
    répétés — sauf tests/vérifications —, limite d'étapes, dernière
    chance). `Developer` : prompt TDD imposé (test, le voir échouer,
    implémenter, vert, vérifications, `finish`).
  - **`internal/tickets`** : étapes `developpement → diff_a_valider →
    accepte` (seconde validation humaine), relance depuis l'échec.
    Valider le plan lance le développement ; **Jarvis refait lui-même la
    vérification finale** (le modèle ne se juge pas) ; en échec, seconde
    tentative avec le rapport ; en succès, commit sur la branche et diff
    soumis à la revue ; un diff vide est un échec ; demander des
    changements relance l'agent avec le retour ; abandonner supprime copie
    et branche. `Branch`, `Diff`, `Report` persistés (Mongo réel testé).
  - **UI** : développement suivi en direct ; revue avec **diff coloré par
    fichier** (couleurs VS Code, **échappé** : c'est du code écrit par un
    modèle — testé avec un `<script>`), rapport de vérification, accepter /
    demander des changements / abandonner ; relancer le développement.
  - Flags : `--agent-dev` (défaut vrai), `--worktrees-dir`,
    **`--agent-timeout` (15 min par tour)** — le délai de l'extraction
    (180 s) a fait échouer le premier essai réel (tour de plus de 3 min).
  - **Essai réel** (ticket : reconnaître l'extension `.jpe` comme JPEG —
    une ligne dans `detect.go` plus un test) : **analyse réussie** (1 min
    47 s, bon fichier, bon plan) ; **développement en échec à chaque
    tentative** — alors que la recherche lui avait montré exactement les
    lignes à modifier, Qwen3-8B **réécrit le fichier entier**
    (`write_file`, 12 800 caractères), réponse coupée par le contexte de
    8192 jetons, appel d'outil au JSON invalide, erreur 500 de llama.cpp.
    Trois garde-fous ajoutés (TDD) : réécriture complète d'un fichier
    existant de plus de 60 lignes refusée ; réponse coupée non fatale
    (modèle prévenu, 3 fois) ; après une coupure, `write_file` retiré des
    outils. **Aucun n'a suffi** : à température 0 le modèle reproduit la
    même réponse coupée au caractère près. Conclusion assumée, sans
    empiler davantage de consignes : **la mécanique est prête, le
    développement autonome demande un modèle de code et un grand
    contexte** — la machine dédiée (cf. jalons 26-27). Ticket de test
    annulé : copie de travail et branche effectivement supprimées.
  - Testé : `workspace` (vrai git : branche isolée, idempotence, diff,
    commit hors de main, suppression, identifiants refusés ; `Checker` :
    succès/échec, environnement vidé, gofmt/vet/build, délai), `agent`
    (outils d'écriture et interdits, `edit_file` exact/introuvable/ambigu,
    motifs de paquet, développeur : écrit/teste/termine, prompt, copie de
    travail, limite, réécriture refusée, coupure récupérée/retrait de
    `write_file`), `tickets` (développement, vérification refaite,
    seconde tentative, diff vide, erreur de l'agent, revue, transitions,
    sans développeur), `cmd/jarvisapp` (diff échappé et coloré, rapport,
    accepter/changements, relance). Suite complète verte (`-race
    -tags=integration`).
  - **Aussi corrigé pendant ce jalon** : le lanceur ne démarrait plus
    depuis le Finder/Dock (PATH minimal sans Homebrew ni Go : ni
    `llama-server`, ni `go`, ni `soffice` trouvés) — `WithToolPaths`,
    testé, commit séparé.

- **Correctifs du harnais de l'agent (ticket réel "Ajouter commentaire sur
  document") : faits.** Le ticket de l'utilisateur échouait en
  développement. Diagnostic : plan déjà faux (fichiers sans rapport ; la
  vraie cible est `webapp.Job`, son store, la recherche et la vue détail),
  puis 40 étapes à chercher des identifiants inventés sans rien modifier ;
  la relance rejouait le même déroulé (température 0). Ticket trop gros
  pour Qwen3-8B à 8k de contexte — mais cinq défauts du harnais trouvés et
  corrigés (TDD, commit bfb701a) : stagnation détectée (recadrage après 6
  appels stériles d'affilée, arrêt à 12 — un échec rapide et expliqué
  plutôt que 40 étapes), température 0,5 à partir de la deuxième tentative,
  vérification "aucun changement" avant la vérification complète (plus de
  minutes de tests sur une copie intacte), délai et messages d'erreur.
  - **Bug trouvé en relançant ce même ticket, corrigé** : l'agent n'avait
    rien modifié, mais le ticket est passé en "diff à valider" avec un faux
    diff — `workspace.Diff` comparait la copie à la **pointe** de main, qui
    avait avancé depuis la création de la copie (commit bfb701a) : ces
    commits apparaissaient à l'envers, le contrôle "diff vide" ne voyait
    rien, et le résumé de l'agent ("champ Commentaire ajouté") était
    invraisemblable sans que rien ne le contredise. Diff désormais calculé
    depuis la **base commune** (`git merge-base`) ; une copie reprise sans
    travail propre est ramenée au main actuel (avance rapide seulement),
    sinon l'agent développerait sur du code périmé. Testé sur un vrai dépôt
    (main qui avance après la création de la copie). Accepter un diff ne
    fait encore que changer le statut (le déploiement est un jalon à
    venir) : main n'a jamais été touché.
- **Jalon 29 — page Architecture (choix techniques, jalons, commits,
  infrastructure en temps réel) : fait, en attente de validation
  utilisateur.** Demandé : "une page où je vois tous les choix techniques
  qu'on a faits, les différents commits avec l'explication, un schéma de
  l'infrastructure ; cette information devra être mise à jour en temps
  réel". Le déploiement des tickets, prévu au jalon 29, devient le jalon 30.
  - **Aucune donnée recopiée** : tout est relu à chaque affichage depuis
    les sources — CLAUDE.md (décisions, contraintes, jalons), git
    (commits, message complet, diff ; travail non commité) et les
    composants eux-mêmes (sondés). Tenir CLAUDE.md et les messages de
    commit à jour suffit à tenir la page à jour.
  - **`internal/projectinfo`** (nouveau, bibliothèque standard + `git`) :
    `Commits` (historique, jalon cité en tête du sujet), `Show` (diff d'un
    commit — le hash vient de l'URL : **hexadécimal uniquement**, jamais
    une révision ni une option, `--end-of-options`), `WorkingChanges`,
    `Fingerprint` (commit courant + fichiers modifiés et leur date +
    CLAUDE.md) ; `ParseProjectDoc` (sections `##`, jalons de "État des
    jalons" avec numéro/titre/statut, titres sur plusieurs lignes, sous-
    sections `###` en référence ; tolérant par construction, garde-fou sur
    le vrai CLAUDE.md) ; `RenderMarkdown` (le sous-ensemble utilisé ici,
    **tout échappé**) ; sondes (`LlamaCheck` : `/health` puis modèle servi ;
    `BinaryCheck`, `DirCheck`, `FuncCheck`), `Diagram.Probe` (en parallèle,
    délai commun) et `RenderSVG` (grille, liens verticaux entre rangées aux
    arrivées réparties, couleurs par classes CSS du thème).
  - **Temps réel sans tout redessiner** : la page sonde toutes les 4 s une
    empreinte (`/architecture/version`) — 204 si rien n'a changé, sinon
    `HX-Refresh` ; l'onglet courant survit au rechargement (ancre d'URL).
    Un commit, une modification de fichier ou de CLAUDE.md apparaît donc en
    quelques secondes, sans perdre sa place le reste du temps. Le schéma
    d'infrastructure se re-sonde toutes les 5 s.
  - **UI** (onglet Architecture) : **Infrastructure** (schéma SVG : entrées,
    application, traitements, modèles, stockage — état et détail réels de
    chaque composant : modèle servi, latence Atlas, documents en file,
    outils trouvés — et tableau), **Choix techniques** (décisions
    tranchées/en attente, contraintes, architecture, non-goals... avec
    sommaire ; setup et corpus en référence), **Jalons** (du plus récent,
    statut, détail, commits associés ; travail non commité en tête),
    **Commits** (message complet, diff coloré chargé à l'ouverture).
  - Deux titres de CLAUDE.md coupés sur deux lignes remis sur une (ils
    s'affichaient tronqués).
  - Testé : `projectinfo` (vrai dépôt git temporaire ; découpage d'un
    CLAUDE.md d'exemple et du vrai ; Markdown : listes imbriquées,
    continuations, code protégé, gras autour du code, échappement ; sondes
    contre `httptest` ; délai et parallélisme ; SVG), `cmd/jarvisapp`
    (page : décisions, jalons, commits, échappement ; empreinte inchangée
    puis changée ; diff d'un commit, révision non-hash refusée ; schéma
    sondé ; onglet de nav). Validation visuelle : binaire sur :8091
    (collections de test), captures des quatre onglets — deux défauts
    trouvés ainsi et corrigés (liens entre rangées qui s'entassaient sur le
    flanc d'un nœud ; gras contenant du code non rendu).

## Atelier de code (cmd/codebrowser) — travail parallèle, outil de développement

**Fusionné dans `cmd/jarvisapp` au jalon 13** — cette section décrit les
moteurs (`internal/codemap`/`internal/testmap`/`internal/testrunner`),
inchangés par la fusion ; pour l'usage, voir "Application web unifiée"
plus haut.
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
- Servi par `cmd/jarvisapp` (`GET /classes`, `GET /tests` — voir
  "Application web unifiée" plus haut ; anciennement un binaire séparé
  `cmd/codebrowser`, retiré au jalon 13). Le `Model` (types) et les
  `Category` (tests) sont calculés une fois au démarrage puis mis en
  cache dans `Server` (protégé par un `sync.RWMutex`) — `go/packages.Load`
  re-type-check tout le module à chaque appel, trop lent pour le refaire
  à chaque page vue. Bouton "↻ Rescanner le code" (`POST /refresh`) pour
  recalculer explicitement après une modification du code. Les derniers
  résultats de tests connus sont conservés à travers un rescan
  (rescanner le code ne relance pas les tests).
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
- **Validé manuellement, bout en bout, serveur réel lancé en local**
  (avant la fusion, sous l'ancien binaire `cmd/codebrowser` — mêmes
  moteurs, comportement inchangé) : `GET /classes/detail?...
  name=DocumentRecord` affiche correctement `RecordMeta` sous "Embed"
  (la relation d'héritage du jalon 9, visible dans le navigateur) ;
  `POST /tests/run?pkg=...` (catégorie) et `POST /tests/run?pkg=...
  &name=...` (test unique) exécutent vraiment `go test` et renvoient un
  statut `pass` correct pour des tests connus bons. Re-couvert par les
  tests automatisés du jalon 13 (`cmd/jarvisapp/server_test.go`).

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
- Validation réelle de `MongoStore` contre Atlas (jalon 12) — **résolue
  au jalon 15** : accès donné, testée manuellement puis avec des tests
  automatisés dédiés (`internal/webapp/mongo_store_test.go`,
  `-tags=integration`, `MONGO_URI` requis). Un bug réel de persistance
  (`doc_type` jamais mis à jour) a été trouvé et corrigé dans la foulée.
- Infrastructure MongoDB multi-provider + copie locale périodique
  (mentionnée par l'utilisateur au jalon 12) — pas détaillée, hors
  scope du code applicatif pour l'instant.
