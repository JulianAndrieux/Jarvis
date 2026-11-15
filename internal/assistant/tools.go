package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/mail"
	"github.com/JulianAndrieux/Jarvis/internal/mcp"
	"github.com/JulianAndrieux/Jarvis/internal/notes"
	"github.com/JulianAndrieux/Jarvis/internal/webapp"
)

// Toolbox : ce que l'assistant peut lire et faire dans Jarvis. Un service
// nil retire ses outils.
//
// Ce qui n'y est pas, délibérément : supprimer quoi que ce soit, créer un
// ticket (un ticket fait écrire du code et le déploie — un email piégé lu
// par l'assistant ne doit jamais pouvoir en créer un), envoyer un email,
// payer, et tout accès au réseau. Chaque action offerte se défait depuis
// l'interface.
type Toolbox struct {
	Notes *notes.Service
	Mail  *mail.Service
	Jobs  *webapp.JobManager
	// Now : horloge (nil : time.Now).
	Now func() time.Time
}

const (
	// maxResults : éléments rendus par une recherche, au plus.
	maxResults = 20
	// maxText : caractères de texte rendus par une lecture (document,
	// email, note), au plus — la suite se demande avec offset.
	maxText = 12000
)

func (t *Toolbox) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// outcome : ce qu'un outil a fait, pour le fil de la conversation.
type outcome struct {
	summary string
	link    string
}

type toolDef struct {
	name, desc string
	props      map[string]any
	required   []string
	action     bool
	run        func(ctx context.Context, raw json.RawMessage) (any, outcome, error)
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

// Tools rend les outils, chacun signalant ce qu'il a fait à record (appelé
// après chaque appel, échecs compris).
func (t *Toolbox) Tools(record func(Step)) []mcp.Tool {
	var defs []toolDef
	if t.Jobs != nil {
		defs = append(defs, t.documentTools()...)
	}
	if t.Mail != nil {
		defs = append(defs, t.mailTools()...)
	}
	if t.Notes != nil {
		defs = append(defs, t.taskTools()...)
		defs = append(defs, t.noteTools()...)
	}
	out := make([]mcp.Tool, 0, len(defs))
	for _, d := range defs {
		schema := map[string]any{"type": "object", "properties": d.props}
		if d.props == nil {
			schema["properties"] = map[string]any{}
		}
		if len(d.required) > 0 {
			schema["required"] = d.required
		}
		out = append(out, mcp.Tool{Name: d.name, Description: d.desc, Schema: schema, Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
			step := Step{Tool: d.name, Action: d.action, At: t.now()}
			res, oc, err := d.run(ctx, raw)
			if err != nil {
				step.Summary, step.Failed, step.Error = d.name, true, err.Error()
				record(step)
				return "", err
			}
			step.Summary, step.Link = oc.summary, oc.link
			record(step)
			text, merr := json.MarshalIndent(res, "", " ")
			if merr != nil {
				return "", fmt.Errorf("résultat illisible : %w", merr)
			}
			return string(text), nil
		}})
	}
	return out
}

// ToolNames : les noms de tous les outils (pour décrire l'assistant dans
// l'onglet Agents).
func ToolNames() []string {
	all := &Toolbox{Notes: &notes.Service{}, Mail: &mail.Service{}, Jobs: &webapp.JobManager{}}
	var out []string
	for _, tool := range all.Tools(func(Step) {}) {
		out = append(out, tool.Name)
	}
	return out
}

// args décode les arguments d'un outil ; un argument inconnu est refusé
// (le modèle apprend tout de suite qu'il s'est trompé de nom).
func args(raw json.RawMessage, into any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("arguments invalides : %w", err)
	}
	return nil
}

func limitOf(n int) int {
	if n <= 0 || n > maxResults {
		return maxResults
	}
	return n
}

// window rend la tranche de texte demandée et dit s'il en reste.
func window(text string, offset int) (part string, total int, more bool) {
	r := []rune(text)
	total = len(r)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := offset + maxText
	if end > total {
		end = total
	}
	return string(r[offset:end]), total, end < total
}

func clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func day(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04")
}

// --- Documents ---

type docSummary struct {
	ID       string   `json:"id"`
	Filename string   `json:"filename"`
	Type     string   `json:"type,omitempty"`
	Status   string   `json:"status"`
	Imported string   `json:"imported"`
	Tags     []string `json:"tags,omitempty"`
	Comment  string   `json:"comment,omitempty"`
}

