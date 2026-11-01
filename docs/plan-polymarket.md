# Trading Polymarket dans Jarvis — plan et décisions

Demandé : « utiliser Jarvis pour faire du trading sur Polymarket », avec des
analyses de Claude et « un modèle de trading et d'analyse de risque ».

## Décisions de l'utilisateur (questions posées, 01/11/2026)

| Sujet | Décision |
|---|---|
| Juridiction | Luxembourg — absent de la liste des pays bloqués par Polymarket (vérifiée le 01/11/2026, page mise à jour le 29/10/2026). Voisins bloqués : France, Belgique, Allemagne, Pays-Bas. |
| Autonomie | Ordres **automatiques sous un seuil**, confirmation au-delà — seulement après la phase de simulation. |
| Analyse | Claude (via `internal/claudecode`), **avec recherche web**. |
| Stockage | Atlas, cloisonné par environnement. |
| Capital de départ | Portefeuille dédié, **≤ 100 USDC**. |

## Principe : Claude estime, Go décide du risque

1. **Analyse (Claude)** : règles de résolution, prix, carnet, actualité →
   probabilité, intervalle d'incertitude, arguments, ce qui ferait changer
   d'avis, ambiguïtés de résolution. Sortie structurée (`--json-schema`).
2. **Modèle de risque (Go pur, déterministe, testé)** : aucun modèle de
   langage ne décide d'une taille.
   - avantage réel = probabilité estimée − prix moyen d'exécution (carnet
     parcouru pour la taille voulue) − frais (`FeeSchedule` du marché) ;
   - taille par **Kelly fractionné** (¼ par défaut), la probabilité d'un
     modèle étant bruitée ;
   - plafonds : par ordre, par marché, par événement (marchés corrélés),
     exposition totale, perte maximale par jour et par semaine ;
   - refus : liquidité insuffisante, échéance trop proche, résolution
     ambiguë signalée par l'analyse, intervalle d'incertitude qui contient
     le prix.
3. **Exécution** : ordre proposé avec son calcul ; automatique sous le
   seuil, confirmation au-delà.

## Jalons

- **53 — Observer** (fait) : marchés, recherche, liste de suivi, carnets,
  historique, alertes. Lecture seule.
- **54 — Analyser et simuler** : analyses Claude conservées (prompt,
  modèle, sources : reproductibilité), modèle de risque, ordres simulés au
  prix réel du carnet, journal. **Calibration** à la résolution des
  marchés : score de Brier de Claude contre celui du marché, gain simulé.
  Critère de passage au 55 à fixer avec l'utilisateur sur ces chiffres.
- **55 — Ordres réels** : portefeuille dédié, clé chiffrée
  (`internal/secretbox`, jamais visible de Claude Code ni d'un ticket),
  signature EIP-712 des ordres, plafonds vérifiés par des tests,
  **refus d'envoyer si l'IP de sortie n'est pas luxembourgeoise**
  (déplacement dans un pays bloqué), seuil d'automatisme.

## Décisions encore ouvertes

- Signature des ordres en Go : la bibliothèque officielle dépend de
  `go-ethereum` (lourd, contraire à « dépendances remplaçables en une
  journée ») ; alternative : EIP-712 écrit à la main + petite bibliothèque
  secp256k1. À argumenter au jalon 55.
- Montant du seuil d'automatisme et plafonds exacts (après la simulation).
- Fréquence des analyses automatiques (coût Claude, quota).
