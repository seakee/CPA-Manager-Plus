package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestPersistChannelPreservesMetadataAndDrivesChecker(t *testing.T) {
	ctx := context.Background()
	store := &memoryStore{}
	s, _, indices := testService(t, "v2.0.0", "v2.1.0", store)
	if _, err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	var before State
	if err := json.Unmarshal(store.data, &before); err != nil {
		t.Fatal(err)
	}
	for _, channel := range []string{"stable", "rc", "beta", "auto"} {
		status, err := s.PersistChannel(ctx, channel)
		if err != nil || status.ChannelPreference != channel {
			t.Fatalf("save %s: %+v %v", channel, status, err)
		}
		var after State
		if err := json.Unmarshal(store.data, &after); err != nil {
			t.Fatal(err)
		}
		after.Preference = before.Preference
		if !reflect.DeepEqual(before, after) || indices.Load() != 1 {
			t.Fatal("save changed metadata or performed discovery")
		}
	}
	// Independent discovery can fail after a durable save. The preference
	// remains saved, and the checker really selects the new channel.
	offline := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer offline.Close()
	s.indexURL = offline.URL
	if _, err := s.PersistChannel(ctx, "beta"); err != nil {
		t.Fatal(err)
	}
	s.state.LastAttempt = s.state.LastAttempt.Add(-2 * time.Minute)
	status, err := s.Check(ctx)
	if err != nil || status.LastError == "" || status.Channel != "beta" {
		t.Fatalf("checker did not consume preference: %+v %v", status, err)
	}
	restarted := New(store, "v2.0.0", "test", false)
	if channel, err := restarted.SavedChannel(ctx); err != nil || channel != "beta" {
		t.Fatalf("restart: %s %v", channel, err)
	}
}

// Model a store that committed but lost its acknowledgement. A later check
// must reload the durable preference rather than overwrite it with old cache.
type unacknowledgedStore struct {
	memoryStore
	loseReply bool
}

func (m *unacknowledgedStore) SaveUpdateCheck(ctx context.Context, data []byte) error {
	if err := m.memoryStore.SaveUpdateCheck(ctx, data); err != nil {
		return err
	}
	if m.loseReply {
		return errors.New("acknowledgement lost")
	}
	return nil
}

func TestUnacknowledgedPreferenceReloadsBeforeNextCheck(t *testing.T) {
	store := &unacknowledgedStore{}
	s := New(store, "dev", "test", false)
	if _, err := s.PersistChannel(t.Context(), "stable"); err != nil {
		t.Fatal(err)
	}
	store.loseReply = true
	if _, err := s.PersistChannel(t.Context(), "beta"); err == nil {
		t.Fatal("lost acknowledgement reported success")
	}
	if actual, err := s.SavedChannel(t.Context()); err != nil || actual != "beta" {
		t.Fatal("did not reconcile actual store", actual, err)
	}
	store.loseReply = false
	status, err := s.Check(t.Context())
	if err != nil || status.Channel != "beta" {
		t.Fatal("check used old cache", status, err)
	}
	if actual, err := s.SavedChannel(t.Context()); err != nil || actual != "beta" {
		t.Fatal("check overwrote durable preference", actual, err)
	}
}

func TestSavedChannelReadsStoreAndFailureDoesNotOverwritePreference(t *testing.T) {
	ctx := context.Background()
	store := &memoryStore{}
	s := New(store, "v2.0.0", "test", false)
	if channel, err := s.SavedChannel(ctx); err != nil || channel != "auto" {
		t.Fatal(channel, err)
	}
	if _, err := s.PersistChannel(ctx, "stable"); err != nil {
		t.Fatal(err)
	}
	before := string(store.data)
	if _, err := s.PersistChannel(ctx, "invalid"); err == nil || string(store.data) != before {
		t.Fatal("invalid preference changed store")
	}
	store.fail = true
	if _, err := s.PersistChannel(ctx, "beta"); err == nil {
		t.Fatal("failed write acknowledged")
	}
	if channel, err := s.SavedChannel(ctx); err != nil || channel != "stable" || string(store.data) != before {
		t.Fatal("failed save changed durable preference", channel, err)
	}
	store.fail = false
	// A previous process/cache is not authoritative for readback.
	var actual State
	_ = json.Unmarshal(store.data, &actual)
	actual.Preference = "rc"
	store.data, _ = json.Marshal(actual)
	if channel, err := s.SavedChannel(ctx); err != nil || channel != "rc" {
		t.Fatal("readback used a cached preference", channel, err)
	}
	if _, err := s.PersistChannel(ctx, "auto"); err != nil {
		t.Fatal("write did not recover", err)
	}
}

func TestConcurrentPreferenceSavesAndChecks(t *testing.T) {
	s, _, _ := testService(t, "v2.0.0", "v2.1.0", &memoryStore{})
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.PersistChannel(t.Context(), "stable"); err != nil {
				t.Error(err)
			}
			if _, err := s.Check(t.Context()); err != nil {
				t.Error(err)
			}
			if channel, err := s.SavedChannel(t.Context()); err != nil || channel != "stable" {
				t.Error(channel, err)
			}
		}()
	}
	wg.Wait()
}