func docOf(j webapp.Job) docSummary {
	return docSummary{ID: j.ID, Filename: j.Filename, Type: j.DocType, Status: string(j.Status), Imported: day(j.CreatedAt), Tags: j.Tags, Comment: j.Comment}
}

func (t *Toolbox) job(ctx context.Context, id string) (webapp.Job, error) {
	j, ok, err := t.Jobs.Get(ctx, id)
	if err != nil {
		return webapp.Job{}, err
	}
	if !ok {
		return webapp.Job{}, fmt.Errorf("document %q introuvable", id)
	}
	return j, nil
}

func (t *Toolbox) documentTools() []toolDef {
	return []toolDef{
		{
			name: "search_documents",
			desc: "Cherche dans la bibliothèque de documents (factures, devis, contrats, pièces…) : nom de fichier, type, tags, commentaire et texte du document. Requête vide : les plus récents.",
			props: map[string]any{
				"query": str("Mots à trouver (sous-chaîne, sans casse). Un ou deux mots : la recherche est littérale."),
				"limit": integer("Nombre de résultats (20 au plus)."),
			},
			run: func(ctx context.Context, raw json.RawMessage) (any, outcome, error) {
				var in struct {
					Query string `json:"query"`
					Limit int    `json:"limit"`
				}
				if err := args(raw, &in); err != nil {
					return nil, outcome{}, err
				}
				jobs, err := t.Jobs.List(ctx, webapp.ListQuery{Search: strings.TrimSpace(in.Query), Limit: limitOf(in.Limit), SummaryOnly: true})
				if err != nil {
					return nil, outcome{}, err
				}
				out := make([]docSummary, 0, len(jobs))
				for _, j := range jobs {
					out = append(out, docOf(j))
				}
				return map[string]any{"documents": out}, outcome{summary: fmt.Sprintf("Documents « %s » : %d", in.Query, len(out)), link: "/documents"}, nil
			},
		},
		{
			name: "read_document",
			desc: "Lit un document : ses métadonnées, les champs extraits (avec leur confiance) et son texte. Le texte long se lit par tranches (offset).",
			props: map[string]any{
				"id":     str("Identifiant du document."),
				"offset": integer("Caractère où commencer le texte (0 par défaut)."),
			},
			required: []string{"id"},
			run: func(ctx context.Context, raw json.RawMessage) (any, outcome, error) {
				var in struct {
					ID     string `json:"id"`
					Offset int    `json:"offset"`
				}
				if err := args(raw, &in); err != nil {
					return nil, outcome{}, err
				}
				j, err := t.job(ctx, in.ID)
				if err != nil {
					return nil, outcome{}, err
				}
				out := map[string]any{"document": docOf(j)}
				if j.Err != "" {
					out["error"] = j.Err
				}
				text := j.SearchText
				if j.Result != nil {
					if len(j.Result.Merged.JSON) > 0 {
						out["extracted_fields"] = j.Result.Merged.JSON
						out["fields_need_review"] = j.Result.Merged.NeedsReview
					}
					if text == "" {
						text = j.Result.SearchText
					}
				}
				part, total, more := window(text, in.Offset)
				out["text"], out["text_chars"], out["more_text"] = part, total, more
				return out, outcome{summary: "Document lu : " + j.Filename, link: "/documents/" + j.ID}, nil
			},
		},
		{
			name: "tag_document", action: true,
			desc: "Remplace les tags d'un document par la liste donnée (donne la liste complète : les tags absents sont retirés).",
			props: map[string]any{
				"id":   str("Identifiant du document."),
				"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Tous les tags du document."},
			},
			required: []string{"id", "tags"},
			run: func(ctx context.Context, raw json.RawMessage) (any, outcome, error) {
				var in struct {
					ID   string   `json:"id"`
					Tags []string `json:"tags"`
				}
				if err := args(raw, &in); err != nil {
					return nil, outcome{}, err
				}
				j, err := t.job(ctx, in.ID)
				if err != nil {
					return nil, outcome{}, err
				}
				tags := cleanTags(in.Tags)
				if err := t.Jobs.SetTags(ctx, j.ID, tags); err != nil {
					return nil, outcome{}, err
				}
				return map[string]any{"ok": true, "tags": tags}, outcome{summary: fmt.Sprintf("Tags de %s : %s", j.Filename, strings.Join(tags, ", ")), link: "/documents/" + j.ID}, nil
			},
		},
		{
			name: "comment_document", action: true,
			desc: "Remplace le commentaire d'un document (texte libre de l'utilisateur, trouvé par la recherche).",
			props: map[string]any{
				"id":      str("Identifiant du document."),
				"comment": str("Le nouveau commentaire, entier."),
			},
			required: []string{"id", "comment"},
			run: func(ctx context.Context, raw json.RawMessage) (any, outcome, error) {
				var in struct {
					ID      string `json:"id"`
					Comment string `json:"comment"`
				}
				if err := args(raw, &in); err != nil {
					return nil, outcome{}, err
				}
				j, err := t.job(ctx, in.ID)
				if err != nil {
					return nil, outcome{}, err
				}
				if err := t.Jobs.SetComment(ctx, j.ID, strings.TrimSpace(in.Comment)); err != nil {
					return nil, outcome{}, err
				}
				return map[string]any{"ok": true}, outcome{summary: "Commentaire de " + j.Filename, link: "/documents/" + j.ID}, nil
			},
		},
	}
}

