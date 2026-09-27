package notes

import (
	"context"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/changes"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

type csFixture struct {
	base    Store
	staged  *changes.FakeChangesetStore
	moi     *Service // édite à travers l'overlay
	autre   *Service // écrit directement dans la base
	commit  *changes.Committer
	ctxMoi  context.Context
	ctxAutr context.Context
	now     time.Time
}

func newCS(t *testing.T) *csFixture {
	t.Helper()
	now := time.Date(2026, 9, 27, 16, 0, 0, 0, time.UTC)
	base := NewFakeStore()
	staged := changes.NewFakeChangesetStore()
	clock := func() time.Time { return now }

	scopeMoi := tenancy.Scope{Env: tenancy.Local, User: "moi", Session: "s-moi", Role: tenancy.RoleMember}
	scopeAutre := tenancy.Scope{Env: tenancy.Local, User: "autre", Session: "s-autre", Role: tenancy.RoleMember}

	f := &csFixture{
		base:    base,
		staged:  staged,
		commit:  &changes.Committer{Staged: staged, Appliers: []changes.Applier{Applier{Base: base}}},
		ctxMoi:  tenancy.WithScope(context.Background(), scopeMoi),
		ctxAutr: tenancy.WithScope(context.Background(), scopeAutre),
		now:     now,
	}
	stager := &changes.Stager{Store: staged, Now: clock}
	// Câblé comme l'application : le Service reçoit la base et son
	// changeset, et c'est db(ctx) qui décide. Injecter l'overlay dans Store
	// testerait l'overlay sans tester le choix — un chemin qui écrirait
	// directement dans la base passerait inaperçu.
	f.moi = &Service{Store: base, Now: clock, Staged: staged, Stager: stager, DefaultScope: scopeMoi}
	f.autre = &Service{Store: base, Now: clock, DefaultScope: scopeAutre}
	return f
}

// Le cœur du jalon : ma modification existe pour moi, pas pour les autres,
// jusqu'à ce que je la commite.
func TestChangeset_InvisibleJusquAuCommit(t *testing.T) {
	f := newCS(t)

	// Une note existe dans la base, écrite par quelqu'un d'autre.
	n, err := f.autre.NewNote(f.ctxAutr, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.autre.SaveNote(f.ctxAutr, n.ID, "Courses", "", false); err != nil {
		t.Fatal(err)
	}
	setBody(t, f.autre, f.ctxAutr, n.ID, "du lait")

	// Je la modifie.
	if _, err := f.moi.SaveNote(f.ctxMoi, n.ID, "Courses de la semaine", "", false); err != nil {
		t.Fatal(err)
	}
	setBody(t, f.moi, f.ctxMoi, n.ID, "du lait")

	// Moi, je vois ma version.
	mine, ok, err := f.moi.DB(f.ctxMoi).GetNote(f.ctxMoi, n.ID)
	if err != nil || !ok {
		t.Fatalf("GetNote (moi) = (%v, %v)", ok, err)
	}
	if mine.Title != "Courses de la semaine" {
		t.Errorf("je vois %q, veut ma modification", mine.Title)
	}
	// Les autres voient la base.
	theirs, ok, err := f.base.For(scopeOf(f.ctxAutr)).GetNote(f.ctxAutr, n.ID)
	if err != nil || !ok {
		t.Fatalf("GetNote (autre) = (%v, %v)", ok, err)
	}
	if theirs.Title != "Courses" {
		t.Errorf("l'autre voit %q : une modification en attente doit rester invisible", theirs.Title)
	}

	// Je commite.
	res, err := f.commit.Commit(f.ctxMoi, scopeOf(f.ctxMoi))
	if err != nil {
		t.Fatalf("Commit : %v", err)
	}
	if res.Applied == 0 || len(res.Conflicts) != 0 {
		t.Fatalf("commit = %+v, veut appliqué sans conflit", res)
	}
	// Maintenant tout le monde la voit, et mon changeset est vide.
	theirs, _, _ = f.base.For(scopeOf(f.ctxAutr)).GetNote(f.ctxAutr, n.ID)
	if theirs.Title != "Courses de la semaine" {
		t.Errorf("après commit, l'autre voit %q", theirs.Title)
	}
	if pending, _ := f.staged.Pending(f.ctxMoi, tenancy.Local, "moi"); len(pending) != 0 {
		t.Errorf("%d opérations restent en attente après un commit réussi", len(pending))
	}
}

// Une note créée en attente n'existe que pour moi, y compris dans les
// listes et la recherche.
func TestChangeset_CreationEnAttente(t *testing.T) {
	f := newCS(t)
	n, err := f.moi.NewNote(f.ctxMoi, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.moi.SaveNote(f.ctxMoi, n.ID, "Secret", "perso", false); err != nil {
		t.Fatal(err)
	}
	setBody(t, f.moi, f.ctxMoi, n.ID, "rien")

	mine, err := f.moi.DB(f.ctxMoi).ListNotes(f.ctxMoi, NoteQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 || mine[0].Title != "Secret" {
		t.Fatalf("mes notes = %+v, veut la note en attente", mine)
	}
	// Retrouvée par son nouveau titre : la requête est appliquée après mes
	// opérations, pas avant (sans quoi Mongo filtrerait sur l'ancien état).
	found, err := f.moi.DB(f.ctxMoi).ListNotes(f.ctxMoi, NoteQuery{Search: "secret"})
	if err != nil || len(found) != 1 {
		t.Errorf("recherche = %d notes (err=%v), veut 1", len(found), err)
	}
	if byTag, _ := f.moi.DB(f.ctxMoi).ListNotes(f.ctxMoi, NoteQuery{Tag: "perso"}); len(byTag) != 1 {
		t.Errorf("filtre par tag = %d, veut 1", len(byTag))
	}
	// Invisible des autres.
	theirs, err := f.base.For(scopeOf(f.ctxAutr)).ListNotes(f.ctxAutr, NoteQuery{})
	if err != nil || len(theirs) != 0 {
		t.Errorf("notes de l'autre = %d (err=%v), veut 0", len(theirs), err)
	}

	if _, err := f.commit.Commit(f.ctxMoi, scopeOf(f.ctxMoi)); err != nil {
		t.Fatal(err)
	}
	theirs, _ = f.base.For(scopeOf(f.ctxAutr)).ListNotes(f.ctxAutr, NoteQuery{})
	if len(theirs) != 1 || theirs[0].Title != "Secret" {
		t.Errorf("après commit, notes de l'autre = %+v", theirs)
	}
}

// Une note renommée en attente ne doit plus sortir sur son ancien nom.
func TestChangeset_RechercheSuitLEtatEnAttente(t *testing.T) {
	f := newCS(t)
	n, _ := f.autre.NewNote(f.ctxAutr, "")
	if _, err := f.autre.SaveNote(f.ctxAutr, n.ID, "Ancien titre", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.moi.SaveNote(f.ctxMoi, n.ID, "Nouveau titre", "", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.moi.DB(f.ctxMoi).ListNotes(f.ctxMoi, NoteQuery{Search: "Nouveau"}); len(got) != 1 {
		t.Errorf("recherche sur le nouveau titre = %d, veut 1", len(got))
	}
	if got, _ := f.moi.DB(f.ctxMoi).ListNotes(f.ctxMoi, NoteQuery{Search: "Ancien"}); len(got) != 0 {
		t.Errorf("recherche sur l'ancien titre = %d, veut 0", len(got))
	}
}

// Deux personnes, deux champs différents de la même note : les deux
// commitent, personne ne perd rien. C'est la granularité qui l'obtient.
func TestChangeset_ChampsDifferentsNeConflictentPas(t *testing.T) {
	f := newCS(t)
	n, _ := f.autre.NewNote(f.ctxAutr, "")
	if _, err := f.autre.SaveNote(f.ctxAutr, n.ID, "Courses", "", false); err != nil {
		t.Fatal(err)
	}
	setBody(t, f.autre, f.ctxAutr, n.ID, "du lait")

	// Je change le titre (en attente).
	if _, err := f.moi.SaveNote(f.ctxMoi, n.ID, "Courses du samedi", "", false); err != nil {
		t.Fatal(err)
	}
	setBody(t, f.moi, f.ctxMoi, n.ID, "du lait")
	// L'autre change le corps, directement dans la base.
	if _, err := f.autre.SaveNote(f.ctxAutr, n.ID, "Courses", "", false); err != nil {
		t.Fatal(err)
	}
	setBody(t, f.autre, f.ctxAutr, n.ID, "du lait et du pain")

	res, err := f.commit.Commit(f.ctxMoi, scopeOf(f.ctxMoi))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("conflits = %+v, veut aucun : les champs touchés sont disjoints", res.Conflicts)
	}
	got, _, _ := f.base.For(scopeOf(f.ctxMoi)).GetNote(f.ctxMoi, n.ID)
	if got.Title != "Courses du samedi" {
		t.Errorf("titre = %q, veut le mien", got.Title)
	}
	if got.Body != "du lait et du pain" {
		t.Errorf("corps = %q, veut celui de l'autre : mon commit ne doit pas l'écraser", got.Body)
	}
}

// Le même champ, par deux personnes : conflit signalé, rien d'écrasé, et
// l'opération reste en attente pour que je tranche.
func TestChangeset_MemeChampConflit(t *testing.T) {
	f := newCS(t)
	n, _ := f.autre.NewNote(f.ctxAutr, "")
	if _, err := f.autre.SaveNote(f.ctxAutr, n.ID, "Courses", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.moi.SaveNote(f.ctxMoi, n.ID, "Mon titre", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.autre.SaveNote(f.ctxAutr, n.ID, "Son titre", "", false); err != nil {
		t.Fatal(err)
	}

	res, err := f.commit.Commit(f.ctxMoi, scopeOf(f.ctxMoi))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 1 {
		t.Fatalf("conflits = %+v, veut 1", res.Conflicts)
	}
	c := res.Conflicts[0]
	if c.Op.Field != "title" {
		t.Errorf("champ en conflit = %q", c.Op.Field)
	}
	if c.Current != `"Son titre"` {
		t.Errorf("valeur courante = %q, veut celle de l'autre", c.Current)
	}
	if c.Reason == "" {
		t.Error("un conflit doit dire pourquoi")
	}
	got, _, _ := f.base.For(scopeOf(f.ctxMoi)).GetNote(f.ctxMoi, n.ID)
	if got.Title != "Son titre" {
		t.Errorf("titre = %q : un conflit ne doit rien écraser", got.Title)
	}
	// L'opération reste en attente : à moi de décider.
	if pending, _ := f.staged.Pending(f.ctxMoi, tenancy.Local, "moi"); len(pending) != 1 {
		t.Errorf("%d opérations en attente, veut 1 (le conflit reste à trancher)", len(pending))
	}
}

// Supprimer en attente : la note disparaît pour moi, reste pour les autres.
func TestChangeset_SuppressionEnAttente(t *testing.T) {
	f := newCS(t)
	n, _ := f.autre.NewNote(f.ctxAutr, "")
	if _, err := f.autre.SaveNote(f.ctxAutr, n.ID, "À jeter", "", false); err != nil {
		t.Fatal(err)
	}
	if err := f.moi.DeleteNote(f.ctxMoi, n.ID); err != nil {
		t.Fatalf("DeleteNote : %v", err)
	}
	if _, ok, _ := f.moi.DB(f.ctxMoi).GetNote(f.ctxMoi, n.ID); ok {
		t.Error("supprimée en attente, elle ne doit plus m'apparaître")
	}
	if _, ok, _ := f.base.For(scopeOf(f.ctxAutr)).GetNote(f.ctxAutr, n.ID); !ok {
		t.Error("elle doit rester pour les autres jusqu'au commit")
	}
	if _, err := f.commit.Commit(f.ctxMoi, scopeOf(f.ctxMoi)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := f.base.For(scopeOf(f.ctxAutr)).GetNote(f.ctxAutr, n.ID); ok {
		t.Error("après commit, elle doit avoir disparu pour tous")
	}
}

// Abandonner : le changeset se vide, la base n'a jamais bougé.
func TestChangeset_Abandon(t *testing.T) {
	f := newCS(t)
	n, _ := f.autre.NewNote(f.ctxAutr, "")
	if _, err := f.autre.SaveNote(f.ctxAutr, n.ID, "Courses", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.moi.SaveNote(f.ctxMoi, n.ID, "Mon titre", "", false); err != nil {
		t.Fatal(err)
	}
	if err := f.staged.DropAll(f.ctxMoi, tenancy.Local, "moi"); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := f.moi.DB(f.ctxMoi).GetNote(f.ctxMoi, n.ID); !ok || got.Title != "Courses" {
		t.Errorf("après abandon je vois %q, veut l'état de la base", got.Title)
	}
}

// Les tâches suivent le même cycle.
func TestChangeset_Taches(t *testing.T) {
	f := newCS(t)
	task, err := f.moi.AddTask(f.ctxMoi, "Payer la facture demain", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if mine, _ := f.moi.DB(f.ctxMoi).ListTasks(f.ctxMoi, TaskQuery{}); len(mine) != 1 {
		t.Errorf("mes tâches = %d, veut 1", len(mine))
	}
	if theirs, _ := f.base.For(scopeOf(f.ctxAutr)).ListTasks(f.ctxAutr, TaskQuery{}); len(theirs) != 0 {
		t.Errorf("tâches de l'autre = %d, veut 0", len(theirs))
	}
	if _, err := f.moi.ToggleTask(f.ctxMoi, task.ID); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := f.moi.DB(f.ctxMoi).GetTask(f.ctxMoi, task.ID); !got.Done {
		t.Error("ma tâche doit être cochée de mon point de vue")
	}
	if _, err := f.commit.Commit(f.ctxMoi, scopeOf(f.ctxMoi)); err != nil {
		t.Fatal(err)
	}
	got, ok, _ := f.base.For(scopeOf(f.ctxAutr)).GetTask(f.ctxAutr, task.ID)
	if !ok || !got.Done || got.Title != "Payer la facture" {
		t.Errorf("après commit, la tâche = %+v", got)
	}
}

// Deux modifications successives du même champ : la dernière gagne, et une
// seule valeur arrive en base.
func TestChangeset_DeuxModifsSuccessives(t *testing.T) {
	f := newCS(t)
	n, _ := f.autre.NewNote(f.ctxAutr, "")
	if _, err := f.autre.SaveNote(f.ctxAutr, n.ID, "A", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.moi.SaveNote(f.ctxMoi, n.ID, "B", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.moi.SaveNote(f.ctxMoi, n.ID, "C", "", false); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := f.moi.DB(f.ctxMoi).GetNote(f.ctxMoi, n.ID); got.Title != "C" {
		t.Errorf("je vois %q, veut ma dernière valeur", got.Title)
	}
	if _, err := f.commit.Commit(f.ctxMoi, scopeOf(f.ctxMoi)); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := f.base.For(scopeOf(f.ctxMoi)).GetNote(f.ctxMoi, n.ID); got.Title != "C" {
		t.Errorf("en base = %q, veut C", got.Title)
	}
}

// Le texte d'une note vit dans ses boîtes, et c'est un humain qui l'écrit :
// il passe donc par le changeset comme n'importe quel autre champ. Sans
// cela, une modification de texte serait immédiatement visible des autres
// alors qu'un renommage attendrait le commit — deux règles pour la même
// note.
func TestChangeset_TexteDUneBoiteAttendLeCommit(t *testing.T) {
	f := newCS(t)
	n, err := f.autre.NewNote(f.ctxAutr, "")
	if err != nil {
		t.Fatal(err)
	}
	setBody(t, f.autre, f.ctxAutr, n.ID, "avant")

	// Je réécris le texte : en attente.
	setBody(t, f.moi, f.ctxMoi, n.ID, "après")

	base := f.base.For(scopeOf(f.ctxAutr))
	theirs, _, _ := base.GetNote(f.ctxAutr, n.ID)
	if theirs.Body != "avant" {
		t.Errorf("l'autre voit %q avant mon commit, veut %q", theirs.Body, "avant")
	}
	mine, _, _ := f.moi.DB(f.ctxMoi).GetNote(f.ctxMoi, n.ID)
	if mine.Body != "après" {
		t.Errorf("je vois %q, veut ma version en attente", mine.Body)
	}
	if len(BlocksOf(mine)) != 1 || BlocksOf(mine)[0].Text != "après" {
		t.Errorf("mes boîtes en attente = %+v", BlocksOf(mine))
	}

	res, err := f.commit.Commit(f.ctxMoi, scopeOf(f.ctxMoi))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("conflits = %+v, veut aucun", res.Conflicts)
	}
	after, _, _ := base.GetNote(f.ctxAutr, n.ID)
	if after.Body != "après" {
		t.Errorf("après commit, corps = %q", after.Body)
	}
	// Les boîtes, pas seulement le texte dérivé : sinon la page afficherait
	// encore l'ancien texte, qui est celui qu'elle lit vraiment.
	if bs := BlocksOf(after); len(bs) != 1 || bs[0].Text != "après" {
		t.Errorf("après commit, boîtes = %+v", bs)
	}
}
