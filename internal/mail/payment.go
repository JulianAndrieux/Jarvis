package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/JulianAndrieux/Jarvis/internal/extraction"
	"github.com/JulianAndrieux/Jarvis/internal/llm"
	"github.com/JulianAndrieux/Jarvis/internal/triage"
)

// Paiements repérés dans les emails : une passe dédiée, après le tri, sur
// les seuls candidats (PaymentCandidate) — augmenter TriageVersion ferait
// retrier toute la boîte. Le modèle local recopie les informations pour
// payer ; des contrôles déterministes vérifient ensuite ce qu'il a rendu
// (décision assumée : ce n'est pas une extraction par regex, seulement un
// contrôle de la sortie du modèle contre le texte source).

// DefaultPaymentPrompt : les consignes par défaut. Modifiables depuis
// l'interface (onglet Agents).
const DefaultPaymentPrompt = `Tu aides l'utilisateur à savoir ce qu'il doit payer. Lis l'email ci-dessous (et le texte des PDF joints, s'il y en a) et réponds en JSON :
- to_pay : true si l'email demande à l'utilisateur de régler lui-même une somme : facture, avis d'échéance, relance, amende, cotisation, appel de fonds. false pour un reçu ou une confirmation de paiement déjà effectué, un prélèvement automatique, une promotion, un devis non accepté, un simple relevé.
- auto_debit : true si la somme sera prélevée automatiquement (prélèvement SEPA, mandat) : l'utilisateur n'a rien à faire, to_pay est alors false.
- payee : à qui payer (le nom du bénéficiaire, tel qu'écrit).
- amount : le montant à payer, recopié exactement tel qu'il est écrit (le total TTC restant dû, pas le HT ni la TVA), sans le symbole de devise.
- currency : la devise en code ISO (EUR, CHF, USD…).
- due_date : la date limite de paiement au format AAAA-MM-JJ ; chaîne vide si elle n'est pas écrite.
- reference : la référence à rappeler avec le paiement (numéro de facture, de dossier, de client).
- iban, bic : recopiés exactement s'ils figurent dans le texte.
- method : le moyen de paiement demandé : virement, prelevement, carte, lien (paiement en ligne), cheque, autre ; chaîne vide si rien n'est dit.
- link : le lien de paiement en ligne, recopié exactement, s'il y en a un.
- evidence : la phrase du texte qui porte le montant, recopiée.
- confidence : entre 0 et 1, ta confiance dans ces informations.
Recopie les valeurs exactement comme elles sont écrites, n'invente rien : une valeur absente du texte donne une chaîne vide.`

// maxPaymentAttachments : PDF joints lus au plus ; maxPaymentAttachmentText :
// leur texte envoyé au modèle, au plus, tous PDF confondus (le contexte du
// modèle des documents est de 8k jetons).
const (
	maxPaymentAttachments    = 2
	maxPaymentAttachmentText = 3000
)

// PaymentMethods : les moyens de paiement reconnus ("" : rien n'est dit).
var PaymentMethods = []string{"virement", "prelevement", "carte", "lien", "cheque", "autre", ""}

// PaymentAnalyzer fait repérer par le modèle local ce qu'un email demande
// de payer.
type PaymentAnalyzer struct {
	LLM   llm.Client
	Model string // nom du modèle, gardé avec l'analyse
	// Prompt : les consignes en vigueur (agents.Registry) ; vide :
	// DefaultPaymentPrompt.
	Prompt func() string
	// PDFText lit la couche texte des PDF joints (pdftotext ; jamais le
	// VLM : une pièce jointe scannée est signalée). nil : non lus.
	PDFText triage.TextExtractor
	// WorkDir : où écrire un PDF le temps de le lire ("" : dossier
	// temporaire du système).
	WorkDir string
	Now     func() time.Time
}

var paymentSchema = func() json.RawMessage {
	str := map[string]any{"type": "string"}
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"required": []string{"amount", "auto_debit", "bic", "confidence", "currency", "due_date", "evidence",
			"iban", "link", "method", "payee", "reference", "to_pay"},
		// Clés triées : to_pay en dernier, le modèle tranche après avoir
		// relevé les valeurs (comme reply dans le tri).
		"properties": map[string]any{
			"amount": str, "auto_debit": map[string]any{"type": "boolean"}, "bic": str,
			"confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
			"currency":   str, "due_date": str, "evidence": str, "iban": str, "link": str,
			"method": map[string]any{"type": "string", "enum": PaymentMethods},
			"payee":  str, "reference": str, "to_pay": map[string]any{"type": "boolean"},
		},
	})
	return b
}()

