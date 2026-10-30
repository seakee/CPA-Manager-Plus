package freshconfig

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

type nameWire struct {
	*fakeWire
	etag     string
	writes   int
	payload  []byte
	writeErr error
}

func (w *nameWire) ReadProviderNameDocument(context.Context, string) (map[string]any, string, error) {
	return w.document, w.etag, nil
}
func (w *nameWire) PutProviderNameGroups(_ context.Context, _, provider, etag string, payload []byte) error {
	if provider != "codex" || etag != w.etag {
		return cpa.ErrConfigChanged
	}
	w.writes++
	w.payload = payload
	return w.writeErr
}
func nameService(t *testing.T) (*Service, *nameWire, *memoryStore) {
	t.Helper()
	wire := &nameWire{fakeWire: &fakeWire{}, etag: `"revision-1"`}
	wire.document = map[string]any{"client-keys": []any{"client-secret"}, "api-keys": map[string]any{"codex": []any{
		map[string]any{"name": "old@example.com", "keys": []any{map[string]any{"api-key": "provider-secret", "websockets": true, "excluded-models": []any{"*"}}}, "future-field": json.Number("9007199254740993")},
		map[string]any{"name": "other", "keys": []any{map[string]any{"api-key": "second-secret"}}},
	}}}
	svc, store := managedService(t, wire.fakeWire)
	svc.wire = wire
	return svc, wire, store
}
func nameRequest() ProviderNameRequest {
	return ProviderNameRequest{Generation: "7", ETag: `"revision-1"`, Provider: "codex", Index: 0, Name: "new@example.com"}
}

func TestProviderNameProjectionAndRename(t *testing.T) {
	svc, wire, _ := nameService(t)
	view, err := svc.ProviderNames(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(view)
	if strings.Contains(string(body), "secret") || !strings.Contains(string(body), "old@example.com") || len(view.Groups) != 2 || !view.Writable {
		t.Fatalf("unsafe projection: %s", body)
	}
	if err = svc.RenameProvider(context.Background(), nameRequest()); err != nil {
		t.Fatal(err)
	}
	var saved []map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(wire.payload)))
	decoder.UseNumber()
	if err = decoder.Decode(&saved); err != nil {
		t.Fatal(err)
	}
	if saved[0]["name"] != "new@example.com" || saved[1]["name"] != "other" || saved[0]["future-field"] != json.Number("9007199254740993") {
		t.Fatal("unrelated group data changed")
	}
	key := saved[0]["keys"].([]any)[0].(map[string]any)
	if key["api-key"] != "provider-secret" || key["websockets"] != true || key["excluded-models"].([]any)[0] != "*" {
		t.Fatal("credential or enable state changed")
	}
	if wire.writes != 1 {
		t.Fatal("write replayed")
	}
}
func TestProviderNameConflictsAndInvalidInput(t *testing.T) {
	for _, mutate := range []func(*ProviderNameRequest){
		func(q *ProviderNameRequest) { q.ETag = `"stale"` }, func(q *ProviderNameRequest) { q.Generation = "8" }, func(q *ProviderNameRequest) { q.Provider = "../../x" }, func(q *ProviderNameRequest) { q.Index = 9 }, func(q *ProviderNameRequest) { q.Name = "a\nb" }, func(q *ProviderNameRequest) { q.Name = strings.Repeat("x", 121) },
	} {
		svc, wire, _ := nameService(t)
		q := nameRequest()
		mutate(&q)
		if svc.RenameProvider(context.Background(), q) == nil || wire.writes != 0 {
			t.Fatal("invalid or stale write reached CPA")
		}
	}
	svc, wire, _ := nameService(t)
	wire.writeErr = cpa.ErrApplyUnknown
	if !errors.Is(svc.RenameProvider(context.Background(), nameRequest()), cpa.ErrApplyUnknown) || wire.writes != 1 {
		t.Fatal("unknown write was replayed")
	}
}
func TestProviderNamePrivateScopeFrozen(t *testing.T) {
	svc, wire, _ := nameService(t)
	svc.managed = false
	svc.targets = managedTargets
	if err := svc.RenameProvider(context.Background(), nameRequest()); !errors.Is(err, ErrReadOnly) || wire.writes != 0 {
		t.Fatal("private scope widened")
	}
}
