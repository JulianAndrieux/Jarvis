package notes

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

func conflictService(t *testing.T) (*Service, context.Context) {
	t.Helper()
	at := time.Date(2026, 9, 27, 15, 0, 0, 0, time.UTC)
	svc := &Service{Store: NewFakeStore(), Now: func() time.Time { return at }}
	ctx := tenancy.WithScope(context.Background(), tenancy.Scope{
		Env: tenancy.Local, User: "u-1", Session: "s-1", Role: tenancy.RoleMember,
	})
	return svc, ctx
}

// Une écriture de la version lue passe ; la même écriture rejouée sur une
// version périmée est refusée. C'est ce qui empêche la perte silencieuse
// d'une modification faite entre-temps par quelqu'un d'autre.
func TestUpdateNote_RefuseUneVersionPerimee(t *testing.T) {
	svc, ctx := conflictService(t)
	n, err := svc.NewNote(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	stale, ok, err := svc.Store.For(scopeOf(ctx)).GetNote(ctx, n.ID)
	if err != nil || !ok {
		t.Fatalf("GetNote = (%v, %v)", ok, err)
	}
	if stale.Version == 0 {
		t.Fatal("une note enregistrée doit porter une version")
	}

	// Quelqu'un modifie la note.
	if _, err := svc.SaveNote(ctx, n.ID, "Courses", "du lait", "", false); err != nil {
		t.Fatal(err)
	}

	// Mon écriture, bâtie sur ce que j'avais lu avant, est refusée.
	stale.Body = "autre chose"
	err = svc.Store.For(scopeOf(ctx)).UpdateNote(ctx, stale)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, veut ErrConflict", err)
	}
	// Et la modification de l'autre est intacte.
	got, _, _ := svc.Store.For(scopeOf(ctx)).GetNote(ctx, n.ID)
	if got.Body != "du lait" {
		t.Errorf("corps = %q : une écriture refusée ne doit rien avoir changé", got.Body)
	}
}

// La version monte à chaque écriture : c'est l'ancrage dont le rebase du
// changeset (jalon 48) a besoin.
func TestVersion_MonteAChaqueEcriture(t *testing.T) {
	svc, ctx := conflictService(t)
	st := svc.Store.For(scopeOf(ctx))
	n, err := svc.NewNote(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := st.GetNote(ctx, n.ID)
	if _, err := svc.SaveNote(ctx, n.ID, "A", "", "", false); err != nil {
		t.Fatal(err)
	}
	second, _, _ := st.GetNote(ctx, n.ID)
	if second.Version <= first.Version {
		t.Errorf("version %d puis %d : elle doit monter", first.Version, second.Version)
	}
}

// SaveNote rend un conflit exploitable : l'état courant, pour que
// l'interface puisse montrer « quelqu'un a modifié ceci pendant ta saisie »
// au lieu d'écraser en silence.
func TestSaveNote_ConflitRendLEtatCourant(t *testing.T) {
	svc, ctx := conflictService(t)
	n, err := svc.NewNote(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveNote(ctx, n.ID, "Titre de l'autre", "corps de l'autre", "", false); err != nil {
		t.Fatal(err)
	}
	// Je soumets une version périmée (celle d'avant l'écriture de l'autre).
	_, err = svc.SaveNoteVersion(ctx, n.ID, n.Version, "Mon titre", "mon corps", "", false)
	var c *Conflict
	if !errors.As(err, &c) {
		t.Fatalf("err = %v, veut un *Conflict", err)
	}
	if c.Current.Title != "Titre de l'autre" {
		t.Errorf("état courant = %q, veut celui de l'autre", c.Current.Title)
	}
	if c.Current.Body != "corps de l'autre" {
		t.Errorf("corps courant = %q", c.Current.Body)
	}
}

// Deux écrivains simultanés sur la même note : l'un passe, l'autre est
// refusé, et aucune modification ne disparaît en silence. C'est la classe
// de bug qui a coûté quatre correctifs à ce projet avec un seul
// utilisateur (jalons 15, 23, 26-27, 39).
func TestDeuxEcrivainsSimultanes_AucunePerteSilencieuse(t *testing.T) {
	svc, ctx := conflictService(t)
	st := svc.Store.For(scopeOf(ctx))
	n, err := svc.NewNote(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	read, _, _ := st.GetNote(ctx, n.ID)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, body := range []string{"écrivain A", "écrivain B"} {
		wg.Add(1)
		go func(i int, body string) {
			defer wg.Done()
			mine := read
			mine.Body = body
			errs[i] = st.UpdateNote(ctx, mine)
		}(i, body)
	}
	wg.Wait()

	okCount, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			okCount++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("erreur inattendue : %v", err)
		}
	}
	if okCount != 1 || conflicts != 1 {
		t.Errorf("%d succès et %d conflits, veut 1 et 1 : deux écritures sur la même version ne peuvent pas toutes deux réussir", okCount, conflicts)
	}
	got, _, _ := st.GetNote(ctx, n.ID)
	if got.Body != "écrivain A" && got.Body != "écrivain B" {
		t.Errorf("corps = %q : l'écriture retenue doit être l'une des deux, entière", got.Body)
	}
}

func TestUpdateTask_RefuseUneVersionPerimee(t *testing.T) {
	svc, ctx := conflictService(t)
	st := svc.Store.For(scopeOf(ctx))
	task, err := svc.AddTask(ctx, "Payer la facture", "", "")
	if err != nil {
		t.Fatal(err)
	}
	stale, _, _ := st.GetTask(ctx, task.ID)
	if _, err := svc.ToggleTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	stale.Title = "autre titre"
	if err := st.UpdateTask(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, veut ErrConflict", err)
	}
	if got, _, _ := st.GetTask(ctx, task.ID); !got.Done {
		t.Error("une écriture refusée ne doit pas avoir décoché la tâche")
	}
}

// scopeOf : la portée du contexte (raccourci de test).
func scopeOf(ctx context.Context) tenancy.Scope {
	s, _ := tenancy.FromContext(ctx)
	return s
}
