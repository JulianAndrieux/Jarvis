package agents

import (
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/mail"
)

// Les agents du système, chacun avec un prompt par défaut valide (sinon il
// serait refusé à la première modification) et ses outils.
func TestDefaults_ValidAgents(t *testing.T) {
	defs := Defaults(Models{Documents: "qwen3-8b Q5_K_M", Tickets: "qwen3-8b"})
	ids := []string{Classification, Extraction, MailTriage, MailPayment, Analysis, Development, Review}
	if len(defs) != len(ids) {
		t.Fatalf("defs = %d, want %d", len(defs), len(ids))
	}
	for i, d := range defs {
		if d.ID != ids[i] || d.Name == "" || d.Role == "" || d.Group == "" || d.Model == "" {
			t.Errorf("def %d = %+v", i, d)
		}
		if err := Validate(d, d.DefaultPrompt); err != nil {
			t.Errorf("%s: default prompt invalid: %v", d.ID, err)
		}
	}
	if len(defs[4].Tools) == 0 || len(defs[5].Tools) <= len(defs[4].Tools) {
		t.Errorf("tools: analysis %v, development %v (development adds write tools)", defs[4].Tools, defs[5].Tools)
	}
	if defs[2].Group != "Emails" || defs[2].Model != "qwen3-8b Q5_K_M" {
		t.Errorf("mail triage = %+v, want the documents model", defs[2])
	}
	if len(defs[0].Placeholders) != 1 || len(defs[1].Placeholders) != 1 {
		t.Error("classification and extraction each need their placeholder")
	}
}

// Paiements des emails : un agent de la famille Emails, sur le modèle des
// documents, au prompt par défaut celui du paquet mail.
func TestDefaults_IncludesMailPayment(t *testing.T) {
	for _, d := range Defaults(Models{Documents: "qwen3-8b Q5_K_M", Tickets: "devstral"}) {
		if d.ID != MailPayment {
			continue
		}
		if d.DefaultPrompt != mail.DefaultPaymentPrompt || d.Group != "Emails" || d.Model != "qwen3-8b Q5_K_M" || d.Name == "" {
			t.Errorf("mail payment = %+v", d)
		}
		return
	}
	t.Fatal("no mail payment agent")
}
