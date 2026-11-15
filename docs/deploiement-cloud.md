# Jarvis hébergé : s'y connecter de partout

Décisions de l'utilisateur (questions posées) : un **serveur européen +
Docker**, **le Mac reste le serveur de modèles**, accès **uniquement par
le réseau privé Tailscale**, tickets **en lecture et création seulement**
côté serveur.

## Ce que ça donne

```
 téléphone / portable ──(Tailscale, HTTPS)──┐
                                            ▼
                    ┌──────────── serveur (VPS) ────────────┐
                    │ tailscale  ──►  jarvisapp --role cloud │──► MongoDB Atlas
                    │ (aucun port ouvert sur Internet)       │
                    └───────────────────┬────────────────────┘
                                        │ Tailscale (WireGuard, chiffré)
                                        ▼
                    ┌──────────────── Mac ───────────────────┐
                    │ llama-server VLM :8080, LLM :8081       │
                    │ jarvisapp --role worker (tickets)       │──► MongoDB Atlas
                    └─────────────────────────────────────────┘
```

| | Serveur (`--role cloud`) | Mac (`--role worker`) |
|---|---|---|
| Interface (documents, notes, tâches, emails, marchés) | oui, toujours disponible | oui, en local |
| Traitement des documents | oui — l'inférence est demandée au Mac | non |
| Relève et tri des emails | oui (tri : modèle du Mac) | non |
| Collecte des marchés | oui | non |
| Tickets | créer, lire, commenter, suivre en direct | analyse, développement, déploiement |
| Modèles (VLM, LLM) | aucun | oui |

**Mac éteint ou en veille** : tout se consulte et se modifie ; un document
importé **attend** (statut « en attente ») et repart seul au retour du Mac ;
le tri des emails reprend à la relève suivante ; un ticket reste en
brouillon jusqu'à ce que le Mac le prenne.

Chaque travail de fond appartient à exactement une des deux instances
(test `TestRoleSettings_CloudAndWorkerShareNoBackgroundWork`) — sans quoi
il serait fait deux fois, ou défait : au démarrage, une instance marque
« interrompus » les travaux restés en cours, et ne doit le faire que pour
les siens.

## Ce qui change pour la confidentialité

- **L'inférence reste sur le Mac**, sans exception — la règle ne bouge pas.
  Les pages partent du serveur au Mac par le tunnel chiffré de Tailscale.
- **Le serveur voit les documents** : il les reçoit, les convertit
  (fichiers temporaires **en mémoire**, `tmpfs`, jamais sur son disque) et
  les lit depuis Atlas. C'est une nouvelle machine de confiance, chez un
  hébergeur — exception n° 9 de CLAUDE.md.
- **Sur le disque du serveur** (volume `jarvis-data`) : la clé
  `secret.key` et la configuration de la boîte mail (mot de passe chiffré
  avec cette clé). Dans `deploy/cloud/.env` : l'URI MongoDB et la clé
  Tailscale. Rien d'autre.
- **Aucun port n'est ouvert sur Internet** : seul ton réseau privé joint
  Jarvis. L'authentification Google n'est donc pas activée sur le serveur
  (elle exige de toute façon une redirection vers `127.0.0.1`) — c'est
  Tailscale qui décide qui entre. Qui est dans ton réseau privé voit tout.

## Mise en place

### 1. Tailscale (une fois)

1. Créer un compte sur tailscale.com ; installer l'application sur le Mac,
   le téléphone, le portable.
