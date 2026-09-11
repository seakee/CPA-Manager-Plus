package updatecheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

type Store interface {
	LoadUpdateCheck(context.Context) ([]byte, error)
	SaveUpdateCheck(context.Context, []byte) error
	ClaimUpdateNotification(context.Context, string) (bool, error)
	DismissUpdateNotification(context.Context, string) error
}
type State struct {
	SchemaVersion int                    `json:"schema_version"`
	Preference    string                 `json:"channel_preference"`
	LastAttempt   time.Time              `json:"last_attempt_at"`
	LastSuccess   time.Time              `json:"last_success_at"`
	LastError     string                 `json:"last_error,omitempty"`
	Index         *Index                 `json:"index,omitempty"`
	Releases      map[string]ReleaseInfo `json:"releases"`
}
type Status struct {
	CurrentVersion    string       `json:"current_version"`
	SourceCommit      string       `json:"source_commit"`
	ChannelPreference string       `json:"channel_preference"`
	Channel           string       `json:"channel"`
	Automatic         bool         `json:"automatic"`
	State             string       `json:"state"`
	LastSuccess       time.Time    `json:"last_success_at"`
	LastError         string       `json:"last_error,omitempty"`
	Stale             bool         `json:"stale"`
	Target            *ReleaseInfo `json:"target,omitempty"`
	UpgradeAction     string       `json:"upgrade_action,omitempty"`
}
type Service struct {
	mu                sync.Mutex
	store             Store
	current, commit   string
	automatic         bool
	loaded            bool
	persistenceFailed bool
	state             State
	client            *http.Client
	indexURL          string
	releaseURL        func(string) string
	now               func() time.Time
}

func New(store Store, current, commit string, automatic bool) *Service {
	return &Service{store: store, current: current, commit: commit, automatic: automatic, now: time.Now, indexURL: IndexURL,
		releaseURL: func(tag string) string { return Repository + "/releases/download/" + tag + "/release-info.json" },
		client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			h := req.URL.Host
			if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.User != nil || (h != "github.com" && h != "release-assets.githubusercontent.com" && h != "objects.githubusercontent.com" && h != "raw.githubusercontent.com") {
				return errors.New("untrusted update redirect")
			}
			return nil
		}}}
}
func (s *Service) load(ctx context.Context) error {
	if s.loaded {
		return nil
	}
	data, err := s.store.LoadUpdateCheck(ctx)
	if err != nil {
		return err
	}
	st := State{SchemaVersion: 1, Preference: "auto", Releases: map[string]ReleaseInfo{}}
	if len(data) > 0 {
		if err = json.Unmarshal(data, &st); err != nil {
			return errors.New("invalid persisted update state")
		}
		if st.SchemaVersion != 1 || !validPreference(st.Preference) || st.Releases == nil {
			return errors.New("unsupported persisted update state")
		}
		if st.Index != nil {
			if err = st.Index.Validate(); err != nil {
				return err
			}
		}
		for tag, info := range st.Releases {
			if err = info.Validate(tag); err != nil {
				return err
			}
		}
	}
	s.state = st
	s.loaded = true
	return nil
}
func (s *Service) save(ctx context.Context) error {
	data, err := json.Marshal(s.state)
	if err == nil {
		err = s.store.SaveUpdateCheck(ctx, data)
	}
	s.persistenceFailed = err != nil
	return err
}
func validPreference(p string) bool { return p == "auto" || p == "stable" || p == "rc" || p == "beta" }
func (s *Service) channel() string {
	if s.state.Preference != "auto" {
		return s.state.Preference
	}
	v, err := ParseVersion(s.current)
	if err != nil || v.Stage == "" {
		return "stable"
	}
	return v.Stage
}
func (s *Service) snapshot() Status {
	out := Status{CurrentVersion: s.current, SourceCommit: s.commit, ChannelPreference: s.state.Preference, Channel: s.channel(), Automatic: s.automatic, State: "never_checked", LastSuccess: s.state.LastSuccess, LastError: s.state.LastError, Stale: s.state.LastSuccess.IsZero() || s.now().Sub(s.state.LastSuccess) > 7*time.Hour}
	if s.persistenceFailed {
		out.LastError = "update state persistence failed"
	}
	cur, err := ParseVersion(s.current)
	if err != nil || cur.Stage == "" {
		out.State = "unknown_version"
		return out
	}
	if s.state.Index == nil {
		return out
	}
	target := s.state.Index.Channels[out.Channel]
	if target == nil {
		out.State = "no_candidate"
		return out
	}
	v, _ := ParseVersion(target.Version)
	cmp := Compare(v, cur)
	if cmp < 0 {
		out.State = "ahead_of_channel"
		return out
	}
	info, ok := s.state.Releases[target.Version]
	if !ok {
		return out
	}
	out.Target = &info
	if cmp == 0 {
		out.State = "up_to_date"
		return out
	}
	out.State = "update_available"
	out.UpgradeAction = "direct"
	// Major transitions, breaking changes, or migration requirements always require a reviewed guide.
	if info.Update.Breaking ||
		info.Update.MigrationRequired ||
		v.core[0] != cur.core[0] {
		out.UpgradeAction = "migration_guide"
	}
	if min := info.Update.MinimumDirectUpgradeVersion; min != nil {
		m, _ := ParseVersion(*min)
		if Compare(cur, m) < 0 {
			out.UpgradeAction = "migration_guide"
		}
	}
	return out
}
func (s *Service) Status(ctx context.Context) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(ctx); err != nil {
		return Status{}, err
	}
	return s.snapshot(), nil
}
func (s *Service) SetChannel(ctx context.Context, p string) (Status, error) {
	if !validPreference(p) {
		return Status{}, errors.New("invalid channel")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(ctx); err != nil {
		return Status{}, err
	}
	old := s.state.Preference
	s.state.Preference = p
	if err := s.save(ctx); err != nil {
		s.state.Preference = old
		return Status{}, err
	}
	return s.checkLocked(ctx, true)
}
func (s *Service) fetch(ctx context.Context, raw string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return errors.New("update network request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("update source returned HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 256*1024+1))
	if err != nil {
		return errors.New("update download failed")
	}
	if len(data) > 256*1024 {
		return errors.New("update metadata too large")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err = dec.Decode(dst); err != nil {
		return errors.New("invalid update JSON")
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return errors.New("trailing update JSON")
	}
	return nil
}

// The mutex serializes discovery/download/persistence. Normal checks share a 60-second
// cooldown; channel changes force an immediate check. Failed persistence retains downloaded data in memory.
func (s *Service) Check(ctx context.Context) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(ctx); err != nil {
		return Status{}, err
	}
	return s.checkLocked(ctx, false)
}

