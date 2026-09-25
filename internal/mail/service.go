package mail

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// Service relève la boîte à intervalle régulier et fait trier les
// nouveaux emails par le modèle local, et porte la configuration de la
// boîte. Relève et tri sont deux boucles séparées : le tri attend son tour
// dans la file des modèles (un ticket peut la tenir une heure), la relève
// jamais.
type Service struct {
	Store   Store
	Syncer  *Syncer
	Triager *Triager // nil : pas de tri
	// Acquire : la file d'accès aux modèles (profil documents) — le tri
	// ne passe jamais pendant un autre traitement. nil : sans file.
	Acquire func(ctx context.Context) (func(), error)
	// ConfigPath : la configuration de la boîte ("" : jamais configurée).
	ConfigPath string
	// Interval : entre deux relèves (0 : 5 min).
	Interval time.Duration
	// TriageBatch : emails triés d'affilée au plus (0 : 20), avant de
	// rendre la main et de reprendre aussitôt.
	TriageBatch int
	Now         func() time.Time
	Log         func(format string, args ...any)

	mu         sync.Mutex
	status     Status
	syncErr    string
	triageErr  string
	kick       chan struct{}
	triageKick chan struct{}
}

// Status : l'état de la relève, affiché dans l'interface.
type Status struct {
	Configured bool
	Account    string
	Running    bool
	LastSync   time.Time // dernière relève réussie
	LastNew    int       // nouveaux emails à la dernière relève
	LastError  string    // dernière erreur (relève ou tri), "" si le dernier cycle a réussi
}

// errBadReply : le modèle a répondu, mais inutilisable — noté sur l'email.
var errBadReply = errors.New("réponse du modèle inutilisable")

// Config : la configuration enregistrée.
func (s *Service) Config() (Config, bool, error) {
	if s.ConfigPath == "" {
		return Config{}, false, nil
	}
	return LoadConfig(s.ConfigPath)
}

// Configure vérifie la connexion avec cette configuration, puis
// l'enregistre et lance une relève. Refusée par le serveur : rien n'est
// enregistré.
func (s *Service) Configure(ctx context.Context, c Config) error {
	c = c.Normalize()
	if err := c.Validate(); err != nil {
		return err
	}
	if s.ConfigPath == "" {
		return errors.New("aucun emplacement pour la configuration de la boîte")
	}
	tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	sess, err := s.Syncer.Connect(tctx, c)
	if err != nil {
		return err
	}
	sess.Logout()
	if err := SaveConfig(s.ConfigPath, c); err != nil {
		return err
	}
	s.logf("mail: boîte configurée (%s)", c)
	s.Kick()
	return nil
}

// Status : l'état courant.
func (s *Service) Status() Status {
	c, ok, _ := s.Config()
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status
	st.Configured, st.Account = ok, c.User
	var errs []string
	if s.syncErr != "" {
		errs = append(errs, "Relève : "+s.syncErr)
	}
	if s.triageErr != "" {
		errs = append(errs, "Tri : "+s.triageErr)
	}
	st.LastError = strings.Join(errs, " — ")
	return st
}

// Kick demande une relève sans attendre l'intervalle.
func (s *Service) Kick() { signal(s.channels()) }

func (s *Service) kickTriage() {
	_, t := s.channelsBoth()
	signal(t)
}

func signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default: // déjà demandé
	}
}

func (s *Service) channels() chan struct{} {
	k, _ := s.channelsBoth()
	return k
}

func (s *Service) channelsBoth() (kick, triage chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.kick == nil {
		s.kick, s.triageKick = make(chan struct{}, 1), make(chan struct{}, 1)
	}
	return s.kick, s.triageKick
}

// Retriage efface le tri d'un email et le fait refaire.
func (s *Service) Retriage(ctx context.Context, id string) error {
	if err := s.Store.SetTriage(ctx, id, Triage{}); err != nil {
		return err
	}
	s.kickTriage()
	return nil
}

// Run relève tout de suite, puis à chaque intervalle ou demande ; le tri
// tourne à côté, relancé après chaque relève. Jusqu'à l'arrêt de ctx.
func (s *Service) Run(ctx context.Context) {
	interval := s.interval()
	kick, triage := s.channelsBoth()
	go func() {
		for {
			if more := s.triageOnce(ctx); more && ctx.Err() == nil {
				continue // d'autres attendent : sans attendre l'intervalle
			}
			select {
			case <-ctx.Done():
				return
			case <-triage:
			case <-time.After(interval):
			}
		}
	}()
	for {
		s.syncOnce(ctx)
		s.kickTriage()
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		case <-kick:
		}
	}
}

// Cycle : une relève puis un lot de tri, l'un après l'autre (tests).
func (s *Service) Cycle(ctx context.Context) {
	s.syncOnce(ctx)
	s.triageOnce(ctx)
}

func (s *Service) interval() time.Duration {
	if s.Interval <= 0 {
		return 5 * time.Minute
	}
	return s.Interval
}

// syncOnce relève la boîte, si elle est configurée.
func (s *Service) syncOnce(ctx context.Context) {
	cfg, ok, err := s.Config()
	if err != nil || !ok {
		s.setSyncErr(err)
		return
	}
	s.setRunning(true)
	defer s.setRunning(false)
	sctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	rep, err := s.Syncer.Sync(sctx, cfg)
	cancel()
	s.setSyncErr(err)
	if err != nil {
		s.logf("mail: relève : %v", err)
		return
	}
	s.mu.Lock()
	s.status.LastSync, s.status.LastNew = s.now(), rep.New
	s.mu.Unlock()
	if rep.New > 0 {
		s.logf("mail: %d nouveaux emails", rep.New)
	}
}

// triageOnce trie un lot ; more : d'autres attendent encore.
func (s *Service) triageOnce(ctx context.Context) bool {
	more, err := s.triagePending(ctx)
	s.mu.Lock()
	s.triageErr = ""
	if err != nil {
		s.triageErr = strings.TrimPrefix(err.Error(), "mail: tri : ")
	}
	s.mu.Unlock()
	if err != nil {
		s.logf("mail: tri : %v", err)
		return false
	}
	return more
}

func (s *Service) setSyncErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncErr = ""
	if err != nil {
		s.syncErr = err.Error()
	}
}

// triagePending trie les emails en attente (un lot au plus : more si
// d'autres attendent encore). Une réponse inutilisable est notée sur
// l'email ; un modèle indisponible arrête le tri (retenté au cycle
// suivant).
func (s *Service) triagePending(ctx context.Context) (more bool, err error) {
	if s.Triager == nil {
		return false, nil
	}
	batch := s.TriageBatch
	if batch <= 0 {
		batch = 20
	}
	pending, err := s.Store.List(ctx, Query{Untriaged: true, Limit: batch})
	if err != nil {
		return false, err
	}
	for _, m := range pending {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		release := func() {}
		if s.Acquire != nil {
			if release, err = s.Acquire(ctx); err != nil {
				return false, err
			}
		}
		t, err := s.Triager.Triage(ctx, m)
		release()
		if errors.Is(err, errBadReply) {
			t = Triage{Error: err.Error(), Model: s.Triager.Model, At: s.now(), Version: TriageVersion}
		} else if err != nil {
			return false, err
		}
		if err := s.Store.SetTriage(ctx, m.ID, t); err != nil {
			return false, err
		}
	}
	return len(pending) == batch, nil
}

func (s *Service) setRunning(v bool) {
	s.mu.Lock()
	s.status.Running = v
	s.mu.Unlock()
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}
