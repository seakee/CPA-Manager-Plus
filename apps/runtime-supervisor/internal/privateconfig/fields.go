package privateconfig

import (
	"encoding/json"
	"strconv"
	"strings"
)

// configFields mirrors the Manager write allowlist for the Runtime journal. The
// Host validates independently: a Manager bug must never widen the file writer,
// and a path outside this table is rejected even if a grant names it.
// Types and non-positive semantics are frozen from CPA v8.0.55 (tag e6e5708e):
// routing.retry.request-retry (config.go:86-88), routing.retry.max-retry-interval
// (config.go:93-97), requests.passthrough-headers (sdk_config.go:66-67),
// requests.nonstream-keepalive-interval (sdk_config.go:71-74),
// requests.streaming.keepalive-seconds (sdk_config.go:100-104) and
// requests.streaming.bootstrap-retries (sdk_config.go:106-109).
var configFields = map[string]fieldSpec{
	"routing.retry.request-retry":           {kind: "int", min: 0, max: 10},
	"routing.retry.max-retry-interval":      {kind: "int", min: 0, max: 300},
	"requests.streaming.keepalive-seconds":  {kind: "int", min: 0, max: 300},
	"requests.streaming.bootstrap-retries":  {kind: "int", min: 0, max: 10},
	"requests.nonstream-keepalive-interval": {kind: "int", min: 0, max: 300},
	"requests.passthrough-headers":          {kind: "bool"},
}

type fieldSpec struct {
	kind string
	min  int
	max  int
}

func fieldFor(path string) ([]string, fieldSpec, bool) {
	spec, ok := configFields[path]
	if !ok {
		return nil, fieldSpec{}, false
	}
	return strings.Split(path, "."), spec, true
}

// normalizeFieldValue turns a JSON-decoded scalar into the exact Go type the
// allowlist uses; a non-integral number stays float64 and is then rejected.
func normalizeFieldValue(value any) any {
	switch number := value.(type) {
	case float64:
		if number == float64(int(number)) {
			return int(number)
		}
	case int64:
		return int(number)
	}
	return value
}

func validFieldValue(path string, value any) bool {
	_, spec, ok := fieldFor(path)
	if !ok {
		return false
	}
	switch scalar := normalizeFieldValue(value).(type) {
	case int:
		return spec.kind == "int" && scalar >= spec.min && scalar <= spec.max
	case bool:
		return spec.kind == "bool"
	default:
		return false
	}
}

// fieldLeaf renders the exact YAML scalar a field value must produce.
func fieldLeaf(path string, value any) (tag, text string, ok bool) {
	if !validFieldValue(path, value) {
		return "", "", false
	}
	switch scalar := normalizeFieldValue(value).(type) {
	case int:
		return "!!int", strconv.Itoa(scalar), true
	case bool:
		if scalar {
			return "!!bool", "true", true
		}
		return "!!bool", "false", true
	default:
		return "", "", false
	}
}

// operationPath is the canonical field an operation writes, including records
// reserved before the allowlist cutover.
func operationPath(op Operation) (string, any, bool) {
	if op.Path != "" {
		value := normalizeFieldValue(op.Value)
		return op.Path, value, validFieldValue(op.Path, value)
	}
	switch op.Scope {
	case "request-retry":
		return "routing.retry.request-retry", op.Retry, true
	case "max-retry-interval":
		return "routing.retry.max-retry-interval", op.MaxRetryInterval, true
	default:
		return "", nil, false
	}
}

// validOperation accepts the pre-allowlist scopes of records written before the
// cutover and the generic config-field scope of every new reservation.
func validOperation(r *record) bool {
	op := r.Operation
	if op.ID == "" || op.Generation == "" || !operationScopeValid(op.Scope) {
		return false
	}
	switch op.Scope {
	case "initial-key":
		return true
	case "key-add", "key-revoke":
		return r.Version == 2
	case "request-retry":
		// Pre-allowlist record: the value lives in exactly that legacy scalar and
		// nowhere else, so a mixed shape is still refused.
		return op.Path == "" && op.Value == nil && op.MaxRetryInterval == 0 && !op.MaxRetryIntervalSet &&
			validFieldValue("routing.retry.request-retry", op.Retry)
	case "max-retry-interval":
		return op.Path == "" && op.Value == nil && op.Retry == 0 && op.MaxRetryIntervalSet &&
			validFieldValue("routing.retry.max-retry-interval", op.MaxRetryInterval)
	}
	return op.Path == op.Scope && validFieldValue(op.Path, op.Value)
}

func operationScopeValid(scope string) bool {
	switch scope {
	case "initial-key", "key-add", "key-revoke", "request-retry", "max-retry-interval":
		// The last two are the pre-allowlist scopes: an upgraded record must keep
		// loading and reconciling, and the allowlist still owns their bounds.
		return true
	}
	_, ok := configFields[scope]
	return ok
}

// migrateCommittedFields moves a pre-allowlist committed scalar into the generic
// committed map and clears the legacy copy. A committed value is therefore
// verified, reported and compared in exactly one place, and a later write of the
// same field can never leave a stale legacy value behind.
func migrateCommittedFields(r *record) {
	for _, legacy := range []struct {
		path  string
		value *int
	}{
		{"routing.retry.request-retry", r.CommittedRetry},
		{"routing.retry.max-retry-interval", r.CommittedMaxRetryInterval},
	} {
		if legacy.value == nil {
			continue
		}
		raw, err := json.Marshal(*legacy.value)
		if err != nil {
			continue
		}
		if r.CommittedFields == nil {
			r.CommittedFields = map[string]json.RawMessage{}
		}
		// A generic value is always newer than the legacy scalar it replaced.
		if _, migrated := r.CommittedFields[legacy.path]; !migrated {
			r.CommittedFields[legacy.path] = raw
		}
	}
	r.CommittedRetry = nil
	r.CommittedMaxRetryInterval = nil
}

// committedFieldValues merges the generic committed map with the pre-allowlist
// committed scalars into one path -> JSON scalar map.
func committedFieldValues(r *record) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(r.CommittedFields))
	for path, raw := range r.CommittedFields {
		out[path] = raw
	}
	return out
}
