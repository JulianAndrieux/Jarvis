package webapp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/JulianAndrieux/Jarvis/internal/pipeline"
)

// FakeStore est une implémentation de test de Store : en mémoire, aucune
// base requise. Les jobs sont perdus à la fin du process — attendu pour
// un test.
type FakeStore struct {
	mu    sync.Mutex
	jobs  map[string]Job
	files map[string]map[FileName][]byte
}

func NewFakeStore() *FakeStore {
	return &FakeStore{jobs: map[string]Job{}, files: map[string]map[FileName][]byte{}}
}

// Create, comme MongoStore : le contenu devient le fichier original et
// n'est plus porté par le job enregistré (Get ne le rend jamais).
func (s *FakeStore) Create(ctx context.Context, job Job) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[job.ID] = map[FileName][]byte{}
	if job.Content != nil {
		s.files[job.ID][FileOriginal] = job.Content
	}
	stored := job
	stored.Content = nil
	s.jobs[job.ID] = stored
	return job, nil
}

func (s *FakeStore) WriteFile(ctx context.Context, id string, name FileName, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[id]; !ok {
		return fmt.Errorf("webapp: fake store: job %s not found", id)
	}
	s.files[id][name] = append([]byte(nil), data...)
	return nil
}

func (s *FakeStore) ReadFile(ctx context.Context, id string, name FileName) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.files[id][name]
	return data, ok, nil
}

func (s *FakeStore) Get(ctx context.Context, id string) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	return j, ok, nil
}

func (s *FakeStore) Update(ctx context.Context, job Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.jobs[job.ID]
	if !ok {
		return fmt.Errorf("webapp: fake store: job %s not found", job.ID)
	}
	// Comme MongoStore : Update n'écrit ni la miniature ni l'avancement
	// (SetThumbnail/SetProgress), ni les tags ni le commentaire
	// (SetTags/SetComment), ni les fichiers (Create/WriteFile).
	job.Content, job.Thumbnail, job.Progress = nil, existing.Thumbnail, existing.Progress
	job.Tags, job.Comment = existing.Tags, existing.Comment
	s.jobs[job.ID] = job
	return nil
}

func (s *FakeStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[id]; !ok {
		return fmt.Errorf("webapp: fake store: job %s not found", id)
	}
	delete(s.jobs, id)
	delete(s.files, id)
	return nil
}

func (s *FakeStore) List(ctx context.Context, q ListQuery) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	limit := q.Limit
	if limit == 0 {
		limit = DefaultListLimit
	}
	search := strings.ToLower(q.Search)

	matched := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		if q.Status != "" && j.Status != q.Status {
			continue
		}
		if q.Format != "" && string(familyOf(j)) != q.Format {
			continue
		}
		if !q.CreatedFrom.IsZero() && j.CreatedAt.Before(q.CreatedFrom) {
			continue
		}
		if !q.CreatedBefore.IsZero() && !j.CreatedAt.Before(q.CreatedBefore) {
			continue
		}
		if search == "" || jobMatchesSearch(j, search) {
			if q.SummaryOnly {
				j.Content, j.Result, j.Thumbnail, j.Progress = nil, nil, nil, nil
			}
			matched = append(matched, j)
		}
	}

	sort.Slice(matched, func(i, k int) bool { return matched[i].CreatedAt.After(matched[k].CreatedAt) })

	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}

func (s *FakeStore) SetThumbnail(ctx context.Context, id string, png []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("webapp: fake store: job %s not found", id)
	}
	j.Thumbnail = png
	s.jobs[id] = j
	return nil
}

func (s *FakeStore) SetTags(ctx context.Context, id string, tags []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("webapp: fake store: job %s not found", id)
	}
	j.Tags = append([]string(nil), tags...)
	s.jobs[id] = j
	return nil
}

func (s *FakeStore) SetComment(ctx context.Context, id, comment string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("webapp: fake store: job %s not found", id)
	}
	j.Comment = comment
	s.jobs[id] = j
	return nil
}

func (s *FakeStore) SetProgress(ctx context.Context, id string, progress *pipeline.Progress) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("webapp: fake store: job %s not found", id)
	}
	if progress != nil {
		cp := *progress
		progress = &cp
	}
	j.Progress = progress
	s.jobs[id] = j
	return nil
}

func jobMatchesSearch(j Job, lowerSearch string) bool {
	if strings.Contains(strings.ToLower(j.Filename), lowerSearch) {
		return true
	}
	if strings.Contains(strings.ToLower(j.DocType), lowerSearch) {
		return true
	}
	for _, tag := range j.Tags {
		if strings.Contains(strings.ToLower(tag), lowerSearch) {
			return true
		}
	}
	if strings.Contains(strings.ToLower(j.Comment), lowerSearch) {
		return true
	}
	if strings.Contains(strings.ToLower(j.SearchText), lowerSearch) {
		return true
	}
	return false
}
