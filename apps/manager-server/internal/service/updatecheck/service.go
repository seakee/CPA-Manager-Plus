package updatecheck

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
)

type Store interface {
	LoadUpdateCheck(context.Context) ([]byte, error)
	SaveUpdateCheck(context.Context, []byte) error
	ClaimUpdateNotification(context.Context, string) (bool, error)
	DismissUpdateNotification(context.Context, string) error
}
type State struct {
	SchemaVersion  int                    `json:"schema_version"`
	Preference     string                 `json:"channel_preference"`
	LastAttempt    time.Time              `json:"last_attempt_at"`
	LastSuccess    time.Time              `json:"last_success_at"`
	LastError      string                 `json:"last_error,omitempty"`
	Index          *Index                 `json:"index,omitempty"`
	Releases       map[string]ReleaseInfo `json:"releases"`
	ReleaseDigests map[string]string      `json:"release_digests,omitempty"`
}
type Status struct {
	CurrentVersion    string            `json:"current_version"`
	SourceCommit      string            `json:"source_commit"`
	DeploymentVariant string            `json:"deployment_variant"`
	BaseImageVersion  string            `json:"base_image_version,omitempty"`
	ActiveSlot        string            `json:"active_slot,omitempty"`
	ChannelPreference string            `json:"channel_preference"`
	Channel           string            `json:"channel"`
	Automatic         bool              `json:"automatic"`
	State             string            `json:"state"`
	LastSuccess       time.Time         `json:"last_success_at"`
	LastError         string            `json:"last_error,omitempty"`
	Stale             bool              `json:"stale"`
	Target            *ReleaseInfo      `json:"target,omitempty"`
	UpgradeAction     string            `json:"upgrade_action,omitempty"`
	SelfUpdate        *SelfUpdateStatus `json:"self_update,omitempty"`
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
	releaseBaseURL    string
	releaseURL        func(string) string
	detectDeployment  func() string
	now               func() time.Time
	baseImageVersion  string
	activeSlot        string
	launcher          *LauncherClient
	preActivateFence  PreActivateFenceFunc
	preSlotSnapshot   PreSlotSnapshotFunc
	activating        atomic.Bool
	currentSchemaGen  int
	slotArch          string
	activateTimeout   time.Duration
}

