package templates

import "testing"

func TestGroupIBAN(t *testing.T) {
	for in, want := range map[string]string{
		"FR7630006000011234567890189": "FR76 3000 6000 0112 3456 7890 189",
		"DE89370400440532013000":      "DE89 3704 0044 0532 0130 00",
		"FR76":                        "FR76",
		"":                            "",
	} {
		if got := GroupIBAN(in); got != want {
			t.Errorf("GroupIBAN(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPaymentMethodLabel(t *testing.T) {
	if paymentMethodLabel("prelevement") != "Prélèvement" || paymentMethodLabel("lien") != "Paiement en ligne" || paymentMethodLabel("") != "" {
		t.Error("labels")
	}
}