func cleanTags(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, tag := range in {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

// --- Emails ---

type mailSummary struct {
	ID       string `json:"id"`
	From     string `json:"from"`
	Subject  string `json:"subject"`
	Date     string `json:"date"`
	Category string `json:"category,omitempty"`
	Summary  string `json:"summary,omitempty"`
	// Reply : l'expéditeur attend une réponse ; Question : laquelle.
	Reply    bool   `json:"reply_expected,omitempty"`
	Question string `json:"question,omitempty"`
	ToPay    string `json:"to_pay,omitempty"`
	Paid     bool   `json:"paid,omitempty"`
	Archived bool   `json:"archived,omitempty"`
}

func mailOf(m mail.Mail) mailSummary {
	s := mailSummary{ID: m.ID, From: m.From.String(), Subject: m.Subject, Date: day(m.Date),
		Category: string(m.Triage.Category), Summary: m.Triage.Summary, Reply: m.Triage.Reply, Question: m.Triage.Question,
		Paid: m.Paid, Archived: m.Archived}
	if m.Payment.ToPay {
		s.ToPay = strings.TrimSpace(strings.Join([]string{m.Payment.Payee, m.Payment.Amount, m.Payment.Currency, m.Payment.DueDate}, " "))
	}
	return s
}

func (t *Toolbox) mailOf(ctx context.Context, id string) (mail.Mail, error) {
	m, ok, err := t.Mail.DB(ctx).Get(ctx, id)
	if err != nil {
		return mail.Mail{}, err
	}
	if !ok {
		return mail.Mail{}, fmt.Errorf("email %q introuvable", id)
	}
	return m, nil
}

func (t *Toolbox) mailTools() []toolDef {
	return []toolDef{
		{
			name: "search_emails",
			desc: "Cherche dans les emails relevés (objet, expéditeur, texte, résumé), du plus récent au plus ancien. filter : all (défaut), to_reply (une réponse est attendue), to_pay (quelque chose à payer, pas encore payé), archived.",
			props: map[string]any{
				"query":  str("Mots à trouver (sous-chaîne, sans casse) ; vide : tous."),
				"filter": map[string]any{"type": "string", "enum": []string{"all", "to_reply", "to_pay", "archived"}},
				"limit":  integer("Nombre de résultats (20 au plus)."),
			},
			run: func(ctx context.Context, raw json.RawMessage) (any, outcome, error) {
				var in struct {
					Query  string `json:"query"`
					Filter string `json:"filter"`
					Limit  int    `json:"limit"`
				}
				if err := args(raw, &in); err != nil {
					return nil, outcome{}, err
				}
				q := mail.Query{Search: strings.TrimSpace(in.Query), Limit: limitOf(in.Limit)}
				switch in.Filter {
				case "", "all":
				case "to_reply":
					q.Reply = true
				case "to_pay":
					q.ToPay = true
				case "archived":
					q.Archive = mail.ArchiveOnly
				default:
					return nil, outcome{}, fmt.Errorf("filter inconnu %q (all, to_reply, to_pay, archived)", in.Filter)
				}
				mails, err := t.Mail.DB(ctx).List(ctx, q)
				if err != nil {
					return nil, outcome{}, err
				}
				out := make([]mailSummary, 0, len(mails))
				for _, m := range mails {
					out = append(out, mailOf(m))
				}
				label := in.Query
				if in.Filter != "" && in.Filter != "all" {
					label = strings.TrimSpace(in.Filter + " " + label)
				}
				return map[string]any{"emails": out}, outcome{summary: fmt.Sprintf("Emails « %s » : %d", label, len(out)), link: "/emails"}, nil
			},
		},
		{
			name: "read_email",
			desc: "Lit un email : en-têtes, tri, ce qu'il y a à payer (bénéficiaire, montant, échéance, IBAN, référence), pièces jointes et texte. Le texte long se lit par tranches (offset).",
			props: map[string]any{
				"id":     str("Identifiant de l'email."),
				"offset": integer("Caractère où commencer le texte (0 par défaut)."),
			},
			required: []string{"id"},
			run: func(ctx context.Context, raw json.RawMessage) (any, outcome, error) {
				var in struct {
					ID     string `json:"id"`
					Offset int    `json:"offset"`
				}
				if err := args(raw, &in); err != nil {
					return nil, outcome{}, err
				}
				m, err := t.mailOf(ctx, in.ID)
				if err != nil {
					return nil, outcome{}, err
				}
				var to, cc, files []string
				for _, a := range m.To {
					to = append(to, a.String())
				}
				for _, a := range m.Cc {
					cc = append(cc, a.String())
				}
				for _, a := range m.Attachments {
					files = append(files, a.Filename)
				}
				part, total, more := window(m.Text, in.Offset)
				out := map[string]any{"email": mailOf(m), "to": to, "cc": cc, "attachments": files,
					"suggested_action": m.Triage.Action, "text": part, "text_chars": total, "more_text": more}
				if p := m.Payment; p.ToPay || p.AutoDebit {
					out["payment"] = map[string]any{"to_pay": p.ToPay, "auto_debit": p.AutoDebit, "payee": p.Payee, "amount": p.Amount,
						"currency": p.Currency, "due_date": p.DueDate, "reference": p.Reference, "iban": p.IBAN, "iban_valid": p.IBANValid,
						"bic": p.BIC, "method": p.Method, "needs_review": p.NeedsReview, "checks": p.Checks}
				}
				return out, outcome{summary: "Email lu : " + clip(m.Subject, 80), link: "/emails/" + m.ID}, nil
			},
		},
		{
			name: "archive_email", action: true,
			desc:     "Range un email dans Jarvis (il quitte les listes ; la boîte de l'utilisateur n'est pas modifiée).",
			props:    map[string]any{"id": str("Identifiant de l'email.")},
			required: []string{"id"},
			run: func(ctx context.Context, raw json.RawMessage) (any, outcome, error) {
				var in struct {
					ID string `json:"id"`
				}
				if err := args(raw, &in); err != nil {
					return nil, outcome{}, err
				}
				m, err := t.mailOf(ctx, in.ID)
				if err != nil {
					return nil, outcome{}, err
				}
				if err := t.Mail.Archive(ctx, m.ID); err != nil {
					return nil, outcome{}, err
				}
				return map[string]any{"ok": true}, outcome{summary: "Email archivé : " + clip(m.Subject, 80), link: "/emails/" + m.ID}, nil
			},
		},
		{
			name: "mark_email_paid", action: true,
			desc:     "Marque comme payé ce que demandait un email. À n'utiliser que si l'utilisateur dit l'avoir payé : Jarvis ne paie jamais rien.",
			props:    map[string]any{"id": str("Identifiant de l'email.")},
			required: []string{"id"},
			run: func(ctx context.Context, raw json.RawMessage) (any, outcome, error) {
				var in struct {
					ID string `json:"id"`
				}
				if err := args(raw, &in); err != nil {
					return nil, outcome{}, err
				}
				m, err := t.mailOf(ctx, in.ID)
				if err != nil {
					return nil, outcome{}, err
				}
				if err := t.Mail.MarkPaid(ctx, m.ID); err != nil {
					return nil, outcome{}, err
				}
				return map[string]any{"ok": true}, outcome{summary: "Marqué payé : " + clip(m.Subject, 80), link: "/emails/" + m.ID}, nil
			},
		},
	}
}

// --- Tâches ---

type taskSummary struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Due      string `json:"due,omitempty"`
	Priority string `json:"priority,omitempty"`
	Done     bool   `json:"done,omitempty"`
	NoteID   string `json:"note_id,omitempty"`
	DocID    string `json:"document_id,omitempty"`
	MailID   string `json:"email_id,omitempty"`
}

func taskOf(t notes.Task) taskSummary {
	return taskSummary{ID: t.ID, Title: t.Title, Due: t.Due, Priority: string(t.Priority), Done: t.Done, NoteID: t.NoteID, DocID: t.DocID, MailID: t.MailID}
}

func (t *Toolbox) task(ctx context.Context, id string) (notes.Task, error) {
	task, ok, err := t.Notes.DB(ctx).GetTask(ctx, id)
	if err != nil {
		return notes.Task{}, err
	}
	if !ok {
		return notes.Task{}, fmt.Errorf("tâche %q introuvable", id)
	}
	return task, nil
}

const priorityDesc = "haute, basse, ou vide pour normale."

func (t *Toolbox) taskTools() []toolDef {
	return []toolDef{
		{
			name: "list_tasks",
			desc: "Liste les tâches de la todo, les non faites d'abord (en retard, puis par échéance). include_done ajoute les tâches terminées.",
			props: map[string]any{
				"include_done": map[string]any{"type": "boolean"},
			},
			run: func(ctx context.Context, raw json.RawMessage) (any, outcome, error) {
				var in struct {
					IncludeDone bool `json:"include_done"`
				}
				if err := args(raw, &in); err != nil {
					return nil, outcome{}, err
				}
				all, err := t.Notes.DB(ctx).ListTasks(ctx, notes.TaskQuery{})
				if err != nil {
					return nil, outcome{}, err
				}
				out := []taskSummary{}
				for _, g := range notes.GroupTasks(all, t.now()) {
					for _, task := range g.Tasks {
						if task.Done && !in.IncludeDone {
							continue
						}
						out = append(out, taskOf(task))
					}
				}
				truncated := len(out) > 200
				if truncated {
					out = out[:200]
				}
				return map[string]any{"today": notes.DateOf(t.now()), "tasks": out, "truncated": truncated}, outcome{summary: fmt.Sprintf("Tâches : %d", len(out)), link: "/tasks"}, nil
			},
		},
		{
			name: "create_task", action: true,
			desc: "Crée une tâche dans la todo. Lie-la à l'email, au document ou à la note d'où elle vient quand il y en a un.",
			props: map[string]any{
				"title":       str("Ce qu'il y a à faire, en une ligne."),
				"due":         str("Échéance AAAA-MM-JJ (facultatif)."),
				"priority":    str(priorityDesc),
				"email_id":    str("Email lié (facultatif)."),
				"document_id": str("Document lié (facultatif)."),
				"note_id":     str("Note liée (facultatif)."),
			},
			required: []string{"title"},
			run: func(ctx context.Context, raw json.RawMessage) (any, outcome, error) {
				var in struct {
					Title    string `json:"title"`
					Due      string `json:"due"`
					Priority string `json:"priority"`
					MailID   string `json:"email_id"`
					DocID    string `json:"document_id"`
					NoteID   string `json:"note_id"`
				}
				if err := args(raw, &in); err != nil {
					return nil, outcome{}, err
				}
				if err := t.checkLinks(ctx, in.MailID, in.DocID, in.NoteID); err != nil {
					return nil, outcome{}, err
				}
				task, err := t.Notes.NewTask(ctx, notes.TaskInput{Title: in.Title, Due: in.Due, Priority: in.Priority, MailID: in.MailID, DocID: in.DocID, NoteID: in.NoteID})
				if err != nil {
					return nil, outcome{}, err
				}
				summary := "Tâche créée : " + task.Title
				if task.Due != "" {
					summary += " (" + task.Due + ")"
				}
				return map[string]any{"task": taskOf(task)}, outcome{summary: summary, link: "/tasks"}, nil
			},
		},
		{
			name: "update_task", action: true,
			desc: "Modifie une tâche : seuls les champs donnés changent. due vide (\"\") retire l'échéance.",
			props: map[string]any{
				"id":       str("Identifiant de la tâche."),
				"title":    str("Nouveau titre."),
				"due":      str("Nouvelle échéance AAAA-MM-JJ, ou \"\" pour la retirer."),
				"priority": str(priorityDesc),
			},
			required: []string{"id"},
			run: func(ctx context.Context, raw json.RawMessage) (any, outcome, error) {
				var in struct {
					ID       string  `json:"id"`
					Title    *string `json:"title"`
					Due      *string `json:"due"`
					Priority *string `json:"priority"`
				}
				if err := args(raw, &in); err != nil {
					return nil, outcome{}, err
				}
				task, err := t.task(ctx, in.ID)
				if err != nil {
					return nil, outcome{}, err
				}
				title, due, priority := task.Title, task.Due, string(task.Priority)
				if in.Title != nil {
					title = *in.Title
				}
				if in.Due != nil {
					due = *in.Due
				}
				if in.Priority != nil {
					priority = *in.Priority
				}
				saved, err := t.Notes.SaveTask(ctx, task.ID, title, due, priority, task.NoteID, task.DocID)
				if err != nil {
					return nil, outcome{}, err
				}
				return map[string]any{"task": taskOf(saved)}, outcome{summary: "Tâche modifiée : " + saved.Title, link: "/tasks"}, nil
			},
		},
		{
			name: "complete_task", action: true,
			desc:     "Coche une tâche comme faite.",
			props:    map[string]any{"id": str("Identifiant de la tâche.")},
			required: []string{"id"},
			run: func(ctx context.Context, raw json.RawMessage) (any, outcome, error) {
				var in struct {
					ID string `json:"id"`
				}
				if err := args(raw, &in); err != nil {
					return nil, outcome{}, err
				}
				task, err := t.task(ctx, in.ID)
				if err != nil {
					return nil, outcome{}, err
				}
				if !task.Done {
					if task, err = t.Notes.ToggleTask(ctx, task.ID); err != nil {
						return nil, outcome{}, err
					}
				}
				return map[string]any{"task": taskOf(task)}, outcome{summary: "Tâche faite : " + task.Title, link: "/tasks"}, nil
			},
		},
	}
}

// checkLinks : un lien vers un email, un document ou une note qui n'existe
// pas est refusé — le modèle a pu inventer ou tronquer un identifiant.
func (t *Toolbox) checkLinks(ctx context.Context, mailID, docID, noteID string) error {
	if mailID != "" {
		if t.Mail == nil {
			return errors.New("les emails ne sont pas disponibles : email_id refusé")
		}
		if _, err := t.mailOf(ctx, mailID); err != nil {
			return err
		}
	}
	if docID != "" {
		if t.Jobs == nil {
			return errors.New("les documents ne sont pas disponibles : document_id refusé")
		}
		if _, err := t.job(ctx, docID); err != nil {
			return err
		}
	}
	if noteID != "" {
		if _, err := t.note(ctx, noteID); err != nil {
			return err
		}
	}
	return nil
}

// --- Notes ---

type noteSummary struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Tags    []string `json:"tags,omitempty"`
	Updated string   `json:"updated"`
	Excerpt string   `json:"excerpt,omitempty"`
}

