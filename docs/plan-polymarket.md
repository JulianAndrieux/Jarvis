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

## Révision du 09/11/2026 : trois avantages candidats, comparés avant de choisir

Évaluation demandée par l'utilisateur (« challenge the idea, find other
options »), plan réécrit avec son accord. **Remplace les jalons 55-57 de la
révision du 03/11** ; le jalon 54 (collecte) reste tel quel et sert aux
trois études.

### Pourquoi la réaction aux annonces n'est plus l'hypothèse centrale

1. **Latence.** Flux relevés toutes les 2 min, GDELT tous les quarts
   d'heure, 13-19 s par texte pour le modèle local, puis Claude : suivre le
   sens d'une annonce est exclu. Ne restent que la sur-réaction (le cours
   revient) et les annonces qui demandent de comprendre.
2. **Mauvaise horloge.** L'heure d'un article est celle de sa publication,
   pas celle où l'information a atteint le marché. Mesurer « +1 min après
   l'annonce » montrerait surtout que le cours avait déjà bougé.
3. **Prix non exécutables.** L'historique du CLOB est une série de prix,
   sans achat/vente. Sur les marchés peu suivis — les seuls où un avantage
   est plausible — l'écart peut dépasser l'avantage. **Les carnets ne se
   rattrapent pas après coup.**
4. **Peu de données, beaucoup de cases.** 60 jours, une dizaine de cibles,
   un rattachement par mots-clés bruité, découpés par catégorie × horizon ×
   liquidité : une case paraîtra rentable par hasard.
5. **Claude avec recherche web ne se rejoue pas sur le passé** (il verrait
   l'issue). Il ne s'évalue qu'en avant, en temps réel.
6. **100 USDC et ¼ de Kelly** donnent souvent une taille sous l'ordre
   minimum. Ce capital est un budget d'apprentissage : **le gain n'est pas
   le critère de réussite de cette phase**, la qualité de la mesure l'est.

### Les trois avantages candidats

Aucun des deux nouveaux ne demande de vitesse.

- **A — Annonces, étude inversée.** Détecter d'abord les sauts de cours,
  puis chercher si nos sources les expliquent et avec quel retard. Ensuite
  seulement : le cours revient-il après un saut (sur-réaction) ?
- **B — Cohérence entre marchés.** Branches d'un événement dont la somme
  s'écarte de 1 ; échelles de dates non monotones (« d'ici décembre » ≤
  « d'ici mars ») ; marchés logiquement emboîtés qui se contredisent.
  Détection déterministe en Go ; Claude ne fait que proposer les liens
  logiques entre marchés d'événements différents, vérifiés ensuite par les
  prix. L'arbitrage simple de somme est sans doute déjà disputé par des
  robots ; les implications entre événements, moins.
- **C — Règles contre titre.** Marchés dont les règles de résolution
  disent autre chose que ce que la question laisse entendre. C'est de la
  lecture attentive de documents — le métier de Jarvis et de Claude. Ne se
  mesure qu'en avant.
- **Témoin — calibration des marchés résolus.** Les prix sont-ils biaisés
  par catégorie, tranche de prix, délai avant l'échéance (favori/outsider) ?
  Des milliers de marchés clos au lieu de 60 jours ; dit aussi où A, B et C
  ont le plus de chances d'exister.

Écartée pour l'instant : la tenue de marché avec récompenses de liquidité
(capital et disponibilité continue nécessaires ; la collecte d'actualités y
servirait de défense — retirer ses ordres à l'annonce). À reconsidérer si
le capital augmente.

### Règles de mesure (valent pour toutes les études)

- **Hypothèse, métrique et seuil écrits avant de regarder les données**,
  dans ce fichier. Période de contrôle mise de côté (les 15 derniers jours
  collectés), jamais consultée avant la fin de l'exploration.
- **Tout résultat est exprimé en prix exécutable** : achat au prix vendeur,
  vente au prix acheteur, frais du marché déduits, taille bornée par la
  profondeur réelle. Un avantage mesuré au prix milieu ne compte pas.
- Un résultat sans intervalle de confiance, ou obtenu sur moins de 30
  occurrences, est « non conclusif », pas « positif ».
- Claude est noté sur le cours quelques heures plus tard **et** à la
  résolution : la résolution seule prendrait des mois.

### Critères d'abandon (proposés, à valider par l'utilisateur)

| Piste | Abandonnée si |
|---|---|
| A, sens de l'annonce | la médiane du retard de nos sources sur le saut dépasse 5 min (attendu : oui) |
| A, sur-réaction | le retour moyen après un saut, net de l'écart et des frais, n'est pas positif sur la période de contrôle |
| B | moins d'une incohérence exécutable par semaine, ou gain net médian < 1 point après écart, frais et taille minimum |
| C | sur 30 marchés signalés, le cours ne va pas plus souvent vers la lecture de Claude que l'inverse |

### Jalons

- **55a — Carnets enregistrés (à faire en premier, urgent).** Meilleur
  achat, meilleure vente et profondeur des cibles, à intervalle régulier,
  dans `internal/collect`. Chaque jour sans est perdu pour les études.
- **55b — Trois études sur les données déjà collectées** (Go pur,
  déterministe, rejouable ; résultats dans l'onglet Marchés) : sauts puis
  annonces (A), calibration des marchés résolus (témoin), incohérences
  entre marchés (B, historique à la minute + carnets dès qu'ils existent).
  Livre un verdict par piste selon les critères ci-dessus.
- **56 — Modèle de la piste qui survit**, en simulation temps réel au prix
  du carnet. Si c'est C ou B-entre-événements : analyses de Claude
  conservées (prompt, modèle, sources). Si c'est A : juge des annonces,
  témoins Timestral/Chronos-2/naïf comme prévu le 03/11. Si rien ne
  survit : on s'arrête là, et c'est un résultat.
- **57 — Modèle de risque** (« Claude estime, Go décide du risque »,
  inchangé) avec une règle en plus : un ordre dont la taille de Kelly est
  sous le minimum du marché n'est pas arrondi vers le haut, il est refusé.
- **58 — Ordres réels** plafonnés (inchangé : portefeuille dédié ≤ 100
  USDC, clé chiffrée, refus hors IP luxembourgeoise, automatique sous un
  seuil).

## Révision du 03/11/2026 : l'impact des annonces d'abord (jalons 55+ remplacés par la révision du 09/11)

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
