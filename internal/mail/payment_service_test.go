package mail

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/llm"
)

// routedLLM : le tri et l'analyse des paiements reçoivent chacun leur
// réponse (reconnus à leur schéma) ; les requêtes de paiement sont
// comptées.
type routedLLM struct {
	mu              sync.Mutex
	triage, payment string
	paymentErr      error
	payments        int
}

func (r *routedLLM) Extract(ctx context.Context, req llm.ExtractRequest) (llm.ExtractResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.Contains(string(req.Schema), `"to_pay"`) {
		r.payments++
		if r.paymentErr != nil {
			return llm.ExtractResult{}, r.paymentErr
		}
		return llm.ExtractResult{JSON: json.RawMessage(r.payment)}, nil
	}
	return llm.ExtractResult{JSON: json.RawMessage(r.triage)}, nil
}

// paymentService : une facture « à traiter » et une newsletter, déjà
// triées ; la boîte n'est pas configurée (pas de relève).
func paymentService(t *testing.T, model *routedLLM) (*Service, *FakeStore) {
	t.Helper()
	s, store := newService(t, oneMailbox(), &scriptedLLM{})
	s.Triager.LLM = model
	s.Payments = &PaymentAnalyzer{LLM: model, Model: "qwen3-8b", Now: func() time.Time { return now }}
	ctx := context.Background()
	store.Save(ctx, invoiceMail(), nil)
	store.Save(ctx, Mail{ID: "m-news", Subject: "Promo", Date: now, Text: "-50 %", Triage: Triage{Category: Newsletter, Version: TriageVersion}}, nil)
	return s, store
}

func TestService_AnalyzesPaymentsAfterTriage(t *testing.T) {
	model := &routedLLM{payment: goodReply}
	s, store := paymentService(t, model)
	var acquired int
	s.Acquire = func(ctx context.Context) (func(), error) { acquired++; return func() {}, nil }
	s.Cycle(context.Background())
	inv, _, _ := store.Get(context.Background(), "m-pay")
	if inv.Payment.Version != PaymentVersion || !inv.Payment.ToPay || inv.Payment.Payee != "Acme SARL" {
		t.Errorf("invoice payment = %+v", inv.Payment)
	}
	if news, _, _ := store.Get(context.Background(), "m-news"); news.Payment.Version != 0 {
		t.Errorf("newsletter analysed: %+v", news.Payment)
	}
	if model.payments != 1 || acquired != 1 {
		t.Errorf("payment requests = %d, acquired = %d, want 1 each (through the model queue)", model.payments, acquired)
	}
	s.Cycle(context.Background())
	if model.payments != 1 {
		t.Errorf("payment requests = %d after a second cycle, want no re-analysis", model.payments)
	}
}

func TestService_PaymentBadReplyNotedOnMail(t *testing.T) {
	model := &routedLLM{payment: `pas du json`}
	s, store := paymentService(t, model)
	s.Cycle(context.Background())
	inv, _, _ := store.Get(context.Background(), "m-pay")
	if inv.Payment.Error == "" || inv.Payment.Version != PaymentVersion || inv.Payment.ToPay {
		t.Errorf("payment = %+v, want the bad reply noted", inv.Payment)
	}
	s.Cycle(context.Background())
	if model.payments != 1 {
		t.Errorf("payment requests = %d, want a noted bad reply not retried", model.payments)
	}
}

func TestService_PaymentModelDownStopsWithoutNoting(t *testing.T) {
	model := &routedLLM{paymentErr: errors.New("connection refused")}
	s, store := paymentService(t, model)
	s.Cycle(context.Background())
	if inv, _, _ := store.Get(context.Background(), "m-pay"); inv.Payment.Version != 0 || inv.Payment.Error != "" {
		t.Errorf("payment = %+v, want nothing noted", inv.Payment)
	}
	if st := s.Status(); !strings.Contains(st.LastError, "Paiements : ") || !strings.Contains(st.LastError, "connection refused") || strings.Contains(st.LastError, "mail: ") {
		t.Errorf("Status = %+v", st)
	}
	model.paymentErr, model.payment = nil, goodReply
	s.Cycle(context.Background())
	if st := s.Status(); st.LastError != "" {
		t.Errorf("Status after recovery = %+v", st)
	}

	s2, store2 := paymentService(t, &routedLLM{payment: goodReply})
	s2.Acquire = func(ctx context.Context) (func(), error) { return nil, errors.New("modèles indisponibles") }
	s2.Cycle(context.Background())
	if inv, _, _ := store2.Get(context.Background(), "m-pay"); inv.Payment.Version != 0 {
		t.Errorf("payment = %+v without the model queue", inv.Payment)
	}
}

func TestService_RetriageResetsPaymentButKeepsPaid(t *testing.T) {
	s, store := paymentService(t, &routedLLM{payment: goodReply})
	ctx := context.Background()
	s.Cycle(ctx)
	if err := s.MarkPaid(ctx, "m-pay"); err != nil {
		t.Fatal(err)
	}
	if err := s.Retriage(ctx, "m-pay"); err != nil {
		t.Fatal(err)
	}
	m, _, _ := store.Get(ctx, "m-pay")
	if m.Payment.Version != 0 || m.Payment.Payee != "" || !m.Paid {
		t.Errorf("after Retriage: payment %+v, paid %v", m.Payment, m.Paid)
	}
}

func TestService_MarkPaidUnpaid(t *testing.T) {
	s, store := paymentService(t, &routedLLM{})
	ctx := context.Background()
	if err := s.MarkPaid(ctx, "m-pay"); err != nil {
		t.Fatal(err)
	}
	if m, _, _ := store.Get(ctx, "m-pay"); !m.Paid || !m.PaidAt.Equal(now) {
		t.Errorf("after MarkPaid = %v, %v", m.Paid, m.PaidAt)
	}
	if err := s.MarkUnpaid(ctx, "m-pay"); err != nil {
		t.Fatal(err)
	}
	if m, _, _ := store.Get(ctx, "m-pay"); m.Paid || !m.PaidAt.IsZero() {
		t.Errorf("after MarkUnpaid = %v, %v", m.Paid, m.PaidAt)
	}
	if err := s.MarkPaid(ctx, "inconnu"); err == nil {
		t.Error("MarkPaid(unknown) = nil")
	}
}

func TestService_NoPaymentAnalyzer(t *testing.T) {
	model := &routedLLM{payment: goodReply}
	s, store := paymentService(t, model)
	s.Payments = nil
	s.Cycle(context.Background())
	if inv, _, _ := store.Get(context.Background(), "m-pay"); inv.Payment.Version != 0 || model.payments != 0 {
		t.Errorf("payment analysed without an analyzer: %+v", inv.Payment)
	}
}
