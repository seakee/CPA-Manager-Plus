package cpa

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// keyListCPA serves the one projection `Configured` reads, with the list a test
// states. It counts requests, so a refused read can prove it never asked.
type keyListCPA struct {
	keys     []string
	requests int
	server   *httptest.Server
}

func newKeyListCPA(t *testing.T, keys ...string) *keyListCPA {
	t.Helper()
	f := &keyListCPA{keys: keys}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests++
		if r.Method != http.MethodGet || r.URL.Path != "/v8/management/config/access/api-keys" {
			t.Error("unexpected request")
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+testManagement() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(f.keys)
	}))
	t.Cleanup(f.server.Close)
	return f
}

// TestConfiguredStatesMembershipForAnAdoptedDestination is the adoption observation
// regression. An adopted destination carries the source CPA's own list, so the key
// this installation was journaled with is one member of it — the single-key reading
// would answer "not configured" for every adoption, which the key page shows as a
// failed installation. A New Managed destination keeps the exact one-key reading,
// and neither posture widens to "some key in the list".
func TestConfiguredStatesMembershipForAnAdoptedDestination(t *testing.T) {
	const adopted = "source-key-one"
	for name, scenario := range map[string]struct {
		adopt bool
		keys  []string
		want  bool
	}{
		"adopted membership": {true, []string{adopted, "source-key-two", "source-key-three"}, true},
		"adopted absent":     {true, []string{"source-key-two", "source-key-three"}, false},
		"adopted empty list": {true, []string{}, false},
		"managed sole key":   {false, []string{testSecret()}, true},
		"managed two keys":   {false, []string{testSecret(), adopted}, false},
		"managed empty list": {false, []string{}, false},
	} {
		t.Run(name, func(t *testing.T) {
			secret := adopted
			if !scenario.adopt {
				secret = testSecret()
			}
			fake := newKeyListCPA(t, scenario.keys...)
			transport := &Transport{client: fake.server.Client(), endpoint: fake.server.URL}
			if scenario.adopt {
				transport.SetPostureSelector(func() (DestinationPosture, bool) {
					return DestinationPosture{Adopt: true}, true
				})
			}
			ok, err := transport.Configured(t.Context(), testManagement(), secret)
			if err != nil || ok != scenario.want {
				t.Fatalf("configured = %v %v, want %v", ok, err, scenario.want)
			}
		})
	}
}

// TestValidKeySetAnswersThePosturesOwnBound is the write-time half of the same
// question: the policy a whole-list write applies, asked before the attempt.
// Adopted and New Managed destinations impose no artificial upper cap on total
// keys, while the private loopback fixture retains its intentional two-key bound.
func TestValidKeySetAnswersThePosturesOwnBound(t *testing.T) {
	adopted := &Transport{}
	adopted.SetPostureSelector(func() (DestinationPosture, bool) { return DestinationPosture{Adopt: true}, true })
	source := func(n int) []string {
		keys := make([]string, 0, n)
		for i := range n {
			keys = append(keys, "source-key-"+strconv.Itoa(i))
		}
		return keys
	}
	if !adopted.ValidKeySet(source(129)) || !adopted.ValidKeySet(source(200)) {
		t.Fatal("adopted destination refused a multi-key list above 128")
	}
	if adopted.ValidKeySet(nil) || adopted.ValidKeySet([]string{"a b"}) || adopted.ValidKeySet([]string{"dup", "dup"}) {
		t.Fatal("an adopted destination accepted a list it cannot hold")
	}
	managed := &Transport{posture: managedProofPosture}
	manyCanonical := make([]string, 0, 150)
	for i := range 150 {
		hexSuffix := strconv.FormatInt(int64(i+1), 16)
		manyCanonical = append(manyCanonical, "cpamp_sk_"+strings.Repeat("0", 64-len(hexSuffix))+hexSuffix)
	}
	if !managed.ValidKeySet([]string{testSecret()}) || !managed.ValidKeySet(manyCanonical[:3]) || !managed.ValidKeySet(manyCanonical) {
		t.Fatal("a multi-key canonical list the managed posture holds was refused")
	}
	if managed.ValidKeySet(nil) || managed.ValidKeySet([]string{testSecret(), testSecret()}) {
		t.Fatal("the managed posture accepted an empty or duplicate key list")
	}
	if managed.ValidKeySet([]string{testSecret(), "source-key-one"}) {
		t.Fatal("the managed posture accepted a non-canonical key")
	}
	private := &Transport{}
	if !private.ValidKeySet(manyCanonical[:2]) || private.ValidKeySet(manyCanonical[:3]) {
		t.Fatal("private fixture two-key bound drifted")
	}
}
