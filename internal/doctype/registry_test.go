package doctype

import (
	"reflect"
	"testing"
)

type dummyDoc struct {
	Name string `json:"name"`
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()

	if err := Register[dummyDoc](r, "dummy", "Un type de test"); err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}

	got, ok := r.Get("dummy")
	if !ok {
		t.Fatal("Get() ok = false, want true")
	}
	if got.Name != "dummy" || got.Description != "Un type de test" {
		t.Errorf("Get() = %+v, want Name=dummy Description='Un type de test'", got)
	}
	if got.Type != reflect.TypeOf(dummyDoc{}) {
		t.Errorf("Get().Type = %v, want %v", got.Type, reflect.TypeOf(dummyDoc{}))
	}
}

func TestRegistry_Get_UnknownName_ReturnsFalse(t *testing.T) {
	r := NewRegistry()

	_, ok := r.Get("does-not-exist")
	if ok {
		t.Error("Get() ok = true, want false for an unregistered name")
	}
}

func TestRegistry_Register_DuplicateName_ReturnsError(t *testing.T) {
	r := NewRegistry()
	if err := Register[dummyDoc](r, "dummy", "premier"); err != nil {
		t.Fatalf("first Register() error = %v, want nil", err)
	}

	err := Register[dummyDoc](r, "dummy", "second")
	if err == nil {
		t.Fatal("second Register() error = nil, want non-nil for a duplicate name")
	}
}

func TestRegistry_Names_SortedAlphabetically(t *testing.T) {
	r := NewRegistry()
	_ = Register[dummyDoc](r, "zzz", "")
	_ = Register[dummyDoc](r, "aaa", "")
	_ = Register[dummyDoc](r, "mmm", "")

	got := r.Names()
	want := []string{"aaa", "mmm", "zzz"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
}

func TestRegistry_Registrations_SortedAlphabeticallyWithDescriptions(t *testing.T) {
	r := NewRegistry()
	_ = Register[dummyDoc](r, "zzz", "description z")
	_ = Register[dummyDoc](r, "aaa", "description a")

	got := r.Registrations()
	if len(got) != 2 {
		t.Fatalf("len(Registrations()) = %d, want 2", len(got))
	}
	if got[0].Name != "aaa" || got[0].Description != "description a" {
		t.Errorf("Registrations()[0] = %+v, want Name=aaa Description='description a'", got[0])
	}
	if got[1].Name != "zzz" || got[1].Description != "description z" {
		t.Errorf("Registrations()[1] = %+v, want Name=zzz Description='description z'", got[1])
	}
}