func (t *Toolbox) note(ctx context.Context, id string) (notes.Note, error) {
	n, ok, err := t.Notes.DB(ctx).GetNote(ctx, id)
	if err != nil {
		return notes.Note{}, err
	}
	if !ok {
		return notes.Note{}, fmt.Errorf("note %q introuvable", id)
	}
	return n, nil
}

func (t *Toolbox) noteTools() []toolDef {
	return []toolDef{
		{
			name: "search_notes",
			desc: "Cherche dans les notes (titre, texte, tags). Requête vide : les plus récentes.",
			props: map[string]any{
				"query": str("Mots à trouver (sous-chaîne, sans casse)."),
				"limit": integer("Nombre de résultats (20 au plus)."),
			},
			run: func(ctx context.Context, raw json.RawMessage) (any, outcome, error) {
				var in struct {
					Query string `json:"query"`
					Limit int    `json:"limit"`
				}
				if err := args(raw, &in); err != nil {
					return nil, outcome{}, err
				}
				list, err := t.Notes.DB(ctx).ListNotes(ctx, notes.NoteQuery{Search: strings.TrimSpace(in.Query)})
				if err != nil {
					return nil, outcome{}, err
				}
				if max := limitOf(in.Limit); len(list) > max {
					list = list[:max]
				}
				out := make([]noteSummary, 0, len(list))
				for _, n := range list {
					out = append(out, noteSummary{ID: n.ID, Title: n.Title, Tags: n.Tags, Updated: day(n.UpdatedAt), Excerpt: clip(strings.TrimSpace(n.Body), 200)})
				}
				return map[string]any{"notes": out}, outcome{summary: fmt.Sprintf("Notes « %s » : %d", in.Query, len(out)), link: "/notes"}, nil
			},
		},
		{
			name: "read_note",
			desc: "Lit une note en entier (Markdown), avec ses documents et son email liés. Le texte long se lit par tranches (offset).",
			props: map[string]any{
				"id":     str("Identifiant de la note."),
				"offset": integer("Caractère où commencer le texte (0 par défaut)."),
			},
			required: []string{"id"},
			run: func(ctx context.Context, raw json.RawMessage) (any, outcome, error) {
				var in struct {
					ID     string `json:"id"`
					Offset int    `json:"offset"`
				}
				if err := args(raw, &in); err != nil {
					return nil, outcome{}, err
				}
				n, err := t.note(ctx, in.ID)
				if err != nil {
					return nil, outcome{}, err
				}
				part, total, more := window(n.Body, in.Offset)
				return map[string]any{"id": n.ID, "title": n.Title, "tags": n.Tags, "updated": day(n.UpdatedAt),
						"document_ids": n.DocIDs, "email_id": n.MailID, "text": part, "text_chars": total, "more_text": more},
					outcome{summary: "Note lue : " + n.Title, link: "/notes/" + n.ID}, nil
			},
		},
		{
			name: "create_note", action: true,
			desc: "Crée une note en Markdown. Lie-la à l'email ou au document dont elle parle quand il y en a un.",
			props: map[string]any{
				"title":       str("Titre de la note."),
				"text":        str("Texte en Markdown."),
				"email_id":    str("Email lié (facultatif)."),
				"document_id": str("Document lié (facultatif)."),
			},
			required: []string{"title", "text"},
			run: func(ctx context.Context, raw json.RawMessage) (any, outcome, error) {
				var in struct {
					Title  string `json:"title"`
					Text   string `json:"text"`
					MailID string `json:"email_id"`
					DocID  string `json:"document_id"`
				}
				if err := args(raw, &in); err != nil {
					return nil, outcome{}, err
				}
				if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Text) == "" {
					return nil, outcome{}, errors.New("title et text sont obligatoires")
				}
				if err := t.checkLinks(ctx, in.MailID, in.DocID, ""); err != nil {
					return nil, outcome{}, err
				}
				n, err := t.Notes.NewMailNote(ctx, in.MailID, in.Title, in.Text)
				if err != nil {
					return nil, outcome{}, err
				}
				if in.DocID != "" {
					if err := t.Notes.LinkDoc(ctx, n.ID, in.DocID); err != nil {
						return nil, outcome{}, fmt.Errorf("note créée (%s) mais pas liée au document : %w", n.ID, err)
					}
				}
				return map[string]any{"id": n.ID, "title": n.Title}, outcome{summary: "Note créée : " + n.Title, link: "/notes/" + n.ID}, nil
			},
		},
		{
			name: "append_to_note", action: true,
			desc: "Ajoute un passage (Markdown) à la fin d'une note existante, sans toucher au reste.",
			props: map[string]any{
				"id":   str("Identifiant de la note."),
				"text": str("Texte à ajouter."),
			},
			required: []string{"id", "text"},
			run: func(ctx context.Context, raw json.RawMessage) (any, outcome, error) {
				var in struct {
					ID   string `json:"id"`
					Text string `json:"text"`
				}
				if err := args(raw, &in); err != nil {
					return nil, outcome{}, err
				}
				if strings.TrimSpace(in.Text) == "" {
					return nil, outcome{}, errors.New("text est vide")
				}
				n, err := t.note(ctx, in.ID)
				if err != nil {
					return nil, outcome{}, err
				}
				b, err := t.Notes.AddBlock(ctx, n.ID, "")
				if err != nil {
					return nil, outcome{}, err
				}
				if _, err := t.Notes.SaveBlock(ctx, n.ID, b.ID, in.Text, ""); err != nil {
					return nil, outcome{}, err
				}
				return map[string]any{"ok": true}, outcome{summary: "Ajout à la note : " + n.Title, link: "/notes/" + n.ID}, nil
			},
		},
	}
}