func New(store Store, current, commit string, automatic bool) *Service {
	launcher, err := NewLauncherClientFromEnv()
	if err != nil {
		launcher = nil
	}
	return &Service{
		store:            store,
		current:          current,
		commit:           commit,
		automatic:        automatic,
		state:            State{SchemaVersion: 1, Preference: "auto", Releases: map[string]ReleaseInfo{}, ReleaseDigests: map[string]string{}},
		now:              time.Now,
		indexURL:         IndexURL,
		releaseBaseURL:   Repository,
		detectDeployment: DetectDeploymentVariant,
		baseImageVersion: strings.TrimSpace(os.Getenv(BaseVersionEnv)),
		activeSlot:       strings.TrimSpace(os.Getenv(RunningSlotEnv)),
		launcher:         launcher,
		currentSchemaGen: 1,
		slotArch:         runtime.GOARCH,
		activateTimeout:  DefaultSlotActivateTimeout,
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
	st, err := s.readState(ctx)
	if err != nil {
		return err
	}
	if st.ReleaseDigests == nil {
		st.ReleaseDigests = map[string]string{}
	}
	s.state = st
	s.loaded = true
	return nil
}

func (s *Service) readState(ctx context.Context) (State, error) {
	data, err := s.store.LoadUpdateCheck(ctx)
	if err != nil {
		return State{}, err
	}
	st := State{SchemaVersion: 1, Preference: "auto", Releases: map[string]ReleaseInfo{}, ReleaseDigests: map[string]string{}}
	if len(data) > 0 {
		if err = json.Unmarshal(data, &st); err != nil {
			return State{}, errors.New("invalid persisted update state")
		}
		if st.SchemaVersion != 1 || !validPreference(st.Preference) || st.Releases == nil {
			return State{}, errors.New("unsupported persisted update state")
		}
		clearedLegacyIndex := false
		if st.Index != nil && st.Index.SchemaVersion == 1 {
			st.Index = nil
			st.Releases = map[string]ReleaseInfo{}
			st.ReleaseDigests = map[string]string{}
			clearedLegacyIndex = true
		} else if len(st.Releases) > 0 {
			allLegacy := true
			for tag, info := range st.Releases {
				if info.SchemaVersion == 1 {
					delete(st.Releases, tag)
					delete(st.ReleaseDigests, tag)
				} else {
					allLegacy = false
				}
			}
			if st.Index == nil && allLegacy {
				clearedLegacyIndex = true
			}
		}
		if clearedLegacyIndex {
			st.LastAttempt = time.Time{}
			st.LastSuccess = time.Time{}
			st.LastError = ""
		}
		if st.Index != nil {
			if err = st.Index.Validate(); err != nil {
				return State{}, err
			}
		}
		for tag, info := range st.Releases {
			if err = info.Validate(tag); err != nil {
				return State{}, err
			}
		}
		for tag, digest := range st.ReleaseDigests {
			if _, ok := st.Releases[tag]; !ok {
				delete(st.ReleaseDigests, tag)
				continue
			}
			if !sha256Pattern.MatchString(digest) {
				return State{}, errors.New("unsupported persisted update state")
			}
		}
	}
	if st.ReleaseDigests == nil {
		st.ReleaseDigests = map[string]string{}
	}
	return st, nil
}

// SavedChannel reads SQLite, including after an unacknowledged write. It must
// not replace downloaded-but-not-yet-persisted update metadata in the cache.
func (s *Service) SavedChannel(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readState(ctx)
	return st.Preference, err
}
func newTrialHoldSelfUpdateError() *SelfUpdateError {
	return &SelfUpdateError{
		HTTPStatus:        http.StatusServiceUnavailable,
		Code:              middleware.TrialHoldErrorCode,
		Message:           "upgrade trial verification is holding writes",
		DefiniteRejection: true,
	}
}

func (s *Service) saveBackgroundCache(ctx context.Context) error {
	done, blocked := middleware.BeginBackgroundMutation()
	if blocked {
		return nil
	}
	defer done()
	return s.saveDirectLocked(ctx)
}

func (s *Service) saveDirectLocked(ctx context.Context) error {
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
func (s *Service) deploymentVariant() string {
	if s.detectDeployment == nil {
		return DeploymentVariantUnknown
	}
	switch v := s.detectDeployment(); v {
	case DeploymentVariantDockerFull, DeploymentVariantDockerSlim, DeploymentVariantNative, DeploymentVariantUnknown:
		return v
	default:
		return DeploymentVariantUnknown
	}
}
func (s *Service) snapshot() Status {
	out := Status{CurrentVersion: s.current, SourceCommit: s.commit, DeploymentVariant: s.deploymentVariant(), ChannelPreference: s.state.Preference, Channel: s.channel(), Automatic: s.automatic, State: "never_checked", LastSuccess: s.state.LastSuccess, LastError: s.state.LastError, Stale: s.state.LastSuccess.IsZero() || s.now().Sub(s.state.LastSuccess) > 7*time.Hour}
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
	info, ok := s.cachedRelease(target)
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
	if err := s.load(ctx); err != nil {
		s.mu.Unlock()
		return Status{}, err
	}
	out := s.snapshot()
	s.mu.Unlock()
	return s.attachSelfUpdateStatus(ctx, out), nil
}
func (s *Service) SetChannel(ctx context.Context, p string) (Status, error) {
	if !validPreference(p) {
		return Status{}, errors.New("invalid channel")
	}
	done, blocked := middleware.BeginBackgroundMutation()
	if blocked {
		return Status{}, newTrialHoldSelfUpdateError()
	}
	s.mu.Lock()
	if err := s.persistChannelDirectLocked(ctx, p); err != nil {
		s.loaded = false
		s.mu.Unlock()
		done()
		return Status{}, err
	}
	done()
	out, err := s.checkLocked(ctx, true)
	s.mu.Unlock()
	if err != nil {
		return s.attachSelfUpdateStatus(ctx, out), err
	}
	return s.attachSelfUpdateStatus(ctx, out), nil
}

// PersistChannel saves only the preference. A successful result acknowledges
// SQLite persistence, independently of remote update discovery. Legacy callers
// retain SetChannel's immediate check; Fresh uses this bounded save operation.
func (s *Service) PersistChannel(ctx context.Context, p string) (Status, error) {
	if !validPreference(p) {
		return Status{}, errors.New("invalid channel")
	}
	done, blocked := middleware.BeginBackgroundMutation()
	if blocked {
		return Status{}, newTrialHoldSelfUpdateError()
	}
	s.mu.Lock()
	if err := s.persistChannelDirectLocked(ctx, p); err != nil {
		// Resolve even an uncertain SQLite acknowledgement from the store
		// before another save/check can publish a cached preference.
		s.loaded = false
		s.mu.Unlock()
		done()
		return Status{}, err
	}
	out := s.snapshot()
	s.mu.Unlock()
	done()
	return s.attachSelfUpdateStatus(ctx, out), nil
}

func (s *Service) persistChannelDirectLocked(ctx context.Context, p string) error {
	if err := s.load(ctx); err != nil {
		return err
	}
	old := s.state.Preference
	s.state.Preference = p
	if err := s.saveDirectLocked(ctx); err != nil {
		s.state.Preference = old
		return err
	}
	return nil
}
func (s *Service) fetch(ctx context.Context, raw string, dst any) error {
	return s.fetchVerified(ctx, raw, "", dst)
}

func (s *Service) fetchVerified(ctx context.Context, raw, expectedSHA256 string, dst any) error {
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
	if expectedSHA256 != "" {
		sum := sha256.Sum256(data)
		if "sha256:"+hex.EncodeToString(sum[:]) != expectedSHA256 {
			return errors.New("release metadata digest mismatch")
		}
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
	if err := s.load(ctx); err != nil {
		s.mu.Unlock()
		return Status{}, err
	}
	out, err := s.checkLocked(ctx, false)
	s.mu.Unlock()
	return s.attachSelfUpdateStatus(ctx, out), err
}

func (s *Service) checkLocked(ctx context.Context, force bool) (Status, error) {
	now := s.now()
	if !force && !s.state.LastAttempt.IsZero() && now.Sub(s.state.LastAttempt) < time.Minute {
		if s.persistenceFailed {
			if err := s.saveBackgroundCache(ctx); err != nil {
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
	if e := s.saveBackgroundCache(ctx); e != nil {
		return s.snapshot(), e
	}
	return s.snapshot(), nil
}
func (s *Service) releaseInfoURL(target *Target) string {
	if s.releaseURL != nil {
		return s.releaseURL(target.Version)
	}
	base := strings.TrimRight(s.releaseBaseURL, "/")
	if base == "" {
		base = Repository
	}
	relPath := strings.TrimLeft(target.ReleaseInfoPath, "/")
	if relPath == "" {
		relPath = "releases/download/" + target.Version + "/release-info.json"
	}
	return base + "/" + relPath
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
			if _, ok := s.cachedRelease(target); !ok {
				var info ReleaseInfo
				if err = s.fetchVerified(ctx, s.releaseInfoURL(target), target.ReleaseInfoSHA256, &info); err != nil {
					return err
				}
				if err = info.Validate(target.Version); err != nil {
					return err
				}
				if s.state.ReleaseDigests == nil {
					s.state.ReleaseDigests = map[string]string{}
				}
				s.state.Releases[target.Version] = info
				if target.ReleaseInfoSHA256 != "" {
					s.state.ReleaseDigests[target.Version] = target.ReleaseInfoSHA256
				}
			}
		}
	}
	s.state.Index = &idx
	return nil
}
func (s *Service) cachedRelease(target *Target) (ReleaseInfo, bool) {
	if target == nil {
		return ReleaseInfo{}, false
	}
	info, ok := s.state.Releases[target.Version]
	if !ok {
		return ReleaseInfo{}, false
	}
	if target.ReleaseInfoSHA256 != "" && s.state.ReleaseDigests[target.Version] != target.ReleaseInfoSHA256 {
		return ReleaseInfo{}, false
	}
	return info, true
}
func (s *Service) Claim(ctx context.Context) (*ReleaseInfo, error) {
	done, blocked := middleware.BeginBackgroundMutation()
	if blocked {
		return nil, newTrialHoldSelfUpdateError()
	}
	defer done()
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
	if err := s.saveDirectLocked(ctx); err != nil {
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
	done, blocked := middleware.BeginBackgroundMutation()
	if blocked {
		return newTrialHoldSelfUpdateError()
	}
	defer done()
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
