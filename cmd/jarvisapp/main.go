// Command jarvisapp est l'application web unique de Jarvis : upload et
// suivi de documents (le pipeline triage -> parsing -> extraction) et
// navigateur de code/tests façon Smalltalk / Glamorous Toolkit, sur un
// seul port. Remplace les deux anciens binaires cmd/jarvisweb et
// cmd/codebrowser — voir CLAUDE.md, jalon 13.
//
// Les jobs (documents uploadés, statut, résultat) sont persistés dans
// MongoDB (Atlas en production) — voir CLAUDE.md pour la portée de
// l'exception explicite à "aucune donnée ne sort de la machine" que ça
// représente. L'inférence (VLM, LLM), elle, continue de tourner en local
// via les serveurs pointés par --vlm-url/--llm-url.
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/accounts"
	"github.com/JulianAndrieux/Jarvis/internal/agent"
	"github.com/JulianAndrieux/Jarvis/internal/agents"
	"github.com/JulianAndrieux/Jarvis/internal/bbox"
	"github.com/JulianAndrieux/Jarvis/internal/changes"
	"github.com/JulianAndrieux/Jarvis/internal/classify"
	"github.com/JulianAndrieux/Jarvis/internal/claudecode"
	"github.com/JulianAndrieux/Jarvis/internal/deploy"
	"github.com/JulianAndrieux/Jarvis/internal/doctype"
	"github.com/JulianAndrieux/Jarvis/internal/formats"
	"github.com/JulianAndrieux/Jarvis/internal/gate"
	"github.com/JulianAndrieux/Jarvis/internal/googleauth"
	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/mail"
	"github.com/JulianAndrieux/Jarvis/internal/models"
	"github.com/JulianAndrieux/Jarvis/internal/notes"
	"github.com/JulianAndrieux/Jarvis/internal/parsing"
	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
	"github.com/JulianAndrieux/Jarvis/internal/secretbox"
	"github.com/JulianAndrieux/Jarvis/internal/store"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
	"github.com/JulianAndrieux/Jarvis/internal/tickets"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
	"github.com/JulianAndrieux/Jarvis/internal/visual"
	"github.com/JulianAndrieux/Jarvis/internal/vlm"
	"github.com/JulianAndrieux/Jarvis/internal/watch"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
	"github.com/JulianAndrieux/Jarvis/internal/workspace"
)

