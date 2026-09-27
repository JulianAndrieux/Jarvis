package webapp

import "github.com/JulianAndrieux/Jarvis/internal/pipeline"

// anyStage : l'étape d'avancement d'un job quelconque de l'environnement
// de cette vue. Sert à un test qui observe l'avancement pendant la
// conversion d'un fichier.
//
// Remplace une lecture directe de la map interne, qui se faisait sans
// prendre le verrou alors que le JobManager y écrivait en parallèle — une
// vraie course, restée invisible parce que rien ne la déclenchait au bon
// moment.
func (s *FakeStore) anyStage() pipeline.Stage {
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	prefix := string(s.scope.Env) + "\x00"
	var stage pipeline.Stage
	for k, j := range s.shared.jobs {
		if len(k) < len(prefix) || k[:len(prefix)] != prefix {
			continue
		}
		if j.Progress != nil {
			stage = j.Progress.Stage
		}
	}
	return stage
}
