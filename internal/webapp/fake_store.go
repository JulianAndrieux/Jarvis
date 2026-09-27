package webapp

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
	"github.com/JulianAndrieux/Jarvis/internal/tenancy"
)

// FakeStore est une implémentation de test de Store : en mémoire, aucune
// base requise. Les jobs sont perdus à la fin du process — attendu pour
// un test.
//
// Comme MongoStore, elle est cloisonnée par environnement : les vues
// rendues par For partagent les mêmes données mais ne voient chacune que
// son environnement. C'est indispensable pour que le contrat d'isolation
// (isolation_test.go) prouve quelque chose sur la fake, et pas seulement
// contre Atlas — la leçon du jalon 23 étant qu'une fake qui ne se comporte
// pas comme le vrai store laisse passer les bugs.
type FakeStore struct {
	shared *fakeShared
	scope  tenancy.Scope
}

// fakeShared est la « base » partagée par toutes les vues scopées.
type fakeShared struct {
	mu    sync.Mutex
	jobs  map[string]Job                 // clé : envKey(env, id)
	files map[string]map[FileName][]byte // idem
}

// NewFakeStore rend une fake déjà scopée sur tenancy.Local — l'unique
// environnement tant que l'authentification n'existe pas. Un test qui
// veut plusieurs environnements appelle For.
func NewFakeStore() *FakeStore {
	return &FakeStore{
		shared: &fakeShared{jobs: map[string]Job{}, files: map[string]map[FileName][]byte{}},
		scope:  tenancy.Scope{Env: tenancy.Local, Role: tenancy.RoleOwner},
	}
}

func (s *FakeStore) For(scope tenancy.Scope) Store {
	return &FakeStore{shared: s.shared, scope: scope}
}

// envKey préfixe l'identifiant par l'environnement : deux environnements
// peuvent porter le même identifiant sans se voir.
func envKey(env tenancy.EnvID, id string) string { return string(env) + "\x00" + id }

func (s *FakeStore) key(id string) string { return envKey(s.scope.Env, id) }

// ensure refuse toute opération sans environnement, plutôt que d'écrire
// dans un environnement vide.
func (s *FakeStore) ensure() error {
	if err := s.scope.Valid(); err != nil {
		return fmt.Errorf("webapp: fake store: %w", err)
	}
	return nil
}

// Create, comme MongoStore : le contenu devient le fichier original et
// n'est plus porté par le job enregistré (Get ne le rend jamais).
func (s *FakeStore) Create(ctx context.Context, job Job) (Job, error) {
	if err := s.ensure(); err != nil {
		return Job{}, err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	k := s.key(job.ID)
	s.shared.files[k] = map[FileName][]byte{}
	if job.Content != nil {
		s.shared.files[k][FileOriginal] = job.Content
	}
	stored := job
	stored.Content = nil
	stored.Env = s.scope.Env
	stored.Version = 1
	s.shared.jobs[k] = stored
	return job, nil
}

func (s *FakeStore) WriteFile(ctx context.Context, id string, name FileName, data []byte) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	if _, ok := s.shared.jobs[s.key(id)]; !ok {
		return fmt.Errorf("webapp: fake store: job %s not found", id)
	}
	s.shared.files[s.key(id)][name] = append([]byte(nil), data...)
	return nil
}

func (s *FakeStore) ReadFile(ctx context.Context, id string, name FileName) ([]byte, bool, error) {
	if err := s.ensure(); err != nil {
		return nil, false, err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	data, ok := s.shared.files[s.key(id)][name]
	return data, ok, nil
}

func (s *FakeStore) Get(ctx context.Context, id string) (Job, bool, error) {
	if err := s.ensure(); err != nil {
		return Job{}, false, err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	j, ok := s.shared.jobs[s.key(id)]
	j.Env = s.scope.Env
	return j, ok, nil
}

func (s *FakeStore) Update(ctx context.Context, job Job) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	existing, ok := s.shared.jobs[s.key(job.ID)]
	if !ok {
		return fmt.Errorf("webapp: fake store: job %s not found", job.ID)
	}
	// Comme MongoStore : Update n'écrit ni la miniature ni l'avancement
	// (SetThumbnail/SetProgress), ni les tags ni le commentaire
	// (SetTags/SetComment), ni les fichiers (Create/WriteFile).
	job.Content, job.Thumbnail, job.Progress = nil, existing.Thumbnail, existing.Progress
	job.Tags, job.Comment = existing.Tags, existing.Comment
	job.Env = s.scope.Env
	job.Version = existing.Version + 1
	s.shared.jobs[s.key(job.ID)] = job
	return nil
}

func (s *FakeStore) Delete(ctx context.Context, id string) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	if _, ok := s.shared.jobs[s.key(id)]; !ok {
		return fmt.Errorf("webapp: fake store: job %s not found", id)
	}
	delete(s.shared.jobs, s.key(id))
	delete(s.shared.files, s.key(id))
	return nil
}

func (s *FakeStore) List(ctx context.Context, q ListQuery) ([]Job, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()

	limit := q.Limit
	if limit == 0 {
		limit = DefaultListLimit
	}

	prefix := string(s.scope.Env) + "\x00"
	matched := make([]Job, 0, len(s.shared.jobs))
	for k, j := range s.shared.jobs {
		if len(k) < len(prefix) || k[:len(prefix)] != prefix {
			continue // un autre environnement
		}
		if !q.Matches(j) {
			continue
		}
		if q.SummaryOnly {
			j.Content, j.Result, j.Thumbnail, j.Progress = nil, nil, nil, nil
		}
		j.Env = s.scope.Env
		matched = append(matched, j)
	}

	sort.Slice(matched, func(i, k int) bool { return matched[i].CreatedAt.After(matched[k].CreatedAt) })

	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}

func (s *FakeStore) SetThumbnail(ctx context.Context, id string, png []byte) error {
	return s.mutate(id, func(j *Job) { j.Thumbnail = png })
}

func (s *FakeStore) SetTags(ctx context.Context, id string, tags []string) error {
	return s.mutate(id, func(j *Job) { j.Tags = append([]string(nil), tags...) })
}

func (s *FakeStore) SetComment(ctx context.Context, id, comment string) error {
	return s.mutate(id, func(j *Job) { j.Comment = comment })
}

func (s *FakeStore) SetProgress(ctx context.Context, id string, progress *pipeline.Progress) error {
	if progress != nil {
		cp := *progress
		progress = &cp
	}
	return s.mutate(id, func(j *Job) { j.Progress = progress })
}

// mutate applique une écriture ciblée sur un seul champ, comme le $set de
// MongoStore : relire puis réécrire le job entier annulerait une écriture
// concurrente (constat du jalon 39).
func (s *FakeStore) mutate(id string, apply func(*Job)) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	j, ok := s.shared.jobs[s.key(id)]
	if !ok {
		return fmt.Errorf("webapp: fake store: job %s not found", id)
	}
	apply(&j)
	j.Version++
	s.shared.jobs[s.key(id)] = j
	return nil
}