//go:embed static
var staticFS embed.FS

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "Adresse d'écoute HTTP")
	vlmURL := flag.String("vlm-url", "", "URL de base du serveur VLM compatible OpenAI")
	vlmModel := flag.String("vlm-model", "", "Identifiant du modèle VLM servi")
	vlmVersion := flag.String("vlm-model-version", "", "Version/quantization du modèle VLM")
	llmURL := flag.String("llm-url", "", "URL de base du serveur LLM compatible OpenAI")
	llmModel := flag.String("llm-model", "", "Identifiant du modèle LLM servi")
	llmVersion := flag.String("llm-model-version", "", "Version/quantization du modèle LLM")
	dpi := flag.Int("dpi", 200, "Résolution de rendu des pages (DPI)")
	vlmTimeout := flag.Duration("vlm-timeout", 600*time.Second, "Timeout par appel VLM")
	llmTimeout := flag.Duration("llm-timeout", 180*time.Second, "Timeout par appel LLM")
	workDir := flag.String("work-dir", "", "Répertoire des fichiers temporaires de traitement (vide = répertoire temporaire du système)")
	outDir := flag.String("out-dir", "", "Répertoire de persistance locale additionnelle des résultats (JSON par page + log de rejeu) ; vide = pas de copie locale")
	mongoURIFlag := flag.String("mongo-uri", "", "URI de connexion MongoDB (ex: Atlas) — stocke les jobs (document source, statut, résultat) ; vide = variable d'environnement MONGO_URI (préférable : une option est visible de tous dans ps)")
	mongoDB := flag.String("mongo-db", "jarvis", "Base MongoDB")
	mongoCollection := flag.String("mongo-collection", "jobs", "Collection MongoDB pour les jobs")
	moduleDir := flag.String("module-dir", "", "Racine du module Go à analyser pour le navigateur de code (vide = répertoire courant)")
	watchDir := flag.String("watch-dir", "", "Dossier surveillé pour l'ingestion automatique de PDF ; vide = désactivée")
	watchInterval := flag.Duration("watch-interval", watch.DefaultInterval, "Intervalle de sondage de --watch-dir")
	watchEnv := flag.String("watch-env", string(tenancy.Local), "Environnement auquel appartiennent les fichiers déposés dans --watch-dir : un document ingéré doit avoir un propriétaire")
	vlmConcurrency := flag.Int("vlm-concurrency", 1, "Nombre de pages traitées en parallèle pour le VLM, par document ; 1 (défaut) = séquentiel. Le VLM (appels multimodaux) sature vite en parallèle, cf. CLAUDE.md — ne pas augmenter sans avoir revalidé sur le serveur cible")
	llmConcurrency := flag.Int("llm-concurrency", 1, "Nombre de pages traitées en parallèle pour l'extraction LLM, par document ; 1 (défaut) = séquentiel. Un contenu dense (page transcrite par le VLM) peut faire échouer le serveur llama.cpp (\"Context size has been exceeded\") au-delà de 1 en parallèle sur ce type de matériel, cf. CLAUDE.md — ne pas augmenter sans avoir revalidé sur le serveur cible")
	agentURL := flag.String("agent-url", "", "URL du serveur du modèle de l'agent des tickets (compatible OpenAI, appels d'outils) ; vide = --llm-url")
	agentModel := flag.String("agent-model", "", "Modèle de l'agent des tickets ; vide = --llm-model")
	agentToolOutput := flag.Int("agent-tool-output-chars", 3000, "Taille maximale (caractères) d'une sortie d'outil de l'agent — 3000 pour Qwen3-8B à 8k jetons ; plus pour un modèle à grand contexte")
	agentThinking := flag.Bool("agent-thinking", false, "Laisser l'agent des tickets réfléchir avant de répondre (sinon /no_think) — plus lent, meilleur sur les modèles qui en tirent parti")
	modelsFile := flag.String("models-file", "", "Profils de modèles (documents / code) que jarvisapp charge selon le travail — écrit par le lanceur (jalon 37) ; vide : les serveurs de modèles sont gérés ailleurs")
	visualReview := flag.Bool("visual-review", true, "Relecture visuelle des diffs qui touchent une page : captures avant/après jugées par le modèle de vision de l'agent (jalon 38)")
	chromePath := flag.String("chrome-path", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "Chrome, pour les captures de la relecture visuelle")
	agentReview := flag.Bool("agent-review", true, "Relecture du diff vérifié par un agent, selon les standards (onglet Agents), avant la revue humaine (jalon 36)")
	agentTemperature := flag.Float64("agent-temperature", 0, "Température de l'agent des tickets (0 : déterministe ; Qwen recommande 0,6 pour le code)")
	agentMaxTokens := flag.Int("agent-max-tokens", 2048, "Jetons générés au plus par réponse de l'agent des tickets (0 : pas de limite) — une réécriture qui s'emballe est coupée vite au lieu de saturer le contexte")
	agentContext := flag.Int("agent-context-chars", 16000, "Taille maximale (caractères) de la conversation envoyée à l'agent — à adapter au contexte du serveur (8192 jetons aujourd'hui)")
	ticketsCollection := flag.String("tickets-collection", "tickets", "Collection MongoDB des tickets")
	agentsCollection := flag.String("agents-collection", "agents", "Collection MongoDB des prompts des agents (onglet Agents)")
	notesCollection := flag.String("notes-collection", "notes", "Collection MongoDB des notes (onglet Notes)")
	tasksCollection := flag.String("tasks-collection", "tasks", "Collection MongoDB des tâches (onglet Tâches)")
	changesCollection := flag.String("changes-collection", "changes", "Collection MongoDB du journal des modifications (onglet Activité)")
	changesetCollection := flag.String("changeset-collection", "changeset", "Collection MongoDB des modifications en attente de commit (onglet Changements) ; vide = écritures directes, sans changeset")
	mailCollection := flag.String("mail-collection", "emails", "Collection MongoDB des emails relevés (onglet Emails)")
	mailFilesCollection := flag.String("mail-files-collection", "email_files", "Collection MongoDB du contenu des pièces jointes")
	mailConfig := flag.String("mail-config", defaultMailConfig(), "Configuration de la boîte mail (serveur, adresse, mot de passe d'application — fichier local 0600, jamais dans Atlas, rempli depuis l'onglet Emails) ; vide = pas de relève ni de tri")
	mailInterval := flag.Duration("mail-interval", 5*time.Minute, "Intervalle entre deux relèves de la boîte mail")
	mailDays := flag.Int("mail-days", 30, "Première relève : les emails des N derniers jours")
	agentTimeout := flag.Duration("agent-timeout", 15*time.Minute, "Délai d'un appel au modèle de l'agent — un tour qui écrit un fichier entier peut prendre plusieurs minutes sur un modèle local lent")
	claudeBin := flag.String("claude-bin", "", "Claude Code (CLI claude) pour les tickets confiés à Claude (jalon 41) ; vide = cherché sur le PATH ; introuvable = tickets au modèle local seulement")
	claudeModel := flag.String("claude-model", "", "Modèle de Claude Code pour les tickets ; vide = celui par défaut du CLI")
	claudeTimeout := flag.Duration("claude-timeout", time.Hour, "Durée maximale d'une session de Claude Code (analyse, développement ou relecture)")
	ticketAgent := flag.String("ticket-default-agent", "claude", "Agent proposé par défaut à la création d'un ticket : claude ou local (local si Claude Code est introuvable)")
	ticketAutopilot := flag.Bool("ticket-autopilot", true, "Pilote automatique des tickets : plan validé sans l'utilisateur, diff déployé si la vérification et la relecture réussissent (sinon il attend la validation)")
	ticketPickup := flag.Duration("ticket-pickup", 5*time.Minute, "Intervalle de la relève des tickets en brouillon (analyse lancée d'elle-même, un ticket à la fois) ; 0 = désactivée")
	agentDev := flag.Bool("agent-dev", true, "Développement automatique des tickets au plan validé (copie de travail git isolée, vérification complète, diff à relire)")
	worktreesDir := flag.String("worktrees-dir", "", "Dossier des copies de travail des tickets ; vide = ~/.jarvis/worktrees")
	deployOn := flag.Bool("deploy", true, "Déploiement des tickets au diff accepté : fusion vérifiée dans main, essai à blanc, redémarrage sur la nouvelle version (nécessite --agent-dev)")
	authConfig := flag.String("auth-config", defaultAuthConfig(), "Fournisseur d'identité (client OAuth Google + propriétaire de l'instance) — fichier local 0600, jamais une option de ligne de commande (un secret en argument est lisible dans ps) ; fichier absent = authentification désactivée, comportement mono-utilisateur d'avant le jalon 45")
	accountsPrefix := flag.String("accounts-prefix", "accounts_", "Préfixe des collections MongoDB des comptes (users, envs, memberships, sessions)")
	deployMarker := flag.String("deploy-marker", "", "Marqueur du déploiement en attente de confirmation ; vide = ~/.jarvis/deploy.json (le lanceur lit le même)")
	flag.Parse()

	if *vlmURL == "" || *vlmModel == "" {
		log.Fatal("jarvisapp: --vlm-url et --vlm-model sont requis")
	}
	if *llmURL == "" || *llmModel == "" {
		log.Fatal("jarvisapp: --llm-url et --llm-model sont requis")
	}
	mongoConn := mongoURI(*mongoURIFlag, os.Getenv)
	if mongoConn == "" {
		log.Fatal("jarvisapp: MONGO_URI (ou --mongo-uri) est requis (les jobs sont persistés dans MongoDB)")
	}

	if *agentURL == "" {
		*agentURL = *llmURL
	}
	if *agentModel == "" {
		*agentModel = *llmModel
	}

	// Onglet Agents : le prompt en vigueur de chaque agent, modifiable
	// depuis l'interface et relu à chaque appel.
	agentsCtx, cancelAgents := context.WithTimeout(context.Background(), 10*time.Second)
	agentStore, err := agents.NewMongoStore(agentsCtx, mongoConn, *mongoDB, *agentsCollection)
	if err != nil {
		log.Fatalf("jarvisapp: agents : %v", err)
	}
	agentRegistry, err := agents.NewRegistry(agentsCtx, agentStore, agents.Defaults(agents.Models{
		Documents: strings.TrimSpace(*llmModel + " " + *llmVersion),
		Tickets:   *agentModel,
	}))
	cancelAgents()
	if err != nil {
		log.Fatalf("jarvisapp: agents : %v", err)
	}

	// Notes et tâches (jalon 32), dans Atlas comme les documents
	// (décision de l'utilisateur).
	notesCtx, cancelNotes := context.WithTimeout(context.Background(), 10*time.Second)
	notesStore, err := notes.NewMongoStore(notesCtx, mongoConn, *mongoDB, *notesCollection, *tasksCollection)
	cancelNotes()
	if err != nil {
		log.Fatalf("jarvisapp: notes : %v", err)
	}
	// Emails (jalon 39), copiés dans Atlas (décision de l'utilisateur).
	mailCtx, cancelMail := context.WithTimeout(context.Background(), 10*time.Second)
	mailStore, err := mail.NewMongoStore(mailCtx, mongoConn, *mongoDB, *mailCollection, *mailFilesCollection)
	cancelMail()
	if err != nil {
		log.Fatalf("jarvisapp: emails : %v", err)
	}

	// Journal des modifications faites par les humains (jalon 46) :
	// append-only, cloisonné par environnement. Un échec d'écriture ne
	// défait jamais la modification qu'il décrit — il est seulement
	// journalisé.
	journalCtx, cancelJournal := context.WithTimeout(context.Background(), 10*time.Second)
	journal, err := changes.NewMongoJournal(journalCtx, mongoConn, *mongoDB, *changesCollection)
	cancelJournal()
	if err != nil {
		log.Fatalf("jarvisapp: journal des modifications : %v", err)
	}
	recorder := &changes.Recorder{Journal: journal, Logf: log.Printf}

	// Changeset (jalon 48) : les modifications d'un humain attendent son
	// commit, invisibles des autres. Le travail de fond, lui, écrit
	// toujours directement — il n'a pas de session.
	var changeset changes.ChangesetStore
	var stager *changes.Stager
	if *changesetCollection != "" {
		csCtx, cancelCS := context.WithTimeout(context.Background(), 10*time.Second)
		cs, err := changes.NewMongoChangesetStore(csCtx, mongoConn, *mongoDB, *changesetCollection)
		cancelCS()
		if err != nil {
			log.Fatalf("jarvisapp: changeset : %v", err)
		}
		changeset, stager = cs, &changes.Stager{Store: cs}
		log.Printf("jarvisapp: changeset activé (les modifications attendent un commit)")
	} else {
		log.Printf("jarvisapp: changeset désactivé (écritures directes)")
	}

	registry := doctype.NewDefaultRegistry()

	llmClient := llm.HTTPClient{
		BaseURL:      *llmURL,
		Model:        *llmModel,
		ModelVersion: *llmVersion,
		HTTP:         &http.Client{Timeout: *llmTimeout},
		// Voir cmd/jarvis/process.go et CLAUDE.md (jalon 10 finding 4
		// / jalon 11) : Qwen3 peut sinon générer un nombre de tokens
		// très variable sur du contenu ambigu.
		DisableThinking: true,
	}

	runner := pipeline.Pipeline{
		TextExtractor: triage.PdftotextExtractor{},
		Renderer:      parsing.PdftoppmRenderer{},
		BBox:          bbox.PdftotextBBoxExtractor{},
		VLM: vlm.HTTPClient{
			BaseURL:      *vlmURL,
			Model:        *vlmModel,
			ModelVersion: *vlmVersion,
			HTTP:         &http.Client{Timeout: *vlmTimeout},
		},
		DPI: *dpi,
		LLM: llmClient,
		// Classifier réutilise le même LLM d'extraction (aucun nouveau
		// modèle choisi) pour déterminer automatiquement le type de
		// document à l'upload — voir CLAUDE.md, "Upload : classification
		// automatique".
		Classifier:       classify.LLMClassifier{Client: llmClient, Prompt: agentRegistry.PromptFunc(agents.Classification)},
		ExtractionPrompt: agentRegistry.PromptFunc(agents.Extraction),
		Registry:         registry,
		// Jalon 21 puis correction (voir CLAUDE.md) : VLM et extraction LLM
		// ont chacun leur propre borne de parallélisme — le VLM (appels
		// multimodaux) sature vite en parallèle sur ce matériel, l'extraction
		// LLM (texte) reste sûre et bénéfique en parallèle.
		VLMConcurrency: *vlmConcurrency,
		LLMConcurrency: *llmConcurrency,
	}

	connectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	jobStore, err := webapp.NewMongoStore(connectCtx, mongoConn, *mongoDB, *mongoCollection)
	if err != nil {
		log.Fatalf("jarvisapp: %v", err)
	}

	// Ticket "Ajouter commentaire sur document" : chaque document existant
	// reçoit un commentaire vide. Idempotent, donc sans risque à chaque
	// démarrage ; un échec n'empêche pas l'application de servir.
	if n, err := jobStore.MigrateComments(context.Background()); err != nil {
		log.Printf("jarvisapp: migration des commentaires : %v", err)
	} else if n > 0 {
		log.Printf("jarvisapp: %d document(s) ont reçu un commentaire vide", n)
	}

	// Cloisonnement par environnement : les documents, notes, tâches et
	// emails d'avant reçoivent l'environnement de cette installation.
	// Idempotent — après la transition, ne trouve plus rien. Puis on
	// compte ce qui reste sans environnement : toutes les requêtes
	// filtrant sur env_id, un enregistrement non estampillé ne
	// remonterait dans aucune liste, et il vaut mieux le dire que le
	// laisser disparaître en silence.
	migrateTenancy(context.Background(), tenancy.Local, map[string]envMigrator{
		"documents":    jobStore,
		"notes/tâches": notesStore,
		"emails":       mailStore,
	})

	jobs := webapp.NewJobManager(jobStore, runner)
	jobs.Changes = recorder
	jobs.Staged, jobs.Stager = changeset, stager
	jobs.Renderer = parsing.PdftoppmRenderer{}
	// Jalon 25 : conversion locale des fichiers non-PDF (LibreOffice,
	// sips). Sans LibreOffice, ces fichiers échouent avec un message
	// explicite — signalé dès le démarrage.
	jobs.Converter = &formats.LocalConverter{}
	if _, err := exec.LookPath("soffice"); err != nil {
		log.Printf("jarvisapp: ATTENTION — soffice (LibreOffice) introuvable : Word, Excel, PowerPoint, CSV et texte ne pourront pas être convertis (brew install --cask libreoffice)")
	}
	jobs.WorkDir = *workDir
	if *outDir != "" {
		jobs.OnFinish = persistJobLocally(*outDir)
	}

	// Tout job encore "pending"/"running" ici appartenait forcément à un
	// process précédent (ce process vient de démarrer) — il ne peut par
	// construction jamais aboutir, cf. CLAUDE.md jalon 20 (incident réel
	// : un job restait bloqué "running" pour toujours après un
	// redémarrage pendant son traitement).
	if n, err := jobs.RecoverOrphaned(context.Background()); err != nil {
		log.Printf("jarvisapp: récupération des jobs orphelins : %v", err)
	} else if n > 0 {
		log.Printf("jarvisapp: %d job(s) laissé(s) en cours par un précédent démarrage, marqué(s) en échec", n)
	}

	if *watchDir != "" {
		w := &watch.Watcher{
			Dir:      *watchDir,
			Interval: *watchInterval,
			OnFile: func(ctx context.Context, filename string, content []byte) error {
				// Portée explicite : un fichier déposé dans un dossier n'a
				// pas de session, mais il a un environnement — sans lui, il
				// n'appartiendrait à personne.
				ctx = tenancy.WithScope(ctx, tenancy.System(tenancy.EnvID(*watchEnv)))
				_, err := jobs.Submit(ctx, filename, content)
				return err
			},
			Logf: log.Printf,
		}
		go func() {
			if err := w.Run(context.Background()); err != nil {
				log.Printf("jarvisapp: watcher arrêté: %v", err)
			}
		}()
		log.Printf("jarvisapp: ingestion automatique depuis %s (toutes les %s)", *watchDir, *watchInterval)
	}

	dir := *moduleDir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			log.Fatalf("jarvisapp: répertoire courant : %v", err)
		}
		dir = wd
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		log.Fatalf("jarvisapp: chemin absolu de %s : %v", dir, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		log.Fatalf("jarvisapp: pas de go.mod dans %s (--module-dir doit pointer sur la racine du module) : %v", dir, err)
	}

	// Tickets et agent d'analyse (jalons 26-27). L'agent partage la file
	// des documents : les modèles locaux ne sont jamais sollicités par
	// les deux en même temps (cf. jalon 21 bis).
	modelGate := gate.New(1)
	if *modelsFile != "" {
		mc, err := models.LoadConfig(*modelsFile)
		if err != nil {
			log.Fatalf("jarvisapp: %v", err)
		}
		switcher := &models.Switcher{Config: mc, Runner: models.ProcessRunner{Binary: mc.Binary, LogDir: mc.LogDir}, Log: log.Printf}
		modelGate.Switch = switcher.Use
		// Au démarrage, les modèles de documents (l'usage courant), à son
		// tour dans la file : jamais pendant un ticket.
		go func() {
			release, err := modelGate.AcquireFair(context.Background(), gate.Documents, tenancy.Local)
			if err != nil {
				log.Printf("jarvisapp: modèles de documents : %v", err)
				return
			}
			release()
			log.Printf("jarvisapp: modèles de documents prêts")
		}()
		log.Printf("jarvisapp: bascule des modèles activée (%s)", *modelsFile)
	}
	jobs.Gate = modelGate
	// Emails : relève périodique, tri par le LLM des documents dans la
	// file des modèles (profil documents).
	secretKey, err := secretbox.LoadOrCreateKey(defaultSecretKey())
	if err != nil {
		log.Fatalf("jarvisapp: clé de chiffrement locale : %v", err)
	}
	mailService := &mail.Service{
		Key:     secretKey,
		Store:   mailStore,
		Syncer:  &mail.Syncer{Store: mailStore, Connect: mail.ConnectIMAP, Days: *mailDays},
		Triager: &mail.Triager{LLM: llmClient, Model: *llmModel, Prompt: agentRegistry.PromptFunc(agents.MailTriage)},
		Acquire: func(ctx context.Context) (func(), error) {
			// Le tri des emails appartient à un environnement, comme les
			// documents : même file équitable.
			scope, _ := tenancy.FromContext(ctx)
			env := scope.Env
			if env == "" {
				env = tenancy.Local
			}
			return modelGate.AcquireFair(ctx, gate.Documents, env)
		},
		ConfigPath: *mailConfig,
		Interval:   *mailInterval,
		Log:        log.Printf,
	}
	if *mailConfig != "" {
		go mailService.Run(context.Background())
		log.Printf("jarvisapp: emails : relève toutes les %s (configuration %s)", *mailInterval, *mailConfig)
	} else {
		log.Printf("jarvisapp: emails : relève désactivée (--mail-config vide)")
	}
	ticketsCtx, cancelTickets := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelTickets()
	ticketStore, err := tickets.NewMongoStore(ticketsCtx, mongoConn, *mongoDB, *ticketsCollection)
	if err != nil {
		log.Fatalf("jarvisapp: tickets : %v", err)
	}
	claudeMD, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	// Contexte donné aux agents des tickets : le début de CLAUDE.md et la
	// carte des paquets (où vit quoi). Le prompt système entier doit
	// laisser de la place à la conversation (--agent-context-chars).
	brief := agent.ProjectBrief(string(claudeMD), 2500)
	codeMap := agent.PackageMap(dir, 3000)
	ticketManager := &tickets.Manager{
		Store: ticketStore,
		Gate:  modelGate,
		Analyst: &agent.Analyzer{
			Model:           agent.HTTPModel{BaseURL: *agentURL, Model: *agentModel, HTTP: &http.Client{Timeout: *agentTimeout}, MaxTokens: *agentMaxTokens, Temperature: *agentTemperature},
			Tools:           agent.Tools{Root: dir},
			ProjectBrief:    brief,
			CodeMap:         codeMap,
			ContextChars:    *agentContext,
			ToolOutputChars: *agentToolOutput,
			DisableThinking: !*agentThinking,
			Instructions:    agentRegistry.PromptFunc(agents.Analysis),
		},
	}
	// Développement (jalon 28) : copie de travail git isolée par ticket,
	// vérification finale refaite par Jarvis.
	var wtRoot string
	var git workspace.Manager // copies de travail et opérations git des tickets
	if *agentDev {
		wtRoot = *worktreesDir
		if wtRoot == "" {
			home, _ := os.UserHomeDir()
			wtRoot = filepath.Join(home, ".jarvis", "worktrees")
		}
		checker := workspace.Checker{Timeout: 10 * time.Minute}
		git = workspace.Manager{Repo: dir, Root: wtRoot}
		ticketManager.Workspace = git
		ticketManager.Verifier = checker
		ticketManager.Developer = &agent.Developer{
			Model:           agent.HTTPModel{BaseURL: *agentURL, Model: *agentModel, HTTP: &http.Client{Timeout: *agentTimeout}, MaxTokens: *agentMaxTokens, Temperature: *agentTemperature},
			Checker:         checker,
			ProjectBrief:    brief,
			CodeMap:         codeMap,
			ContextChars:    *agentContext,
			ToolOutputChars: *agentToolOutput,
			DisableThinking: !*agentThinking,
			Instructions:    agentRegistry.PromptFunc(agents.Development),
			Temperature:     *agentTemperature,
		}
		if *agentReview {
			// Même modèle, température 0 : un verdict stable.
			codeReviewer := &agent.Reviewer{
				Model:           agent.HTTPModel{BaseURL: *agentURL, Model: *agentModel, HTTP: &http.Client{Timeout: *agentTimeout}, MaxTokens: *agentMaxTokens},
				ProjectBrief:    brief,
				CodeMap:         codeMap,
				ContextChars:    *agentContext,
				ToolOutputChars: *agentToolOutput,
				DisableThinking: !*agentThinking,
				Instructions:    agentRegistry.PromptFunc(agents.Review),
			}
			reviewers := tickets.MultiReviewer{codeReviewer}
			if _, err := os.Stat(*chromePath); *visualReview && err == nil {
				self, _ := os.Executable()
				reviewers = append(reviewers, &visual.Reviewer{
					Build:        deploy.GoBuild,
					Launch:       launchIsolated(*mongoCollection, *ticketsCollection),
					BeforeBinary: self,
					Capturer:     visual.Chrome{Path: *chromePath},
					Judge:        visual.VisionJudge{BaseURL: *agentURL, Model: *agentModel, HTTP: &http.Client{Timeout: *agentTimeout}},
				})
				log.Printf("jarvisapp: relecture visuelle activée (captures avant/après)")
			} else if *visualReview {
				log.Printf("jarvisapp: relecture visuelle désactivée : Chrome introuvable (%s)", *chromePath)
			}
			ticketManager.Reviewer = reviewers
			log.Printf("jarvisapp: relecture des diffs activée")
		}
		log.Printf("jarvisapp: développement des tickets activé (copies de travail : %s)", wtRoot)
	}
	// Déploiement (jalon 30) : au diff accepté, fusion vérifiée dans main,
	// essai à blanc, puis remplacement du processus par la nouvelle version.
	home, _ := os.UserHomeDir()
	if *deployMarker == "" {
		*deployMarker = filepath.Join(home, ".jarvis", "deploy.json")
	}
	if *agentDev && *deployOn {
		binary, err := os.Executable()
		if err == nil {
			binary, err = filepath.EvalSymlinks(binary)
		}
		if err != nil {
			log.Fatalf("jarvisapp: chemin du binaire en service : %v", err)
		}
		ticketManager.Deployer = deploy.Deployer{
			Git:        git,
			Verifier:   ticketManager.Verifier,
			Binary:     binary,
			MarkerPath: *deployMarker,
			Smoke:      smokeTest(*mongoCollection, *ticketsCollection),
		}
		ticketManager.Busy = busyReason(jobs, ticketManager)
		ticketManager.Pusher = git // push vers GitHub, bouton du ticket déployé
		log.Printf("jarvisapp: déploiement des tickets activé (binaire %s)", binary)
	}
	log.Printf("jarvisapp: agent des tickets -> %s (%s)", *agentURL, *agentModel)
	// Claude Code (jalon 41) : mêmes étapes, même vérification par Jarvis,
	// mais le travail d'agent part à Claude plutôt qu'au modèle local.
	if bin, err := findClaude(*claudeBin); err == nil {
		cli := claudecode.CLI{Binary: bin, Model: *claudeModel, Timeout: *claudeTimeout}
		snapRoot := *worktreesDir
		if snapRoot == "" {
			home, _ := os.UserHomeDir()
			snapRoot = filepath.Join(home, ".jarvis", "worktrees")
		}
		snapshots := workspace.Manager{Repo: dir, Root: snapRoot}
		set := &tickets.AgentSet{Analyst: claudecode.Analyst{Runner: cli, Snapshot: snapshots.Snapshot}}
		if ticketManager.Developer != nil {
			set.Developer = claudecode.Developer{Runner: cli}
			if ticketManager.Reviewer != nil {
				set.Reviewer = claudecode.Reviewer{Runner: cli}
			}
		}
		ticketManager.Claude = set
		ticketManager.DefaultAgent = tickets.AgentKind(*ticketAgent)
		log.Printf("jarvisapp: Claude Code pour les tickets -> %s", bin)
	} else {
		log.Printf("jarvisapp: Claude Code introuvable (%v) : tickets au modèle local seulement", err)
	}
	ticketManager.Autopilot = *ticketAutopilot
	if *ticketAutopilot {
		log.Printf("jarvisapp: pilote automatique des tickets activé (déploiement si vérification et relecture réussies)")
	}

	srv := &Server{Jobs: jobs, Registry: registry, ModuleDir: dir, Tickets: ticketManager, Agents: agentRegistry, Notes: &notes.Service{Store: notesStore, Changes: recorder, Staged: changeset, Stager: stager}, Mail: mailService, Journal: journal}
	if changeset != nil {
		// Un seul commit pour tous les genres d'entités : chaque paquet
		// fournit l'Applier de ce qu'il possède.
		srv.Changeset = changeset
		srv.Committer = &changes.Committer{Staged: changeset, Appliers: []changes.Applier{
			notes.Applier{Base: notesStore},
			webapp.Applier{Base: jobStore},
		}}
	}

	// Authentification (jalon 45). Sans fichier de configuration, rien ne
	// change : un seul environnement, aucun écran de connexion — c'est ce
	// qui permet de continuer à lancer l'application depuis le Dock sans
	// rien préparer.
	authCfg, hasAuth, err := accounts.LoadConfig(*authConfig)
	if err != nil {
		log.Fatalf("jarvisapp: %v", err)
	}
	if hasAuth {
		if loose, err := accounts.InsecurePermissions(*authConfig); err == nil && loose {
			log.Printf("jarvisapp: ATTENTION — %s est lisible par d'autres utilisateurs : chmod 600 (il contient le secret du client OAuth)", *authConfig)
		}
		accCtx, cancelAcc := context.WithTimeout(context.Background(), 10*time.Second)
		accStore, err := accounts.NewMongoStore(accCtx, mongoConn, *mongoDB, *accountsPrefix)
		cancelAcc()
		if err != nil {
			log.Fatalf("jarvisapp: comptes : %v", err)
		}
		mgr := &accounts.Manager{Store: accStore, OwnerEmail: authCfg.OwnerEmail}
		if err := mgr.Bootstrap(context.Background()); err != nil {
			log.Fatalf("jarvisapp: comptes : %v", err)
		}
		if n, err := mgr.PurgeExpired(context.Background()); err != nil {
			log.Printf("jarvisapp: purge des sessions expirées : %v", err)
		} else if n > 0 {
			log.Printf("jarvisapp: %d session(s) expirée(s) purgée(s)", n)
		}
		srv.Accounts = mgr
		if authCfg.ClientID != "" {
			srv.OAuth = googleauth.Google(authCfg.ClientID, authCfg.ClientSecret, "http://"+loopbackAddr(*addr)+"/auth/callback")
			log.Printf("jarvisapp: authentification Google active (%s)", authCfg)
		} else {
			log.Printf("jarvisapp: authentification active sans Google (%s) : seul le lien de secours du propriétaire ouvre une session", authCfg)
		}
		// Le lien de secours : se connecter par Google demande Internet, et
		// une panne du fournisseur ne doit pas interdire d'ouvrir une
		// application par ailleurs locale. Écrit ici seulement — donc
		// lisible par qui a accès à la machine.
		token, err := accounts.RandomToken()
		if err != nil {
			log.Fatalf("jarvisapp: jeton de secours : %v", err)
		}
		srv.LocalLogin = NewLocalLogin(token, time.Now(), localLoginTTL)
		log.Printf("jarvisapp: lien de secours du propriétaire (un seul usage, %s) : %s", localLoginTTL, localLoginURL(*addr, token))
	} else {
		log.Printf("jarvisapp: authentification désactivée (%s absent) : un seul environnement, aucun écran de connexion", *authConfig)
	}
	if *agentDev {
		srv.Unpushed = git.Unpushed
	}
	srv.Infra = srv.buildInfra(InfraConfig{
		Addr:   *addr,
		VLMURL: *vlmURL, VLMModel: *vlmModel,
		LLMURL: *llmURL, LLMModel: *llmModel,
		AgentURL: *agentURL, AgentModel: *agentModel,
		MongoDB: *mongoDB, JobsCollection: *mongoCollection, TicketsCol: *ticketsCollection,
		WatchDir: *watchDir, OutDir: *outDir, WorktreesDir: wtRoot,
		AgentDev: *agentDev,
	})
	log.Printf("jarvisapp: analyse de %s...", dir)
	if err := srv.Refresh(); err != nil {
		log.Fatalf("jarvisapp: %v", err)
	}

	staticContent, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("jarvisapp: static assets: %v", err)
	}

	mux := srv.Routes()
	mux.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticContent))))

	log.Printf("jarvisapp: listening on http://%s (doc types: %v)", *addr, registry.Names())
	log.Printf("jarvisapp: jobs -> mongodb %s/%s", *mongoDB, *mongoCollection)
	if *outDir != "" {
		log.Printf("jarvisapp: copie locale des résultats -> %s", *outDir)
	}
	// Écouter d'abord : un déploiement en attente ne se confirme qu'une fois
	// la nouvelle version capable de répondre. Puis seulement, les tickets
	// restés en cours (un déploiement réglé ici n'est plus orphelin).
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("jarvisapp: %v", err)
	}
	if msg, err := settleDeployment(context.Background(), *deployMarker, ticketManager, git); err != nil {
		log.Printf("jarvisapp: déploiement en attente : %v", err)
	} else if msg != "" {
		log.Printf("jarvisapp: %s", msg)
	}
	if n, err := ticketManager.RecoverOrphaned(context.Background()); err != nil {
		log.Printf("jarvisapp: tickets orphelins : %v", err)
	} else if n > 0 {
		log.Printf("jarvisapp: %d ticket(s) interrompu(s) par le redémarrage", n)
	}
	if *ticketPickup > 0 {
		go ticketManager.RunPickUp(context.Background(), *ticketPickup)
		log.Printf("jarvisapp: relève des tickets toutes les %s", *ticketPickup)
	}
	if err := http.Serve(ln, mux); err != nil {
		log.Fatalf("jarvisapp: %v", err)
	}
}

