package projectinfo

import (
	"os"
	"strings"
	"testing"
)

const sampleDoc = `# Jarvis — pipeline

## Contexte
Pipeline Go local.

## Contraintes non négociables
- Langage : Go.
- TDD strict.

## État des jalons
- **Jalon 1 — squelette CLI + étage Triage : fait.**
  - premier point
    - sous-point
- **Jalon 3 — implémentation HTTP réelle du port VLM + câblage CLI : fait
  côté code.** Reste le test end-to-end.
  - détail 3

### Setup local complet
` + "```" + `
llama-server -m x
` + "```" + `
- **Jalons 26-27 — tickets + agent d'analyse local : faits, validés.**
  - détail 26
- **Jalon 21 bis — correction après retest : fait.**
- **Correctifs du harnais : faits.**
  - détail hors jalon

## Atelier de code (cmd/codebrowser) — travail parallèle, outil de
développement

Moteurs.

## Décisions tranchées
- **Moteur : llama.cpp server**.

## Décisions en attente
- Bbox des pages scannées.
`

func TestParseProjectDoc_Sections(t *testing.T) {
	d := ParseProjectDoc(sampleDoc)
	var titles []string
	for _, s := range d.Sections {
		titles = append(titles, s.Title)
	}
	want := "Contexte|Contraintes non négociables|Atelier de code (cmd/codebrowser) — travail parallèle, outil de|Décisions tranchées|Décisions en attente"
	if got := strings.Join(titles, "|"); got != want {
		t.Errorf("sections = %q\nwant %q", got, want)
	}
	if s, ok := d.Section("Décisions tranchées"); !ok || !strings.Contains(s.Body, "llama.cpp server") {
		t.Errorf("Section(Décisions tranchées) = %+v, %v", s, ok)
	}
	if _, ok := d.Section("État des jalons"); ok {
		t.Error("l'état des jalons est découpé en jalons, pas rendu comme une section")
	}
}

func TestParseProjectDoc_Milestones(t *testing.T) {
	d := ParseProjectDoc(sampleDoc)
	if len(d.Milestones) != 5 {
		t.Fatalf("got %d milestones, want 5: %+v", len(d.Milestones), d.Milestones)
	}
	if extra := d.Milestones[4]; extra.Number != "" || extra.Title != "Correctifs du harnais" || extra.Status != "faits." || extra.Body != "- détail hors jalon" {
		t.Errorf("entrée hors jalon numéroté = %+v", extra)
	}
	m1, m3, m26, m21 := d.Milestones[0], d.Milestones[1], d.Milestones[2], d.Milestones[3]
	if m1.Number != "1" || m1.Title != "squelette CLI + étage Triage" || m1.Status != "fait." {
		t.Errorf("jalon 1 = %+v", m1)
	}
	if !strings.Contains(m1.Body, "- premier point\n  - sous-point") {
		t.Errorf("jalon 1 body (désindenté d'un niveau) = %q", m1.Body)
	}
	if m3.Number != "3" || m3.Status != "fait côté code." || !strings.HasPrefix(m3.Body, "Reste le test end-to-end.") {
		t.Errorf("jalon 3 (titre sur deux lignes) = %+v", m3)
	}
	if strings.Contains(m3.Body, "llama-server") {
		t.Errorf("une sous-section ### ne fait pas partie du jalon qui la précède : %q", m3.Body)
	}
	if m26.Number != "26-27" || m26.Title != "tickets + agent d'analyse local" || m21.Number != "21 bis" {
		t.Errorf("jalons 26-27 / 21 bis = %+v / %+v", m26, m21)
	}
	if len(d.References) != 1 || d.References[0].Title != "Setup local complet" || !strings.Contains(d.References[0].Body, "llama-server -m x") {
		t.Errorf("references = %+v", d.References)
	}
}

func TestMilestone_Matches(t *testing.T) {
	m := Milestone{Number: "26-27"}
	for _, c := range []string{"Jalons 26-27", "Jalon 26"} {
		if !m.Matches(c) {
			t.Errorf("%q ne correspond pas à 26-27", c)
		}
	}
	if (Milestone{Number: "2"}).Matches("Jalon 21") || (Milestone{Number: "21"}).Matches("") {
		t.Error("correspondance trop large")
	}
	if !(Milestone{Number: "21 bis"}).Matches("Jalon 21 bis") || (Milestone{Number: "21"}).Matches("Jalon 21 bis") {
		t.Error("21 bis doit rester distinct de 21")
	}
}

// Le vrai CLAUDE.md du dépôt : garde-fou contre un changement de forme
// qui viderait silencieusement la page.
func TestParseProjectDoc_RealFile(t *testing.T) {
	src, err := os.ReadFile("../../CLAUDE.md")
	if err != nil {
		t.Skip("CLAUDE.md absent :", err)
	}
	d := ParseProjectDoc(string(src))
	if len(d.Milestones) < 25 {
		t.Errorf("seulement %d jalons trouvés dans CLAUDE.md", len(d.Milestones))
	}
	for _, name := range []string{"Contraintes non négociables", "Décisions tranchées", "Architecture (3 étages strictement séparés, testables isolément)"} {
		if _, ok := d.Section(name); !ok {
			t.Errorf("section %q introuvable", name)
		}
	}
	for _, m := range d.Milestones {
		if m.Title == "" || m.Status == "" {
			t.Errorf("jalon %s mal découpé : %+v", m.Number, m)
		}
	}
}