func (s *Service) checkLocked(ctx context.Context, force bool) (Status, error) {
	now := s.now()
	if !force && !s.state.LastAttempt.IsZero() && now.Sub(s.state.LastAttempt) < time.Minute {
		if s.persistenceFailed {
			if err := s.save(ctx); err != nil {
				return s.snapshot(), err
			}
		}
		return s.snapshot(), nil
	}
	s.state.LastAttempt = now
	err := s.discover(ctx)
	if err != nil {
		s.state.LastError = err.Error()
	} else {
		s.state.LastSuccess = s.now()
		s.state.LastError = ""
	}
	if e := s.save(ctx); e != nil {
		return s.snapshot(), e
	}
	return s.snapshot(), nil
}
func (s *Service) discover(ctx context.Context) error {
	cur, err := ParseVersion(s.current)
	if err != nil || cur.Stage == "" {
		return errors.New("unknown running version")
	}
	var idx Index
	if err = s.fetch(ctx, s.indexURL, &idx); err != nil {
		return err
	}
	if err = idx.Validate(); err != nil {
		return err
	}
	if old := s.state.Index; old != nil {
		if idx.Revision < old.Revision {
			return errors.New("stale index revision")
		}
		if idx.Revision == old.Revision {
			a, _ := json.Marshal(old)
			b, _ := json.Marshal(idx)
			if !bytes.Equal(a, b) {
				return errors.New("conflicting index revision")
			}
		}
	}
	target := idx.Channels[s.channel()]
	if target != nil {
		v, _ := ParseVersion(target.Version)
		if Compare(v, cur) >= 0 {
			if _, ok := s.state.Releases[target.Version]; !ok {
				var info ReleaseInfo
				if err = s.fetch(ctx, s.releaseURL(target.Version), &info); err != nil {
					return err
				}
				if err = info.Validate(target.Version); err != nil {
					return err
				}
				s.state.Releases[target.Version] = info
			}
		}
	}
	s.state.Index = &idx
	return nil
}
func (s *Service) Claim(ctx context.Context) (*ReleaseInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(ctx); err != nil {
		return nil, err
	}
	st := s.snapshot()
	if st.State != "update_available" || st.Stale || st.LastError != "" {
		return nil, nil
	}
	// Persist downloaded metadata before exposing a notification.
	if err := s.save(ctx); err != nil {
		return nil, err
	}
	ok, err := s.store.ClaimUpdateNotification(ctx, st.Target.Release.Version)
	if err != nil || !ok {
		return nil, err
	}
	return st.Target, nil
}
func (s *Service) Dismiss(ctx context.Context, tag string) error {
	if _, err := ParseVersion(tag); err != nil {
		return err
	}
	return s.store.DismissUpdateNotification(ctx, tag)
}
func (s *Service) Run(ctx context.Context) {
	if !s.automatic {
		return
	}
	delay := time.Duration(5+rand.IntN(55)) * time.Second
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		st, err := s.Check(ctx)
		delay = 6*time.Hour + time.Duration(rand.IntN(3601)-1800)*time.Second
		if err != nil || st.LastError != "" {
			delay = time.Duration(15+rand.IntN(16)) * time.Minute
		}
	}
}
