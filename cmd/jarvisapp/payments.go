package main

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/JulianAndrieux/Jarvis/cmd/jarvisapp/templates"
	"github.com/JulianAndrieux/Jarvis/internal/mail"
	"github.com/JulianAndrieux/Jarvis/internal/notes"
)

// Paiements repérés dans les emails par le modèle local (mail.
// PaymentAnalyzer) : filtre « À payer », bloc de la fiche, marque « payé »
// — Jarvis ne paie jamais rien.

// paymentHorizon : la barre latérale montre les paiements en retard, sans
// échéance, ou à échéance dans ces jours-là.
const paymentHorizon = 14

// paymentAmount : « 120,00 EUR ».
func paymentAmount(p mail.Payment) string {
	return strings.TrimSpace(p.Amount + " " + p.Currency)
}

// sortByDueDate : par échéance croissante, sans échéance en dernier (ordre
// d'origine gardé à échéance égale).
func sortByDueDate(list []mail.Mail) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i].Payment.DueDate, list[j].Payment.DueDate
		if (a == "") != (b == "") {
			return b == ""
		}
		return a < b
	})
}

// paymentView : le bloc de paiement de la fiche ; nil si l'analyse n'a
// rien trouvé à payer (ni prélèvement, ni erreur, ni marque « payé »).
func paymentView(m mail.Mail) *templates.PaymentView {
	p := m.Payment
	if !p.ToPay && !p.AutoDebit && p.Error == "" && !m.Paid {
		return nil
	}
	v := &templates.PaymentView{
		ToPay: p.ToPay, Payee: p.Payee, Amount: paymentAmount(p), Reference: p.Reference,
		IBAN: templates.GroupIBAN(p.IBAN), IBANValid: p.IBANValid, BIC: p.BIC, Method: p.Method, Link: p.Link,
		Evidence: p.Evidence, Confidence: p.Confidence, NeedsReview: p.NeedsReview, Checks: p.Checks,
		Sources: p.Sources, AutoDebit: p.AutoDebit, Paid: m.Paid, Error: p.Error, Model: p.Model,
	}
	if d, err := time.Parse("2006-01-02", p.DueDate); err == nil {
		v.DueDate = d.Format("02/01/2006")
	}
	if m.Paid {
		v.PaidAt = m.PaidAt.Local().Format("02/01/2006")
	}
	return v
}

func (s *Server) handleEmailPaid(w http.ResponseWriter, r *http.Request) {
	s.setPaid(w, r, true)
}

func (s *Server) handleEmailUnpaid(w http.ResponseWriter, r *http.Request) {
	s.setPaid(w, r, false)
}

// setPaid marque un email payé ou non. Depuis la liste (HTMX) : réponse
// vide, la ligne disparaît de « À payer » ; sinon retour à la fiche.
func (s *Server) setPaid(w http.ResponseWriter, r *http.Request, paid bool) {
	m, ok := s.email(w, r)
	if !ok {
		return
	}
	mark := s.Mail.MarkUnpaid
	if paid {
		mark = s.Mail.MarkPaid
	}
	if err := mark(r.Context(), m.ID); err != nil {
		serverError(w, err)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, localBack(r.FormValue("back"), "/emails/"+m.ID), http.StatusSeeOther)
}

// buildPayments : les paiements de la barre latérale — en retard, sans
// échéance ou à échéance sous paymentHorizon jours, ni payés ni archivés,
// par échéance ; au-delà de sidebarMax, leur nombre.
func buildPayments(now time.Time, mails []mail.Mail) (items []templates.SideItem, more int) {
	horizon := notes.DateOf(now.AddDate(0, 0, paymentHorizon))
	today := notes.DateOf(now)
	var keep []mail.Mail
	for _, m := range mails {
		if m.Payment.ToPay && !m.Paid && !m.Archived && (m.Payment.DueDate == "" || m.Payment.DueDate <= horizon) {
			keep = append(keep, m)
		}
	}
	sortByDueDate(keep)
	for _, m := range keep {
		if len(items) == sidebarMax {
			more++
			continue
		}
		title := m.Payment.Payee
		if title == "" {
			title = m.Subject
		}
		detail := paymentAmount(m.Payment)
		if d, err := time.Parse("2006-01-02", m.Payment.DueDate); err == nil {
			when := "avant le " + d.Format("02/01")
			if m.Payment.DueDate < today {
				when = "en retard (" + d.Format("02/01") + ")"
			}
			detail = strings.TrimSpace(detail + " · " + when)
		}
		items = append(items, templates.SideItem{Href: "/emails/" + m.ID, Icon: "💶", Title: title, Detail: strings.Trim(detail, " ·"), Full: m.Payment.Evidence})
	}
	return items, more
}
