package deploy

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMarker_RoundTripAndRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deploy.json")
	if _, ok, err := ReadMarker(path); ok || err != nil {
		t.Fatalf("absent marker: ok=%v err=%v", ok, err)
	}
	m := Marker{TicketID: "t1", Commit: "abc", PrevCommit: "def", Binary: "/b/jarvisapp", PrevBinary: "/b/jarvisapp.prev", At: time.Now().Truncate(time.Second)}
	if err := WriteMarker(path, m); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadMarker(path)
	if err != nil || !ok || got.TicketID != "t1" || got.PrevCommit != "def" || !got.At.Equal(m.At) {
		t.Fatalf("ReadMarker() = %+v, %v, %v", got, ok, err)
	}
	if err := RemoveMarker(path); err != nil {
		t.Fatal(err)
	}
	if err := RemoveMarker(path); err != nil {
		t.Errorf("removing an absent marker must not fail: %v", err)
	}
}

func TestMarker_CorruptIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deploy.json")
	os.WriteFile(path, []byte("{pas du json"), 0o600)
	if _, _, err := ReadMarker(path); err == nil {
		t.Error("corrupt marker must be reported, not ignored")
	}
}

func binaries(t *testing.T) (dir, bin, prev string) {
	dir = t.TempDir()
	bin, prev = filepath.Join(dir, "jarvisapp"), filepath.Join(dir, "jarvisapp.prev")
	os.WriteFile(bin, []byte("nouvelle"), 0o755)
	os.WriteFile(prev, []byte("ancienne"), 0o755)
	return
}

// Le lanceur voit l'application s'arrêter pendant un déploiement non
// confirmé : l'ancienne version reprend sa place.
func TestRollbackBinary_RestoresPreviousVersion(t *testing.T) {
	dir, bin, prev := binaries(t)
	marker := filepath.Join(dir, "deploy.json")
	WriteMarker(marker, Marker{TicketID: "t1", Binary: bin, PrevBinary: prev})

	rolled, err := RollbackBinary(marker, "arrêt au démarrage (exit 2)")
	if err != nil || !rolled {
		t.Fatalf("RollbackBinary() = %v, %v", rolled, err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "ancienne" {
		t.Errorf("binary = %q, want the previous version back", b)
	}
	if b, _ := os.ReadFile(bin + ".failed"); string(b) != "nouvelle" {
		t.Errorf("failed version kept as .failed = %q", b)
	}
	m, _, _ := ReadMarker(marker)
	if !m.RolledBack || m.Reason != "arrêt au démarrage (exit 2)" {
		t.Errorf("marker = %+v, want rolled back with the reason (the old version finishes the job)", m)
	}
	// Une seule fois : l'ancienne version qui tomberait à son tour ne
	// déclenche pas un second échange.
	if rolled, _ := RollbackBinary(marker, "encore"); rolled {
		t.Error("a marker already rolled back must not roll back again")
	}
}

func TestRollbackBinary_NothingToDo(t *testing.T) {
	dir, bin, _ := binaries(t)
	if rolled, err := RollbackBinary(filepath.Join(dir, "absent.json"), "x"); rolled || err != nil {
		t.Errorf("no marker: %v, %v", rolled, err)
	}
	marker := filepath.Join(dir, "deploy.json")
	WriteMarker(marker, Marker{TicketID: "t1", Binary: bin, PrevBinary: filepath.Join(dir, "introuvable")})
	if rolled, err := RollbackBinary(marker, "x"); rolled || err == nil {
		t.Errorf("missing previous binary: rolled=%v err=%v, want an explicit error", rolled, err)
	}
}