2. Console d'administration → **DNS** : activer *MagicDNS* et *HTTPS
   Certificates* (sans cela, pas d'adresse `https://jarvis.….ts.net`).
3. Console → **Settings → Keys** : générer une clé d'authentification
   (pour le serveur).

### 2. Le Mac : servir les modèles au réseau privé, garder les tickets

Les `llama-server` continuent d'écouter sur `127.0.0.1` ; c'est Tailscale
qui les expose, au réseau privé seulement :

```bash
tailscale serve --bg --tcp 8080 tcp://127.0.0.1:8080
```

```bash
tailscale serve --bg --tcp 8081 tcp://127.0.0.1:8081
```

```bash
tailscale ip -4
```

(La dernière commande donne l'adresse `100.x.y.z` du Mac, à reporter dans
`.env`. La syntaxe de `tailscale serve` a changé d'une version à l'autre :
en cas de refus, `tailscale serve --help`.)

Dans `~/.jarvis/launcher.json`, ajouter `"role": "worker"`, puis
reconstruire et relancer :

```bash
make build-app package-app
```

Le Mac doit rester éveillé pour servir (Réglages → Batterie → empêcher la
suspension d'activité sur secteur, ou `caffeinate -s`).

### 3. Le serveur

Un VPS Linux chez un hébergeur européen, 2 vCPU / 4 Go de RAM / 20 Go de
disque suffisent (aucun modèle n'y tourne ; LibreOffice et l'analyse du
code au démarrage sont les plus gourmands). Docker installé.

Dans MongoDB Atlas → **Network Access**, autoriser l'adresse IP publique
du serveur.

```bash
git clone https://github.com/JulianAndrieux/Jarvis.git
```

```bash
cd Jarvis/deploy/cloud && cp .env.example .env && chmod 600 .env
```

Remplir `.env` (`MONGO_URI`, `TS_AUTHKEY`, `MAC_TAILNET_IP`), puis :

```bash
docker compose up -d --build
```

```bash
docker compose logs -f jarvis
```

Le journal doit dire `rôle cloud`, puis `listening on`. Jarvis est alors à
`https://jarvis.<ton-tailnet>.ts.net` depuis n'importe quel appareil de
ton réseau privé (le premier accès attend quelques secondes le certificat).

### 4. Après le premier démarrage

- **Emails** : ressaisir le mot de passe d'application dans l'onglet
  Emails du serveur — il est chiffré avec une clé propre à chaque machine,
  celle du Mac ne le déchiffre pas ailleurs.
- **Architecture** (Admin) : le schéma d'infrastructure montre si les
  modèles du Mac répondent.

## Mettre à jour le serveur

```bash
cd Jarvis && git pull && cd deploy/cloud && docker compose up -d --build
```

Après le déploiement d'un ticket sur le Mac : pousser main vers GitHub
(bouton du ticket), puis la commande ci-dessus. Le serveur ne se redéploie
jamais seul.

## Limites connues

- **Redémarrage du serveur** : un document en attente ou en cours est
  marqué « interrompu » (comme après tout redémarrage) — à relancer.
- **Prompts des agents** (onglet Agents) : chaque instance garde le prompt
  en mémoire ; une modification faite sur l'une est vue par l'autre à son
  prochain redémarrage.
- **Tickets confiés au modèle local** : sur le Mac, ils basculent les
  modèles vers le profil « code » et arrêtent ceux des documents ; rien ne
  les recharge ensuite (le worker ne traite pas de documents). Garder les
  tickets sur Claude Code (le défaut), ou retirer `code_model_path` de
  `launcher.json`.
- **Valider un plan ou un diff** se fait sur le Mac ; avec le pilote
  automatique (le défaut), ce n'est nécessaire que lorsqu'une relecture
  refuse.
- **Dossier surveillé et copie locale des résultats** : non configurés
  dans le conteneur (pas de dossier partagé).
- **Vieux navigateurs** : les requêtes de modification sont acceptées sur
  l'en-tête `Sec-Fetch-Site` (tout navigateur récent ; Safari ≥ 16.4).

## Ce qui a été vérifié, et ce qui ne l'a pas été

Vérifié sous Linux, dans l'environnement de développement : le rôle
`cloud` contre un vrai MongoDB (pages, import pendant que les modèles sont
absents → en attente, reprise à leur retour, ticket créé en brouillon) ;
**l'image construite puis lancée** (utilisateur non root, `/tmp` en
mémoire) : démarrage, pages de l'application et de l'Admin, et les 22
fichiers du corpus `testdata/formats` importés et traités sans échec
(Word, Excel, PowerPoint, e-mail, images HEIC comprises), avec de faux
serveurs de modèles ; la syntaxe de `compose.yaml`.

**Pas vérifié** : le conteneur Tailscale et son HTTPS, l'exposition des
modèles par `tailscale serve`, les vrais modèles à travers le tunnel, et
le parcours réel avec le Mac et Atlas.
