package main

import "testing"

func TestRoleSettings(t *testing.T) {
	full, err := roleSettings("full")
	if err != nil {
		t.Fatal(err)
	}
	if want := (role{Name: "full", TicketWork: true, RecoverJobs: true, Mail: true, Collect: true, Watch: true}); full != want {
		t.Errorf("full = %+v, want %+v", full, want)
	}
	if def, _ := roleSettings(""); def != full {
		t.Errorf("default role = %+v, want full", def)
	}

	// cloud : tout sauf le travail sur les tickets ; les documents
	// attendent les modèles servis par le Mac.
	cloud, err := roleSettings("cloud")
	if err != nil {
		t.Fatal(err)
	}
	if want := (role{Name: "cloud", RecoverJobs: true, Mail: true, Collect: true, Watch: true, WaitModels: true}); cloud != want {
		t.Errorf("cloud = %+v, want %+v", cloud, want)
	}

	// worker : seulement les tickets. Surtout pas la reprise des documents
	// au démarrage : ceux « en cours » sont traités par l'instance cloud.
	worker, err := roleSettings("worker")
	if err != nil {
		t.Fatal(err)
	}
	if want := (role{Name: "worker", TicketWork: true}); worker != want {
		t.Errorf("worker = %+v, want %+v", worker, want)
	}

	if _, err := roleSettings("nuage"); err == nil {
		t.Error("unknown role: error = nil")
	}
}

// Deux instances sur la même base : chaque travail de fond appartient à
// exactement une des deux, sinon il est fait deux fois (relève des emails,
// collecte) ou défait par l'autre (reprise au démarrage).
func TestRoleSettings_CloudAndWorkerShareNoBackgroundWork(t *testing.T) {
	cloud, _ := roleSettings("cloud")
	worker, _ := roleSettings("worker")
	for name, pair := range map[string][2]bool{
		"tickets":           {cloud.TicketWork, worker.TicketWork},
		"reprise documents": {cloud.RecoverJobs, worker.RecoverJobs},
		"emails":            {cloud.Mail, worker.Mail},
		"collecte":          {cloud.Collect, worker.Collect},
		"dossier surveillé": {cloud.Watch, worker.Watch},
	} {
		if pair[0] == pair[1] {
			t.Errorf("%s : cloud = %v et worker = %v, want exactement une des deux instances", name, pair[0], pair[1])
		}
	}
}