// persistJobLocally branche une copie locale additionnelle (internal/store,
// la même que `jarvis process --out-dir`) sur la fin de traitement d'un
// job web — utile pour inspecter/déboguer sans requêter MongoDB, ou comme
// filet de secours local. MongoDB (jobStore) reste la source de vérité.
func persistJobLocally(outDir string) func(webapp.Job) {
	return func(job webapp.Job) {
		if job.Status != webapp.StatusDone || job.Result == nil {
			return
		}
		// Un dossier par environnement : deux environnements peuvent porter
		// un document de même empreinte sans écrire l'un sur l'autre
		// (jalon 50).
		if job.Env != "" {
			outDir = filepath.Join(outDir, string(job.Env))
		}
		hash := job.SourceHash
		doc, pages := store.BuildRecords(hash, job.Filename, job.DocType, time.Now().UTC(), *job.Result)
		if err := store.WriteRecords(outDir, doc, pages); err != nil {
			fmt.Fprintf(os.Stderr, "jarvisapp: write records for %s: %v\n", job.Filename, err)
			return
		}
		entry := store.RunLogEntry{
			Timestamp:  doc.ProcessedAt,
			SourceHash: hash,
			SourcePath: job.Filename,
			DocType:    job.DocType,
			PagesTotal: len(pages),
		}
		for _, p := range pages {
			if p.Extraction != nil && p.Extraction.NeedsReview {
				entry.PagesNeedingReview++
			}
			if (p.Parsing != nil && p.Parsing.Failed) || (p.Extraction != nil && p.Extraction.Failed) {
				entry.PagesFailed++
			}
		}
		if err := store.AppendRunLog(outDir, hash, entry); err != nil {
			fmt.Fprintf(os.Stderr, "jarvisapp: append run log for %s: %v\n", job.Filename, err)
		}
	}
}

