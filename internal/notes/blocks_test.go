package notes

import (
	"strings"
	"testing"
	"time"
)

// Une note d'avant les boîtes (seulement Body) se lit comme une boîte
// unique — aucune migration, la conversion a lieu à la première écriture.
func TestBlocksOf_LegacyNoteBecomesOneBox(t *testing.T) {
	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	legacy := Note{ID: "n1", Body: "- lait\n- pain", CreatedAt: at, UpdatedAt: at}
	got := BlocksOf(legacy)
	if len(got) != 1 || got[0].Text != legacy.Body || !got[0].CreatedAt.Equal(at) || !got[0].UpdatedAt.Equal(at) {
		t.Fatalf("BlocksOf(legacy) = %+v", got)
	}
	if got[0].ID == "" {
		t.Error("la boîte héritée n'a pas d'identifiant")
	}

	withBlocks := Note{ID: "n2", Body: "dérivé", Blocks: []Block{{ID: "b1", Text: "un"}, {ID: "b2", Text: "deux"}}}
	if got := BlocksOf(withBlocks); len(got) != 2 || got[1].Text != "deux" {
		t.Errorf("BlocksOf(withBlocks) = %+v", got)
	}

	if got := BlocksOf(Note{ID: "n3"}); len(got) != 0 {
		t.Errorf("BlocksOf(empty) = %+v, want nil", got)
	}
}

// Body reste le texte cherché : la concaténation des boîtes, archivées
// comprises (une boîte archivée reste trouvable par la recherche).
func TestJoinBlocks_DerivesTheSearchableBody(t *testing.T) {
	got := JoinBlocks([]Block{
		{ID: "b1", Text: "premier"},
		{ID: "b2", Text: ""},
		{ID: "b3", Text: "archivé", Archived: true},
	})
	if got != "premier\n\narchivé" {
		t.Errorf("JoinBlocks = %q", got)
	}
	if got := JoinBlocks(nil); got != "" {
		t.Errorf("JoinBlocks(nil) = %q", got)
	}
}

func TestBlockTags_UnionSorted(t *testing.T) {
	got := BlockTags([]Block{
		{Tags: []string{"urgent", "maison"}},
		{Tags: []string{"maison"}},
		{Tags: nil},
		{Tags: []string{"archive"}},
	})
	if strings.Join(got, ",") != "archive,maison,urgent" {
		t.Errorf("BlockTags = %v", got)
	}
}

func TestTaskTitleFrom_StripsMarkersAndKeepsFirstLine(t *testing.T) {
	for in, want := range map[string]string{
		"":                             "",
		"   \n\n":                      "",
		"Appeler le notaire":           "Appeler le notaire",
		"\n\n- Acheter du pain\nautre": "Acheter du pain",
		"* Relire le devis":            "Relire le devis",
		"## Courses":                   "Courses",
		"[ ] Payer EDF":                "Payer EDF",
		"**Rappeler Alice**":           "Rappeler Alice",
	} {
		if got := TaskTitleFrom(in); got != want {
			t.Errorf("TaskTitleFrom(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIndexOfBlock(t *testing.T) {
	bs := []Block{{ID: "a"}, {ID: "b"}}
	if got := indexOfBlock(bs, "b"); got != 1 {
		t.Errorf("indexOfBlock(b) = %d", got)
	}
	if got := indexOfBlock(bs, "z"); got != -1 {
		t.Errorf("indexOfBlock(z) = %d, want -1", got)
	}
}
