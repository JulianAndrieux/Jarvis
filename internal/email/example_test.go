package email_test

import (
	"fmt"
	"os"

	"github.com/JulianAndrieux/Jarvis/internal/email"
)

// Le format exact de PlainText est figé ici : c'est ce texte qui est indexé
// et converti en PDF, un changement doit être délibéré.
func ExampleMessage_PlainText() {
	raw, err := os.ReadFile("testdata/mixed_pdf.eml")
	if err != nil {
		fmt.Println(err)
		return
	}
	m, err := email.Parse(raw)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Print(m.PlainText())
	// Output:
	// De : Fournisseur SA <factures@fournisseur.example>
	// À : Julien Martin <julien@example.com>
	// Date : 16/09/2026 11:02 +0200
	// Objet : Votre facture de septembre
	//
	// Bonjour à tous,
	// Le fichier est joint.
	//
	// Pièces jointes :
	// - facture-2026-09.pdf (application/pdf, 93 octets)
}

func ExampleHTMLToText() {
	fmt.Println(email.HTMLToText(`<html><head><style>p{color:red}</style></head>` +
		`<body><p>Bonjour,<br>Votre relev&eacute; est pr&ecirc;t.</p>` +
		`<table><tr><td>Solde</td><td>1&nbsp;234,56 &#8364;</td></tr></table></body></html>`))
	// Output:
	// Bonjour,
	// Votre relevé est prêt.
	//
	// Solde 1 234,56 €
}