// defaultMailConfig : ~/.jarvis/mail.json ("" si le dossier personnel est
// inconnu : relève désactivée).
func defaultMailConfig() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".jarvis", "mail.json")
}

// mongoURI : l'option --mongo-uri si elle est donnée, sinon MONGO_URI.
// Le lanceur passe l'URI par l'environnement : en argument, son mot de
// passe serait lisible par tout utilisateur de la machine (ps).
func mongoURI(flagValue string, getenv func(string) string) string {
	if flagValue != "" {
		return flagValue
	}
	return getenv("MONGO_URI")
}

// findClaude : le CLI claude demandé, sinon sur le PATH, sinon là où
// son installateur le met (~/.local/bin) — lancé depuis le Dock, le PATH
// est minimal.
func findClaude(flagValue string) (string, error) {
	if flagValue != "" {
		return exec.LookPath(flagValue)
	}
	if bin, err := exec.LookPath("claude"); err == nil {
		return bin, nil
	}
	home, _ := os.UserHomeDir()
	return exec.LookPath(filepath.Join(home, ".local", "bin", "claude"))
}

// envMigrator : un store qui sait estampiller ses enregistrements d'avant
// le cloisonnement, et compter ceux qui ne le sont pas.
type envMigrator interface {
	MigrateToEnv(ctx context.Context, env tenancy.EnvID) (int64, error)
	CountUnstamped(ctx context.Context) (int64, error)
}