// Analyze repère ce qu'il y a à payer dans m. st : la persistance vue
// depuis la portée de l'appel (lecture des pièces jointes). Une réponse
// inutilisable enveloppe errBadReply.
func (a PaymentAnalyzer) Analyze(ctx context.Context, st Store, m Mail) (Payment, error) {
	prompt := DefaultPaymentPrompt
	if a.Prompt != nil {
		if p := strings.TrimSpace(a.Prompt()); p != "" {
			prompt = p
		}
	}
	attached, sources, notes := a.attachmentTexts(ctx, st, m)
	input := triageInput(m) + attached
	res, err := a.LLM.Extract(ctx, llm.ExtractRequest{Page: 1, Text: input, Schema: paymentSchema, Prompt: prompt})
	if err != nil {
		return Payment{}, fmt.Errorf("mail: paiement : %w", err)
	}
	var out struct {
		ToPay      bool    `json:"to_pay"`
		AutoDebit  bool    `json:"auto_debit"`
		Payee      string  `json:"payee"`
		Amount     string  `json:"amount"`
		Currency   string  `json:"currency"`
		DueDate    string  `json:"due_date"`
		Reference  string  `json:"reference"`
		IBAN       string  `json:"iban"`
		BIC        string  `json:"bic"`
		Method     string  `json:"method"`
		Link       string  `json:"link"`
		Evidence   string  `json:"evidence"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal(res.JSON, &out); err != nil {
		return Payment{}, fmt.Errorf("mail: paiement : %w (%s)", errBadReply, res.JSON)
	}
	method := strings.TrimSpace(out.Method)
	known := false
	for _, k := range PaymentMethods {
		known = known || method == k
	}
	if !known {
		return Payment{}, fmt.Errorf("mail: paiement : %w (moyen de paiement inconnu %q)", errBadReply, out.Method)
	}
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	p := checkPayment(Payment{
		ToPay: out.ToPay, AutoDebit: out.AutoDebit, Payee: strings.TrimSpace(out.Payee), Amount: strings.TrimSpace(out.Amount),
		Currency: strings.ToUpper(strings.TrimSpace(out.Currency)), DueDate: strings.TrimSpace(out.DueDate),
		Reference: strings.TrimSpace(out.Reference), IBAN: out.IBAN, BIC: strings.TrimSpace(out.BIC), Method: method,
		Link: strings.TrimSpace(out.Link), Evidence: strings.TrimSpace(out.Evidence), Confidence: out.Confidence,
	}, input)
	p.Checks = append(notes, p.Checks...)
	p.Sources = sources
	p.Version, p.Model, p.Prompt, p.At = PaymentVersion, a.Model, prompt, now()
	return p, nil
}

// attachmentTexts : le texte des PDF joints, à ajouter à ce que lit le
// modèle ; sources : "corps" puis les PDF lus ; notes : ce qui n'a pas pu
// être lu (jamais une raison d'échouer).
func (a PaymentAnalyzer) attachmentTexts(ctx context.Context, st Store, m Mail) (text string, sources, notes []string) {
	sources = []string{"corps"}
	if a.PDFText == nil {
		return "", sources, nil
	}
	var b strings.Builder
	budget, read := maxPaymentAttachmentText, 0
	for _, att := range m.Attachments {
		isPDF := att.ContentType == "application/pdf" || strings.HasSuffix(strings.ToLower(att.Filename), ".pdf")
		if !att.Stored || !isPDF {
			continue
		}
		if read == maxPaymentAttachments || budget <= 0 {
			notes = append(notes, fmt.Sprintf("« %s » : non lu (au plus %d PDF joints, %d caractères)", att.Filename, maxPaymentAttachments, maxPaymentAttachmentText))
			continue
		}
		read++
		content, err := a.pdfText(ctx, st, m.ID, att.Index)
		switch {
		case err != nil:
			notes = append(notes, fmt.Sprintf("« %s » illisible : %v", att.Filename, err))
			continue
		case strings.TrimSpace(content) == "":
			notes = append(notes, fmt.Sprintf("« %s » : PDF sans texte (scanné ?), non lu — envoie-le dans Documents pour le faire lire", att.Filename))
			continue
		}
		content = strings.TrimSpace(content)
		if len(content) > budget {
			notes = append(notes, fmt.Sprintf("« %s » : seul le début a été lu", att.Filename))
			content = truncate(content, budget)
		}
		budget -= len(content)
		fmt.Fprintf(&b, "\n\n--- Pièce jointe « %s » ---\n%s", att.Filename, content)
		sources = append(sources, att.Filename)
	}
	return b.String(), sources, notes
}

// pdfText : la couche texte d'une pièce jointe PDF, lue depuis un fichier
// temporaire supprimé aussitôt.
func (a PaymentAnalyzer) pdfText(ctx context.Context, st Store, id string, index int) (string, error) {
	data, ok, err := st.Attachment(ctx, id, index)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("contenu introuvable")
	}
	f, err := os.CreateTemp(a.WorkDir, "mail-pdf-*.pdf")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	pages, err := a.PDFText.ExtractPerPage(ctx, f.Name())
	if err != nil {
		return "", err
	}
	texts := make([]string, len(pages))
	for i, p := range pages {
		texts[i] = p.Text
	}
	return strings.Join(texts, "\n"), nil
}

// checkPayment contrôle ce que le modèle a rendu contre source (le texte
// qu'il a lu) : un IBAN, un BIC ou un lien absents du texte sont écartés,
// une clé d'IBAN fausse, un montant introuvable, une confiance basse
// passent en revue, une échéance illisible est vidée. Jamais une valeur
// acceptée en silence.
func checkPayment(p Payment, source string) Payment {
	var checks []string
	review := false
	note := func(needsReview bool, format string, args ...any) {
		checks = append(checks, fmt.Sprintf(format, args...))
		review = review || needsReview
	}
	if p.IBAN = normalizeIBAN(p.IBAN); p.IBAN != "" {
		switch {
		case !inSource(p.IBAN, source):
			note(true, "IBAN absent du texte de l'email : écarté")
			p.IBAN = ""
		case !validIBAN(p.IBAN):
			note(true, "IBAN : clé de contrôle invalide — à vérifier avant de payer")
		default:
			p.IBANValid = true
		}
	}
	if p.BIC != "" && !inSource(p.BIC, source) {
		note(true, "BIC absent du texte de l'email : écarté")
		p.BIC = ""
	}
	if p.Link != "" {
		switch {
		case !strings.HasPrefix(p.Link, "https://"):
			note(true, "Lien de paiement non sécurisé (pas https) : écarté")
			p.Link = ""
		case !strings.Contains(source, p.Link):
			note(true, "Lien de paiement absent du texte de l'email : écarté")
			p.Link = ""
		}
	}
	if p.Amount != "" && !amountInSource(p.Amount, source) {
		note(true, "Montant introuvable tel quel dans le texte : à vérifier")
	}
	if p.DueDate != "" {
		if _, err := time.Parse("2006-01-02", p.DueDate); err != nil {
			note(false, "Échéance illisible (%q) : vidée", p.DueDate)
			p.DueDate = ""
		}
	}
	p.ToPay = p.ToPay && !p.AutoDebit
	if p.ToPay && p.Amount == "" {
		note(true, "Montant non trouvé : à vérifier")
	}
	p.Confidence = min(max(p.Confidence, 0), 1)
	if p.Confidence < extraction.DefaultConfidenceThreshold {
		note(true, "Confiance du modèle basse (%.2f)", p.Confidence)
	}
	p.Checks, p.NeedsReview = checks, review
	return p
}

// compact : s en majuscules, sans aucun espace (insécables compris).
func compact(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == ' ' || r == ' ' {
			return -1
		}
		return unicode.ToUpper(r)
	}, s)
}

// normalizeIBAN : en majuscules, sans espaces.
func normalizeIBAN(s string) string { return compact(s) }

// inSource : value figure dans source, aux espaces et à la casse près.
func inSource(value, source string) bool {
	v := compact(value)
	return v != "" && strings.Contains(compact(source), v)
}

// amountInSource : le montant figure dans source, aux espaces près, la
// virgule et le point décimal confondus (« 1234.56 » est trouvé dans
// « 1 234,56 € »).
func amountInSource(amount, source string) bool {
	num := strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) || r == ',' || r == '.' {
			return r
		}
		return -1 // espaces, symbole de devise
	}, amount)
	num = strings.Trim(num, ",.")
	if num == "" {
		return false
	}
	dot := strings.NewReplacer(",", ".")
	return strings.Contains(dot.Replace(compact(source)), dot.Replace(num))
}

// validIBAN : la clé ISO 13616 (mod 97) d'un IBAN normalisé est juste. Ne
// prouve pas que le compte est le bon : un email frauduleux peut porter un
// IBAN valide.
func validIBAN(iban string) bool {
	if len(iban) < 15 || len(iban) > 34 {
		return false
	}
	for i, r := range iban {
		switch {
		case i < 2 && (r < 'A' || r > 'Z'):
			return false
		case i >= 2 && i < 4 && (r < '0' || r > '9'):
			return false
		case !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z'):
			return false
		}
	}
	var digits strings.Builder
	for _, r := range iban[4:] + iban[:4] {
		if r >= 'A' {
			fmt.Fprintf(&digits, "%d", r-'A'+10)
		} else {
			digits.WriteRune(r)
		}
	}
	n, ok := new(big.Int).SetString(digits.String(), 10)
	return ok && new(big.Int).Mod(n, big.NewInt(97)).Int64() == 1
}

// PaymentTaskInput : la tâche proposée pour un email à payer, en saisie
// rapide (l'échéance en JJ/MM/AAAA, en dernier mot, devient celle de la
// tâche).
func PaymentTaskInput(m Mail) string {
	p := m.Payment
	payee := p.Payee
	if payee == "" {
		payee = m.Subject
	}
	parts := []string{"Payer", payee}
	if p.Amount != "" {
		parts = append(parts, strings.TrimSpace(p.Amount+" "+p.Currency))
	}
	if d, err := time.Parse("2006-01-02", p.DueDate); err == nil {
		parts = append(parts, d.Format("02/01/2006"))
	}
	return strings.Join(parts, " ")
}
