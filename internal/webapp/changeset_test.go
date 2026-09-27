package webapp

import (
	"context"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/changes"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

type docCS struct {
	base    Store
	staged  *changes.FakeChangesetStore
	moi     *JobManager
	commit  *changes.Committer
	ctxMoi  context.Context
	ctxAutr context.Context
	scopeM  tenancy.Scope
	scopeA  tenancy.Scope
}

func newDocCS(t *testing.T) *docCS {
	t.Helper()
	now := time.Date(2026, 9, 27, 17, 0, 0, 0, time.UTC)
	base := NewFakeStore()
	staged := changes.NewFakeChangesetStore()
	stager := &changes.Stager{Store: staged, Now: func() time.Time { return now }}

	scopeM := tenancy.Scope{Env: tenancy.Local, User: "moi", Session: "s-moi", Role: tenancy.RoleMember}
	scopeA := tenancy.Scope{Env: tenancy.Local, User: "autre", Session: "s-autre", Role: tenancy.RoleMember}

	m := NewJobManager(base, &fakeRunner{})
	m.WorkDir = t.TempDir()
	m.Staged, m.Stager, m.DefaultScope = staged, stager, scopeM

	return &docCS{
		base: base, staged: staged, moi: m,
		commit:  &changes.Committer{Staged: staged, Appliers: []changes.Applier{Applier{Base: base}}},
		ctxMoi:  tenancy.WithScope(context.Background(), scopeM),
		ctxAutr: tenancy.WithScope(context.Background(), scopeA),
		scopeM:  scopeM, scopeA: scopeA,
	}
}

func (f *docCS) seedDoc(t *testing.T, id, filename string) Job {
	t.Helper()
	job, err := f.base.For(f.scopeA).Create(context.Background(), Job{
		ID: id, Filename: filename, Status: StatusDone, CreatedAt: time.Now(), Comment: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

// Retaguer un document reste chez moi jusqu'au commit.
func TestDocChangeset_TagsEnAttente(t *testing.T) {
	f := newDocCS(t)
	f.seedDoc(t, "d1", "facture.pdf")

	if err := f.moi.SetTags(f.ctxMoi, "d1", []string{"urgent"}); err != nil {
		t.Fatalf("SetTags : %v", err)
	}
	mine, ok, err := f.moi.Get(f.ctxMoi, "d1")
	if err != nil || !ok {
		t.Fatalf("Get (moi) = (%v, %v)", ok, err)
	}
	if len(mine.Tags) != 1 || mine.Tags[0] != "urgent" {
		t.Errorf("mes tags = %v, veut [urgent]", mine.Tags)
	}
	theirs, _, _ := f.base.For(f.scopeA).Get(f.ctxAutr, "d1")
	if len(theirs.Tags) != 0 {
		t.Errorf("tags vus par l'autre = %v, veut aucun avant le commit", theirs.Tags)
	}
	// Et la recherche suit mon état.
	found, err := f.moi.List(f.ctxMoi, ListQuery{Search: "urgent"})
	if err != nil || len(found) != 1 {
		t.Errorf("recherche sur mon tag = %d (err=%v), veut 1", len(found), err)
	}
	if theirsList, _ := f.base.For(f.scopeA).List(f.ctxAutr, ListQuery{Search: "urgent"}); len(theirsList) != 0 {
		t.Errorf("l'autre trouve %d documents par mon tag en attente, veut 0", len(theirsList))
	}

	res, err := f.commit.Commit(f.ctxMoi, f.scopeM)
	if err != nil {
		t.Fatalf("Commit : %v", err)
	}
	if res.Applied != 1 || len(res.Conflicts) != 0 {
		t.Fatalf("commit = %+v", res)
	}
	theirs, _, _ = f.base.For(f.scopeA).Get(f.ctxAutr, "d1")
	if len(theirs.Tags) != 1 || theirs.Tags[0] != "urgent" {
		t.Errorf("après commit, tags = %v", theirs.Tags)
	}
}

// Tags et commentaire sont deux champs : deux personnes, chacune le sien,
// aucun conflit.
func TestDocChangeset_ChampsDifferentsNeConflictentPas(t *testing.T) {
	f := newDocCS(t)
	f.seedDoc(t, "d1", "facture.pdf")

	if err := f.moi.SetTags(f.ctxMoi, "d1", []string{"urgent"}); err != nil {
		t.Fatal(err)
	}
	// L'autre commente directement en base.
	if err := f.base.For(f.scopeA).SetComment(f.ctxAutr, "d1", "vu avec le client"); err != nil {
		t.Fatal(err)
	}
	res, err := f.commit.Commit(f.ctxMoi, f.scopeM)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("conflits = %+v, veut aucun", res.Conflicts)
	}
	got, _, _ := f.base.For(f.scopeA).Get(f.ctxAutr, "d1")
	if len(got.Tags) != 1 || got.Comment != "vu avec le client" {
		t.Errorf("document = tags %v, commentaire %q : les deux doivent survivre", got.Tags, got.Comment)
	}
}

func TestDocChangeset_MemeChampConflit(t *testing.T) {
	f := newDocCS(t)
	f.seedDoc(t, "d1", "facture.pdf")
	if err := f.moi.SetComment(f.ctxMoi, "d1", "mon commentaire"); err != nil {
		t.Fatal(err)
	}
	if err := f.base.For(f.scopeA).SetComment(f.ctxAutr, "d1", "son commentaire"); err != nil {
		t.Fatal(err)
	}
	res, err := f.commit.Commit(f.ctxMoi, f.scopeM)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 1 || res.Applied != 0 {
		t.Fatalf("commit = %+v, veut 1 conflit et 0 appliqué", res)
	}
	if res.Conflicts[0].Current != `"son commentaire"` {
		t.Errorf("valeur courante = %q", res.Conflicts[0].Current)
	}
	got, _, _ := f.base.For(f.scopeA).Get(f.ctxAutr, "d1")
	if got.Comment != "son commentaire" {
		t.Errorf("commentaire = %q : un conflit ne doit rien écraser", got.Comment)
	}
}

// La suppression est une donnée : en attente, elle me cache le document et
// le laisse aux autres ; au commit, elle emporte aussi ses fichiers.
func TestDocChangeset_SuppressionEnAttente(t *testing.T) {
	f := newDocCS(t)
	if _, err := f.base.For(f.scopeA).Create(context.Background(), Job{
		ID: "d1", Filename: "facture.pdf", Status: StatusDone, CreatedAt: time.Now(), Content: []byte("%PDF"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.moi.Delete(f.ctxMoi, "d1"); err != nil {
		t.Fatalf("Delete : %v", err)
	}
	if _, ok, _ := f.moi.Get(f.ctxMoi, "d1"); ok {
		t.Error("supprimé en attente, il ne doit plus m'apparaître")
	}
	if _, ok, _ := f.base.For(f.scopeA).Get(f.ctxAutr, "d1"); !ok {
		t.Error("il doit rester pour les autres jusqu'au commit")
	}
	if _, err := f.commit.Commit(f.ctxMoi, f.scopeM); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := f.base.For(f.scopeA).Get(f.ctxAutr, "d1"); ok {
		t.Error("après commit, il doit avoir disparu pour tous")
	}
	if data, ok, _ := f.base.For(f.scopeA).ReadFile(f.ctxAutr, "d1", FileOriginal); ok || data != nil {
		t.Error("le fichier doit être purgé au commit, pas avant")
	}
}

// Les écritures de la machine ne passent jamais par le changeset : un
// commit ne doit pas pouvoir retenir un résultat d'extraction.
func TestDocChangeset_EcrituresMachineDirectes(t *testing.T) {
	f := newDocCS(t)
	f.seedDoc(t, "d1", "facture.pdf")
	// Portée de fond : c'est ce que le traitement utilise.
	bg := tenancy.WithScope(context.Background(), tenancy.System(tenancy.Local))
	if err := f.moi.db(bg).Update(bg, Job{ID: "d1", Status: StatusFailed, Err: "boum"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := f.base.For(f.scopeA).Get(f.ctxAutr, "d1")
	if got.Status != StatusFailed {
		t.Errorf("statut = %q : une écriture de la machine doit être immédiate", got.Status)
	}
	if pending, _ := f.staged.Pending(f.ctxMoi, tenancy.Local, "moi"); len(pending) != 0 {
		t.Errorf("%d opérations en attente, veut 0", len(pending))
	}
}

// Même avec des modifications en attente, la liste reste bornée.
func TestDocChangeset_ListeBornee(t *testing.T) {
	f := newDocCS(t)
	for i := 0; i < 5; i++ {
		f.seedDoc(t, string(rune('a'+i)), "f.pdf")
	}
	if err := f.moi.SetTags(f.ctxMoi, "a", []string{"urgent"}); err != nil {
		t.Fatal(err)
	}
	got, err := f.moi.List(f.ctxMoi, ListQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("%d documents, veut 2 : la limite doit tenir malgré le filtrage en mémoire", len(got))
	}
}
