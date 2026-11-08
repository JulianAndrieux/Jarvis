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

## Révision du 03/11/2026 : l'impact des annonces d'abord

Demandé par l'utilisateur : collecter les actualités en parallèle des cours
pour mesurer l'impact d'une annonce, puis un modèle qui réagit à chaque
annonce. Décisions : sources GDELT + flux RSS ; catégories politique,
géopolitique, économie/banques centrales, crypto ; juge des annonces = le
modèle local (tri rapide) **et** Claude (annonces jugées pertinentes),
comparés en simulation.

Limite dite à l'utilisateur : sur les grands marchés, des robots réagissent
aux dépêches en secondes — Jarvis arrivera après le mouvement. L'avantage,
s'il existe, est à chercher sur les marchés peu suivis, dans la
sur-réaction (le cours revient), ou dans les annonces qui demandent de
comprendre. L'étude d'impact (55) dira lequel existe.

Timestral (Mistral, séries temporelles, licence « MIT modifié », CPU) :
témoin du jalon 56 sur la courbe seule, avec Chronos-2 et la référence
naïve (« le prix ne bouge pas ») — une cote d'événement bouge sur
l'actualité, pas sur son passé.

- **54 — Collecte** (fait) : prix à la minute (l'historique du CLOB remonte
  au moins à 60 jours), actualités GDELT + RSS rattachées par mots-clés.
- **55 — Étude d'impact** : mouvement à +1 min, +5 min, +1 h, +24 h après
  chaque annonce ; délai d'intégration ; sur/sous-réaction ; par catégorie
  et par liquidité. Décide s'il y a une fenêtre.
- **56 — Modèle de réaction** : juge des annonces (local puis Claude) →
  signal ; simulation en temps réel ; témoins Timestral/Chronos-2/naïf.
- **57+** : modèle de risque (Kelly fractionné, plafonds), puis ordres
  réels plafonnés (100 USDC, auto sous un seuil).

## Jalons (plan initial, remplacé par la révision ci-dessus)

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