// migrateTenancy estampille puis vérifie. Un échec n'empêche pas
// l'application de servir (comme la migration des commentaires), mais il
// est dit clairement : sans environnement, les enregistrements concernés
// sont invisibles.
func migrateTenancy(ctx context.Context, env tenancy.EnvID, stores map[string]envMigrator) {
	for name, st := range stores {
		n, err := st.MigrateToEnv(ctx, env)
		if err != nil {
			log.Printf("jarvisapp: ATTENTION — %s : estampillage de l'environnement impossible : %v", name, err)
			continue
		}
		if n > 0 {
			log.Printf("jarvisapp: %s : %d enregistrement(s) rattaché(s) à l'environnement %q", name, n, env)
		}
		left, err := st.CountUnstamped(ctx)
		if err != nil {
			log.Printf("jarvisapp: %s : comptage des enregistrements sans environnement impossible : %v", name, err)
			continue
		}
		if left > 0 {
			log.Printf("jarvisapp: ATTENTION — %s : %d enregistrement(s) sans environnement, donc invisibles dans l'application", name, left)
		}
	}
}

// localLoginTTL : durée de validité du lien de secours écrit au démarrage.
// Assez pour ouvrir le journal et cliquer, pas assez pour qu'il traîne.
const localLoginTTL = 15 * time.Minute

// defaultAuthConfig : ~/.jarvis/oauth.json ("" si le dossier personnel est
// inconnu : authentification désactivée).
func defaultAuthConfig() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".jarvis", "oauth.json")
}

// loopbackAddr : l'adresse d'écoute sous une forme utilisable dans une URI
// de redirection. Google n'accepte http:// que sur l'adresse de boucle
// locale, et refuse "localhost" pour les clients récents : on écrit donc
// toujours 127.0.0.1.
func loopbackAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "localhost" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// defaultSecretKey : ~/.jarvis/secret.key, la clé qui chiffre les
// identifiants de services tiers (mot de passe de la boîte mail). Elle ne
// quitte jamais la machine : c'est ce qui rend vérifiable « aucun mot de
// passe enregistré en clair hors de l'hôte ». Dossier personnel inconnu :
// la clé est posée à côté du binaire, faute de mieux.
func defaultSecretKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "secret.key"
	}
	return filepath.Join(home, ".jarvis", "secret.key")
}
