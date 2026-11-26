package freshconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

// ConfigSourceGroupID is the journal slot owned by the source YAML editor on
// the gateway configuration page. It uses standard group operation entries
// (Target: "group") so journal invariants and read-back reconciliation apply
// uniformly without expanding the adoption group catalog.
const ConfigSourceGroupID = "source"

// SourceView projects the non-secret, non-control-plane CPA configuration
// document as editable YAML, preserving unknown upstream CPA fields so users can
// adopt new CPA options before CPAMP adapts dedicated UI controls.
type SourceView struct {
	Capability     Capability                       `json:"capability"`
	SourceWritable bool                             `json:"sourceWritable"`
	Reason         string                           `json:"reason,omitempty"`
	Generation     string                           `json:"generation"`
	Baseline       string                           `json:"baseline"`
	YAML           string                           `json:"yaml"`
	GroupsInvalid  bool                             `json:"groupsInvalid,omitempty"`
	Operation      *freshstore.ConfigGroupOperation `json:"operation,omitempty"`
}

// SourceRequest applies a diff derived from an edited scoped YAML document.
type SourceRequest struct {
	RequestID string   `json:"requestId"`
	Expected  Expected `json:"expected"`
	YAML      string   `json:"yaml"`
}

var protectedSourceRoots = map[string]bool{
	"config-version":           true,
	"management":               true,
	"remote-management":        true,
	"access":                   true,
	"api-keys":                 true,
	"plugins":                  true,
	"observability":            true,
	"models":                   true,
	"credentials":              true,
	"credential-concurrency":   true,
	"credential-in-flight":     true,
	"pprof":                    true,
	"usage-statistics-enabled": true,
	"logging":                  true,
	"auth-dir":                 true,
	"kv-store":                 true,
	"gemini-api-key":           true,
	"interactions-api-key":     true,
	"vertex-api-key":           true,
	"codex-api-key":            true,
	"claude-api-key":           true,
	"xai-api-key":              true,
	"meta-api-key":             true,
	"openai-compatibility":     true,
}

var protectedSourceSubtrees = map[string]map[string]bool{
	"server": {
		"host":         true,
		"port":         true,
		"server-id":    true,
		"discovery":    true,
		"tls":          true,
		"unix-socket":  true,
		"github-token": true,
		"kv-store":     true,
	},
	"oauth": {
		"auth-dir": true,
	},
}

var sensitiveSourceKeyNames = map[string]bool{
	"secret-key":     true,
	"secret":         true,
	"api-key":        true,
	"api-keys":       true,
	"management-key": true,
	"github-token":   true,
	"password":       true,
	"private-key":    true,
	"access-token":   true,
	"refresh-token":  true,
	"client-secret":  true,
}

// ReadSource projects the current CPA document as sanitized YAML for the source
// editor. Protected control-plane sections and secrets are stripped so they
// never leave the Manager.
func (s *Service) ReadSource(ctx context.Context) (SourceView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.store.Load(ctx)
	if err != nil {
		return SourceView{}, err
	}
	view := SourceView{
		Generation:    strconv.FormatUint(r.Operation.Strategy.TargetGeneration, 10),
		GroupsInvalid: r.ConfigGroupsInvalid,
	}
	auth, resolveErr := s.resolve(ctx, r, gatewayConfigScope)
	if errors.Is(resolveErr, cpa.ErrExternalRejectionUnavailable) {
		return SourceView{}, resolveErr
	}
	view.Capability = auth.capable

	var document map[string]any
	if auth.capable.Readable && auth.wire != nil {
		document, err = auth.wire.ReadConfigDocument(ctx, auth.secret)
		if err != nil {
			if auth.mode == ModeExternal && (errors.Is(err, cpa.ErrManagementRejected) || errors.Is(err, cpa.ErrExternalTargetMoved) || errors.Is(err, cpa.ErrExternalRejectionUnavailable)) {
				return SourceView{}, err
			}
			view.Capability.Readable = false
			view.Capability.Writable = false
			view.Capability.RulesWritable = false
			view.Capability.Reason = ReasonReadUnavailable
			document = nil
		}
	}

	var blockedReason string
	if !view.GroupsInvalid {
		if entry := r.ConfigGroups[ConfigSourceGroupID]; entry != nil {
			if operation, ok := entry.Operations[entry.Latest]; ok {
				copied := operation.Clone()
				view.Operation = &copied
			}
		}
		if blockingOp, reason, _ := blockingSourceOperation(r); reason != "" {
			blockedReason = reason
			if auth.mode != ModePrivate && (view.Operation == nil || view.Operation.State != "unknown") {
				view.Operation = blockingOp
			}
		}
	}

	if document != nil {
		sanitized := sanitizeSourceDocument(document)
		view.Baseline = canonicalDigest(sanitized)
		encoded, encErr := encodeSourceYAML(sanitized)
		if encErr != nil {
			return SourceView{}, encErr
		}
		view.YAML = encoded
	}

	switch {
	case !view.Capability.Readable:
		view.SourceWritable = false
		view.Reason = firstReason(view.Capability.Reason, ReasonUnavailable)
	case auth.mode == ModePrivate:
		view.SourceWritable = false
		view.Reason = ReasonPrivateFrozen
	case view.GroupsInvalid:
		view.SourceWritable = false
		view.Reason = ReasonJournalInvalid
	case blockedReason != "":
		view.SourceWritable = false
		view.Reason = blockedReason
	case !view.Capability.Writable:
		view.SourceWritable = false
		view.Reason = firstReason(view.Capability.Reason, ReasonUnavailable)
	default:
		view.SourceWritable = true
	}
	return view, nil
}

// ValidateSourceRequest validates the request shape, YAML syntax, and protected
// path boundaries before any journal entry or CPA write happens.
func ValidateSourceRequest(q SourceRequest) error {
	if err := validateExpected(q.Expected); err != nil {
		return err
	}
	if !freshkeysValidRequestID(q.RequestID) {
		return ErrInvalid
	}
	doc, err := parseStrictSourceYAML(q.YAML)
	if err != nil {
		return err
	}
	if badPath := findProtectedSourcePath(doc); badPath != "" {
		return Rejection{Code: CodeSourceProtectedPath, Field: badPath}
	}
	return validateSourceSchemaValues(doc)
}

// SaveSource computes the tree diff between the sanitized CPA baseline and the
// submitted YAML, then applies minimal PATCH / PUT / DELETE calls through CPA's
// v8 config endpoints under operation-journal protection.
func (s *Service) SaveSource(ctx context.Context, q SourceRequest) (freshstore.ConfigGroupOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateSourceRequest(q); err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}
	targetDoc, err := parseStrictSourceYAML(q.YAML)
	if err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}

	r, err := s.store.Load(ctx)
	if err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}
	auth, err := s.resolve(ctx, r, gatewayConfigScope)
	if err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}
	if auth.wire == nil {
		return freshstore.ConfigGroupOperation{}, cpa.ErrProofUnavailable
	}
	if err = s.externalWriteGate(ctx, auth); err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}
	if !auth.capable.Writable || auth.mode == ModePrivate {
		return freshstore.ConfigGroupOperation{}, ErrReadOnly
	}
	if err = checkGeneration(q.Expected, r.Operation.Strategy.TargetGeneration); err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}
	if r.ConfigGroupsInvalid {
		return freshstore.ConfigGroupOperation{}, cpa.ErrSourceConflict
	}

	targetPrefix := ConfigSourceGroupID + ":" + canonicalDigest(targetDoc)
	for id, entry := range r.ConfigGroups {
		if entry == nil {
			continue
		}
		if old, exists := entry.Operations[q.RequestID]; exists {
			if id != ConfigSourceGroupID || !sameSourceRequest(old, targetPrefix, q.Expected.Baseline) {
				return old, cpa.ErrSourceConflict
			}
			if old.State == "unknown" {
				return old, cpa.ErrApplyUnknown
			}
			return old, nil
		}
	}
	if _, _, blockErr := blockingSourceOperation(r); blockErr != nil {
		return freshstore.ConfigGroupOperation{}, blockErr
	}

	// Read live CPA document so baseline mismatch sees the live source state.
	document, err := auth.wire.ReadConfigDocument(ctx, auth.secret)
	if err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}
	baselineDoc := sanitizeSourceDocument(document)
	if canonicalDigest(baselineDoc) != q.Expected.Baseline {
		return freshstore.ConfigGroupOperation{}, cpa.ErrSourceConflict
	}
	if err := validateTargetKeyCaseAgainstRaw(document, targetDoc); err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}
	if !auth.capable.RulesWritable && targetDeletesOrNullsRequestsWithPayload(document, baselineDoc, targetDoc) {
		return freshstore.ConfigGroupOperation{}, ErrRulesReadOnly
	}

	edits, ops, err := planSourceChanges(document, baselineDoc, targetDoc)
	if err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}
	if len(edits) == 0 {
		return freshstore.ConfigGroupOperation{}, Rejection{Code: CodeSourceNoChanges}
	}
	if !auth.capable.RulesWritable && touchesPayloadRules(document, baselineDoc, targetDoc, edits) {
		return freshstore.ConfigGroupOperation{}, ErrRulesReadOnly
	}
	operation := freshstore.ConfigGroupOperation{
		RequestID: q.RequestID, Group: ConfigSourceGroupID, Target: "group", Prefix: targetPrefix,
		Baseline: q.Expected.Baseline, Edits: edits, State: "unknown",
		SourceID: r.Operation.Identity.InstallationID, Generation: q.Expected.Generation,
	}
	if err = s.appendGroupOperation(ctx, r.Operation.Identity, r.Operation.Revision, ConfigSourceGroupID, operation); err != nil {
		return operation, err
	}

	applied := false
	for _, op := range ops {
		switch op.kind {
		case sourceOpPatch:
			err = auth.wire.PatchConfigPath(ctx, auth.secret, op.path, op.payload)
		case sourceOpPut:
			err = auth.wire.PutConfigValue(ctx, auth.secret, op.path, op.payload)
		case sourceOpDelete:
			err = auth.wire.DeleteConfigPath(ctx, auth.secret, op.path)
		}
		if err != nil {
			return s.failGroupOperation(ctx, ConfigSourceGroupID, operation, err, applied)
		}
		applied = true
	}

	acknowledged := operation
	acknowledged.HTTP200 = true
	if err = s.updateGroupOperation(ctx, ConfigSourceGroupID, operation, acknowledged); err != nil {
		return operation, err
	}
	return s.resolveGroupOperation(ctx, ConfigSourceGroupID, auth, acknowledged)
}

func sameSourceRequest(operation freshstore.ConfigGroupOperation, targetPrefix, baseline string) bool {
	return operation.Target == "group" && operation.Prefix == targetPrefix &&
		operation.Baseline == baseline
}
func sanitizeSourceDocument(document map[string]any) map[string]any {
	out := map[string]any{}
	for key, val := range document {
		lower := strings.ToLower(key)
		if protectedSourceRoots[lower] || sensitiveSourceKeyNames[lower] {
			continue
		}
		if cpa.IsConfigKVMapLeaf(key) {
			out[key] = cloneJSONValue(val)
			continue
		}
		if subMap, ok := val.(map[string]any); ok {
			cleaned := sanitizeSourceSubtree(key, subMap)
			if len(subMap) == 0 || len(cleaned) > 0 {
				out[key] = cleaned
			}
			continue
		}
		if subSlice, ok := val.([]any); ok {
			out[key] = sanitizeSourceSlice(key, subSlice)
			continue
		}
		out[key] = cloneJSONValue(val)
	}
	return out
}

func sanitizeSourceSubtree(prefix string, mapping map[string]any) map[string]any {
	out := map[string]any{}
	subBlocked := protectedSourceSubtrees[strings.ToLower(prefix)]
	for key, val := range mapping {
		lower := strings.ToLower(key)
		if subBlocked != nil && subBlocked[lower] {
			continue
		}
		if sensitiveSourceKeyNames[lower] {
			continue
		}
		if strings.EqualFold(prefix, "requests") && lower == "proxy-url" {
			if errors.Is(cpa.ValidateProxyURL(val), cpa.ErrProxyURLCredentials) {
				continue
			}
		}
		path := prefix + "." + key
		if cpa.IsConfigKVMapLeaf(path) {
			out[key] = cloneJSONValue(val)
			continue
		}
		if child, ok := val.(map[string]any); ok {
			cleaned := sanitizeSourceSubtree(path, child)
			if len(child) == 0 || len(cleaned) > 0 {
				out[key] = cleaned
			}
			continue
		}
		if sliceVal, ok := val.([]any); ok {
			out[key] = sanitizeSourceSlice(path, sliceVal)
			continue
		}
		out[key] = cloneJSONValue(val)
	}
	return out
}

func sanitizeSourceSlice(prefix string, items []any) []any {
	out := make([]any, len(items))
	itemPrefix := prefix + "[]"
	for i, item := range items {
		switch typed := item.(type) {
		case map[string]any:
			out[i] = sanitizeSourceSubtree(itemPrefix, typed)
		case []any:
			out[i] = sanitizeSourceSlice(itemPrefix, typed)
		default:
			out[i] = cloneJSONValue(item)
		}
	}
	return out
}

func cloneJSONValue(val any) any {
	switch typed := val.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			out[k] = cloneJSONValue(v)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, v := range typed {
			out[i] = cloneJSONValue(v)
		}
		return out
	default:
		return val
	}
}

func findProtectedSourcePath(doc map[string]any) string {
	keys := sortedMapKeys(doc)
	for _, key := range keys {
		lower := strings.ToLower(key)
		if protectedSourceRoots[lower] || sensitiveSourceKeyNames[lower] {
			return key
		}
		if cpa.IsConfigKVMapLeaf(key) {
			continue
		}
		switch typed := doc[key].(type) {
		case map[string]any:
			if bad := findProtectedSubtreePath(key, key, typed); bad != "" {
				return bad
			}
		case []any:
			if bad := findProtectedSlicePath(key, key, typed); bad != "" {
				return bad
			}
		}
	}
	return ""
}

func findProtectedSubtreePath(reportPrefix, schemaPrefix string, mapping map[string]any) string {
	subBlocked := protectedSourceSubtrees[strings.ToLower(schemaPrefix)]
	for _, key := range sortedMapKeys(mapping) {
		lower := strings.ToLower(key)
		reportPath := reportPrefix + "." + key
		schemaPath := schemaPrefix + "." + key
		if (subBlocked != nil && subBlocked[lower]) || sensitiveSourceKeyNames[lower] {
			return reportPath
		}
		if cpa.IsConfigKVMapLeaf(schemaPath) {
			continue
		}
		switch typed := mapping[key].(type) {
		case map[string]any:
			if bad := findProtectedSubtreePath(reportPath, schemaPath, typed); bad != "" {
				return bad
			}
		case []any:
			if bad := findProtectedSlicePath(reportPath, schemaPath, typed); bad != "" {
				return bad
			}
		}
	}
	return ""
}

func findProtectedSlicePath(reportPrefix, schemaPrefix string, items []any) string {
	itemSchemaPrefix := schemaPrefix + "[]"
	for i, item := range items {
		itemReportPrefix := fmt.Sprintf("%s[%d]", reportPrefix, i)
		switch typed := item.(type) {
		case map[string]any:
			if bad := findProtectedSubtreePath(itemReportPrefix, itemSchemaPrefix, typed); bad != "" {
				return bad
			}
		case []any:
			if bad := findProtectedSlicePath(itemReportPrefix, itemSchemaPrefix, typed); bad != "" {
				return bad
			}
		}
	}
	return ""
}

func hasProtectedSubtree(path string) bool {
	lower := strings.ToLower(path)
	if protectedSourceSubtrees[lower] != nil {
		return true
	}
	prefix := lower + "."
	for blockedPrefix := range protectedSourceSubtrees {
		if strings.HasPrefix(blockedPrefix, prefix) {
			return true
		}
	}
	return false
}

func normalizeSourceLeafValue(val any) any {
	switch typed := val.(type) {
	case json.Number:
		if n, err := strconv.Atoi(typed.String()); err == nil {
			return n
		}
		return val
	case float64:
		return cpa.NormalizeConfigValue(typed)
	default:
		return val
	}
}

func validateSourceSchemaValues(doc map[string]any) error {
	for _, key := range sortedMapKeys(doc) {
		lower := strings.ToLower(key)
		if (lower == "server" || lower == "oauth") && doc[key] != nil {
			if _, ok := doc[key].(map[string]any); !ok {
				return Rejection{Code: CodeSourceInvalidYAML, Field: lower, Message: fmt.Sprintf("%s must be a mapping", lower)}
			}
		}
	}
	for _, leafPath := range cpa.GatewayPageLeafPaths() {
		var leafErr error
		forEachPathFold(doc, strings.Split(leafPath, "."), func(val any) {
			if leafErr != nil || val == nil {
				return
			}
			if err := cpa.ValidateConfigLeafValue(leafPath, normalizeSourceLeafValue(val)); err != nil {
				msg := "invalid configuration value"
				if cpa.IsConfigKVMapLeaf(leafPath) {
					msg = "invalid key-value mapping"
				}
				leafErr = Rejection{Code: CodeSourceInvalidYAML, Field: leafPath, Message: msg}
			}
		})
		if leafErr != nil {
			return leafErr
		}
	}
	for _, reqKey := range sortedMapKeys(doc) {
		if !strings.EqualFold(reqKey, "requests") {
			continue
		}
		rawRequests := doc[reqKey]
		if rawRequests == nil {
			continue
		}
		requestsMap, ok := rawRequests.(map[string]any)
		if !ok {
			return Rejection{Code: CodeSourceInvalidYAML, Field: "requests", Message: "requests must be a mapping"}
		}
		for _, childKey := range sortedMapKeys(requestsMap) {
			rawChild := requestsMap[childKey]
			if rawChild == nil {
				continue
			}
			switch {
			case strings.EqualFold(childKey, "proxy-url"):
				if err := cpa.ValidateProxyURL(rawChild); err != nil {
					if errors.Is(err, cpa.ErrProxyURLCredentials) {
						return Rejection{Code: CodeProxyURLCredentials, Field: "requests.proxy-url"}
					}
					return Rejection{Code: CodeSourceInvalidYAML, Field: "requests.proxy-url", Message: err.Error()}
				}
			case strings.EqualFold(childKey, "payload"):
				payloadMap, ok := rawChild.(map[string]any)
				if !ok {
					return Rejection{Code: CodeSourceInvalidYAML, Field: "requests.payload", Message: "requests.payload must be a mapping"}
				}
				for _, familyKey := range sortedMapKeys(payloadMap) {
					rawFamily := payloadMap[familyKey]
					if rawFamily == nil {
						continue
					}
					entry, known := cpa.LookupGatewayPageRuleFamily(strings.ToLower(familyKey))
					if !known {
						continue
					}
					encoded, err := json.Marshal(rawFamily)
					if err != nil {
						return Rejection{Code: CodeSourceInvalidYAML, Field: entry.Path, Message: "invalid payload rules"}
					}
					if _, err := cpa.ParseConfigRules(entry.Family, encoded); err != nil {
						return Rejection{Code: CodeSourceInvalidYAML, Field: entry.Path, Message: "invalid payload rules"}
					}
				}
			}
		}
	}
	return nil
}

func forEachPathFold(current any, parts []string, visit func(any)) {
	if len(parts) == 0 {
		visit(current)
		return
	}
	mapping, ok := current.(map[string]any)
	if !ok {
		return
	}
	for _, key := range sortedMapKeys(mapping) {
		if strings.EqualFold(key, parts[0]) {
			forEachPathFold(mapping[key], parts[1:], visit)
		}
	}
}

func encodeSourceYAML(doc map[string]any) (string, error) {
	if len(doc) == 0 {
		return "{}\n", nil
	}
	node, err := toYAMLNode(doc)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func toYAMLNode(value any) (*yaml.Node, error) {
	switch typed := value.(type) {
	case map[string]any:
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, key := range sortedMapKeys(typed) {
			kNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
			vNode, err := toYAMLNode(typed[key])
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, kNode, vNode)
		}
		return node, nil
	case []any:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range typed {
			itemNode, err := toYAMLNode(item)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, itemNode)
		}
		return node, nil
	case json.Number:
		raw := typed.String()
		if _, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: raw}, nil
		}
		if _, err := strconv.ParseUint(raw, 10, 64); err == nil {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: raw}, nil
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: raw}, nil
	case bool:
		if typed {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}, nil
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"}, nil
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: typed}, nil
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	default:
		// Fallback via JSON round-trip for typed slices/structs if ever present.
		raw, err := json.Marshal(typed)
		if err != nil {
			return nil, err
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var normalized any
		if err := dec.Decode(&normalized); err != nil {
			return nil, err
		}
		return toYAMLNode(normalized)
	}
}

func parseStrictSourceYAML(raw string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}, nil
	}
	dec := yaml.NewDecoder(strings.NewReader(raw))
	var root yaml.Node
	if err := dec.Decode(&root); err != nil {
		if errors.Is(err, io.EOF) {
			return map[string]any{}, nil
		}
		return nil, Rejection{Code: CodeSourceInvalidYAML, Message: "invalid YAML syntax"}
	}
	var second yaml.Node
	if err := dec.Decode(&second); err != io.EOF {
		return nil, Rejection{Code: CodeSourceInvalidYAML, Message: "multiple YAML documents are not allowed"}
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 {
		return nil, Rejection{Code: CodeSourceInvalidYAML, Message: "invalid YAML document"}
	}
	top := root.Content[0]
	if top.Kind == yaml.ScalarNode && top.Tag == "!!null" {
		return map[string]any{}, nil
	}
	if top.Kind != yaml.MappingNode {
		return nil, Rejection{Code: CodeSourceInvalidYAML, Message: "YAML root must be a mapping"}
	}
	val, err := decodeYAMLNode(top, "", false, 0)
	if err != nil {
		return nil, err
	}
	doc, ok := val.(map[string]any)
	if !ok {
		return nil, Rejection{Code: CodeSourceInvalidYAML, Message: "YAML root must be a mapping"}
	}
	return doc, nil
}

const maxYAMLDepth = 32

func sourceMappingKeyPolicy(path string, inDataValue bool) (allowDataKeys bool, valuesAreData bool) {
	if inDataValue {
		return true, true
	}
	lower := strings.ToLower(path)
	if cpa.IsConfigKVMapLeaf(lower) {
		return true, false
	}
	if strings.HasPrefix(lower, "requests.payload.") {
		rest := strings.TrimPrefix(lower, "requests.payload.")
		if family, tail, ok := strings.Cut(rest, "[]"); ok {
			if _, known := cpa.LookupGatewayPageRuleFamily(family); known {
				switch tail {
				case ".params", ".models[].match[]", ".models[].not-match[]":
					return true, true
				case ".models[].headers", ".replacements[]":
					return true, false
				}
			}
		}
	}
	return false, false
}

func decodeYAMLNode(node *yaml.Node, path string, inDataValue bool, depth int) (any, error) {
	if depth > maxYAMLDepth {
		return nil, Rejection{Code: CodeSourceInvalidYAML, Message: "YAML nesting depth exceeded"}
	}
	switch node.Kind {
	case yaml.AliasNode:
		return nil, Rejection{Code: CodeSourceInvalidYAML, Message: "YAML aliases are not allowed"}
	case yaml.MappingNode:
		if len(node.Content)%2 != 0 {
			return nil, Rejection{Code: CodeSourceInvalidYAML, Message: "invalid YAML mapping"}
		}
		allowDataKeys, valuesAreData := sourceMappingKeyPolicy(path, inDataValue)
		out := make(map[string]any, len(node.Content)/2)
		var seenLower map[string]struct{}
		if !allowDataKeys {
			seenLower = make(map[string]struct{}, len(node.Content)/2)
		}
		for i := 0; i < len(node.Content); i += 2 {
			kNode := node.Content[i]
			vNode := node.Content[i+1]
			if kNode.Kind != yaml.ScalarNode || kNode.Tag != "!!str" {
				return nil, Rejection{Code: CodeSourceInvalidYAML, Message: "YAML mapping keys must be strings"}
			}
			key := kNode.Value
			if allowDataKeys {
				if key == "" {
					return nil, Rejection{Code: CodeSourceInvalidYAML, Message: "YAML keys cannot be empty"}
				}
			} else if key == "" || strings.TrimSpace(key) != key || strings.ContainsAny(key, "./?#%\\") {
				return nil, Rejection{Code: CodeSourceInvalidYAML, Message: "YAML keys cannot be empty or contain dots"}
			}
			if _, dup := out[key]; dup {
				return nil, Rejection{Code: CodeSourceInvalidYAML, Message: fmt.Sprintf("duplicate YAML key: %s", key)}
			}
			if seenLower != nil {
				lowerKey := strings.ToLower(key)
				if _, dup := seenLower[lowerKey]; dup {
					return nil, Rejection{Code: CodeSourceInvalidYAML, Message: fmt.Sprintf("duplicate YAML key: %s", key)}
				}
				seenLower[lowerKey] = struct{}{}
			}
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			val, err := decodeYAMLNode(vNode, childPath, valuesAreData, depth+1)
			if err != nil {
				return nil, err
			}
			out[key] = val
		}
		return out, nil
	case yaml.SequenceNode:
		itemPath := path + "[]"
		out := make([]any, 0, len(node.Content))
		for _, item := range node.Content {
			val, err := decodeYAMLNode(item, itemPath, inDataValue, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, val)
		}
		return out, nil
	case yaml.ScalarNode:
		switch node.Tag {
		case "!!null":
			return nil, nil
		case "!!bool":
			var b bool
			if err := node.Decode(&b); err != nil {
				return nil, Rejection{Code: CodeSourceInvalidYAML, Message: "invalid boolean value"}
			}
			return b, nil
		case "!!int":
			if i, err := strconv.ParseInt(node.Value, 10, 64); err == nil {
				return json.Number(strconv.FormatInt(i, 10)), nil
			}
			if u, err := strconv.ParseUint(node.Value, 10, 64); err == nil {
				if u > math.MaxInt64 {
					return nil, Rejection{Code: CodeSourceInvalidYAML, Message: "integer value exceeds int64 range"}
				}
				return json.Number(strconv.FormatUint(u, 10)), nil
			}
			var i int64
			if err := node.Decode(&i); err != nil {
				return nil, Rejection{Code: CodeSourceInvalidYAML, Message: "invalid integer value"}
			}
			return json.Number(strconv.FormatInt(i, 10)), nil
		case "!!float":
			var f float64
			if err := node.Decode(&f); err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, Rejection{Code: CodeSourceInvalidYAML, Message: "invalid float value"}
			}
			raw := strconv.FormatFloat(f, 'g', -1, 64)
			if !strings.ContainsAny(raw, ".eE") {
				raw += ".0"
			}
			return json.Number(raw), nil
		case "!!str":
			return node.Value, nil
		default:
			// Refuse !!binary, !!timestamp or custom tags to preserve JSON fidelity.
			return nil, Rejection{Code: CodeSourceInvalidYAML, Message: fmt.Sprintf("unsupported YAML tag: %s", node.Tag)}
		}
	default:
		return nil, Rejection{Code: CodeSourceInvalidYAML, Message: "unsupported YAML node"}
	}
}

type sourceOpKind int

const (
	sourceOpPatch sourceOpKind = iota + 1
	sourceOpPut
	sourceOpDelete
)

type sourceWriteOp struct {
	kind    sourceOpKind
	path    string
	payload []byte
}

func planSourceChanges(rawDoc, baselineDoc, targetDoc map[string]any) ([]freshstore.ConfigGroupEdit, []sourceWriteOp, error) {
	if err := validateTargetKeyCaseAgainstRaw(rawDoc, targetDoc); err != nil {
		return nil, nil, err
	}
	var edits []freshstore.ConfigGroupEdit
	patchGroups := map[string]map[string]any{}
	var puts []sourceWriteOp
	var deletes []string

	recordSet := func(path string, val any) error {
		if val == nil {
			if rawPathHasProtectedOrStripped(rawDoc, path) || hasProtectedSubtree(path) {
				return Rejection{Code: CodeSourceProtectedPath, Field: path, Message: "cannot remove mapping containing protected keys"}
			}
			// Setting a leaf explicitly to null cannot satisfy ConfigGroupEdit
			// "set" non-nil contract; treat clearing an existing key as default
			// deletion and reject brand-new null keys cleanly.
			if _, presence := cpa.LookupConfigValue(baselineDoc, path); presence == cpa.ConfigPresent {
				edits = append(edits, freshstore.ConfigGroupEdit{Path: path, Action: "default"})
				deletes = append(deletes, path)
				return nil
			}
			return Rejection{Code: CodeSourceInvalidYAML, Field: path, Message: "null values cannot be added as new keys"}
		}
		parts := strings.Split(path, ".")
		if len(parts) == 1 && rawPathHasProtectedOrStripped(rawDoc, path) {
			return Rejection{Code: CodeSourceProtectedPath, Field: path, Message: "cannot overwrite mapping containing protected keys"}
		}
		if isPayloadRulePath(path) && rawPathHasProtectedOrStripped(rawDoc, path) {
			return Rejection{Code: CodeSourceProtectedPath, Field: path, Message: "cannot overwrite value containing protected keys"}
		}
		edits = append(edits, freshstore.ConfigGroupEdit{Path: path, Action: "set", Value: val})
		// Root sections, payload rule families, and KVMap user-data leaves use
		// atomic replacement PUT in CPA so omitted entries are removed cleanly.
		if len(parts) == 1 || isPayloadRulePath(path) || cpa.IsConfigKVMapLeaf(path) {
			encoded, err := json.Marshal(val)
			if err != nil {
				return err
			}
			puts = append(puts, sourceWriteOp{kind: sourceOpPut, path: path, payload: encoded})
			return nil
		}
		// Group non-rule set operations under their parent prefix for PATCH merge.
		parent := strings.Join(parts[:len(parts)-1], ".")
		leafKey := parts[len(parts)-1]
		bucket, ok := patchGroups[parent]
		if !ok {
			bucket = map[string]any{}
			patchGroups[parent] = bucket
		}
		bucket[leafKey] = val
		return nil
	}

	var walk func(prefix string, baseMap, nextMap map[string]any) error
	walk = func(prefix string, baseMap, nextMap map[string]any) error {
		// Deletions (or keys present in baselineDoc absent in targetDoc).
		for _, key := range sortedMapKeys(baseMap) {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			baseVal := baseMap[key]
			nextVal, exists := nextMap[key]
			if !exists {
				if baseChild, ok := baseVal.(map[string]any); ok {
					// If the deleted node is a map, expand to child deletes when its
					// subtree can hold protected siblings or had stripped keys in CPA.
					if hasProtectedSubtree(path) || rawPathHasProtectedOrStripped(rawDoc, path) {
						if err := walk(path, baseChild, map[string]any{}); err != nil {
							return err
						}
						continue
					}
				} else if rawPathHasProtectedOrStripped(rawDoc, path) {
					return Rejection{Code: CodeSourceProtectedPath, Field: path, Message: "cannot remove value containing protected keys"}
				}
				edits = append(edits, freshstore.ConfigGroupEdit{Path: path, Action: "default"})
				deletes = append(deletes, path)
				continue
			}
			if cpa.IsConfigKVMapLeaf(path) {
				if canonicalDigest(baseVal) != canonicalDigest(nextVal) {
					if err := recordSet(path, nextVal); err != nil {
						return err
					}
				}
				continue
			}
			baseChild, baseIsMap := baseVal.(map[string]any)
			nextChild, nextIsMap := nextVal.(map[string]any)
			if baseIsMap && nextIsMap {
				if err := walk(path, baseChild, nextChild); err != nil {
					return err
				}
				continue
			}
			if baseIsMap && !nextIsMap {
				if rawPathHasProtectedOrStripped(rawDoc, path) || hasProtectedSubtree(path) {
					return Rejection{Code: CodeSourceProtectedPath, Field: path, Message: "cannot overwrite or remove mapping containing protected keys"}
				}
			}
			if canonicalDigest(baseVal) != canonicalDigest(nextVal) {
				if !baseIsMap && rawPathHasProtectedOrStripped(rawDoc, path) {
					return Rejection{Code: CodeSourceProtectedPath, Field: path, Message: "cannot overwrite value containing protected keys"}
				}
				if nextIsMap {
					if err := validateNoNilSourceMapLeaves(path, nextChild); err != nil {
						return err
					}
					if err := recordSet(path, nextChild); err != nil {
						return err
					}
					continue
				}
				if err := recordSet(path, nextVal); err != nil {
					return err
				}
			}
		}

		// Additions present in targetDoc absent in baseMap.
		for _, key := range sortedMapKeys(nextMap) {
			if _, exists := baseMap[key]; exists {
				continue
			}
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			nextVal := nextMap[key]
			if cpa.IsConfigKVMapLeaf(path) {
				if err := recordSet(path, nextVal); err != nil {
					return err
				}
				continue
			}
			if nextChild, ok := nextVal.(map[string]any); ok {
				if len(nextChild) == 0 {
					if rawPathHasProtectedOrStripped(rawDoc, path) {
						return Rejection{Code: CodeSourceProtectedPath, Field: path, Message: "cannot overwrite mapping containing protected keys"}
					}
					// Empty map addition: only meaningful if not already empty.
					if err := recordSet(path, nextChild); err != nil {
						return err
					}
					continue
				}
				if err := walk(path, map[string]any{}, nextChild); err != nil {
					return err
				}
				continue
			}
			if rawPathHasProtectedOrStripped(rawDoc, path) || (nextVal == nil && hasProtectedSubtree(path)) {
				return Rejection{Code: CodeSourceProtectedPath, Field: path, Message: "cannot overwrite mapping containing protected keys"}
			}
			if err := recordSet(path, nextVal); err != nil {
				return err
			}
		}
		return nil
	}

	if err := walk("", baselineDoc, targetDoc); err != nil {
		return nil, nil, err
	}

	sort.Slice(edits, func(i, j int) bool { return edits[i].Path < edits[j].Path })

	var ops []sourceWriteOp
	patchPrefixes := sortedMapKeys(patchGroups)
	for _, prefix := range patchPrefixes {
		encoded, err := json.Marshal(patchGroups[prefix])
		if err != nil {
			return nil, nil, err
		}
		ops = append(ops, sourceWriteOp{kind: sourceOpPatch, path: prefix, payload: encoded})
	}
	sort.Slice(puts, func(i, j int) bool { return puts[i].path < puts[j].path })
	ops = append(ops, puts...)
	sort.Strings(deletes)
	for _, path := range deletes {
		ops = append(ops, sourceWriteOp{kind: sourceOpDelete, path: path})
	}
	return edits, ops, nil
}

func validateTargetKeyCaseAgainstRaw(rawDoc, targetDoc map[string]any) error {
	return checkTargetMapKeyCaseAgainstRaw("", "", false, rawDoc, targetDoc)
}

func checkTargetMapKeyCaseAgainstRaw(
	reportPrefix string,
	schemaPrefix string,
	inDataValue bool,
	rawMap map[string]any,
	targetMap map[string]any,
) error {
	if len(rawMap) == 0 || len(targetMap) == 0 {
		return nil
	}
	allowDataKeys, valuesAreData := sourceMappingKeyPolicy(schemaPrefix, inDataValue)
	if valuesAreData {
		return nil
	}
	for _, targetKey := range sortedMapKeys(targetMap) {
		targetReportPath := targetKey
		if reportPrefix != "" {
			targetReportPath = reportPrefix + "." + targetKey
		}
		targetSchemaPath := targetKey
		if schemaPrefix != "" {
			targetSchemaPath = schemaPrefix + "." + targetKey
		}
		if allowDataKeys {
			rawVal, exists := rawMap[targetKey]
			if !exists {
				continue
			}
			if err := checkTargetValueKeyCaseAgainstRaw(targetReportPath, targetSchemaPath, valuesAreData, rawVal, targetMap[targetKey]); err != nil {
				return err
			}
			continue
		}
		for _, rawKey := range sortedMapKeys(rawMap) {
			if !strings.EqualFold(targetKey, rawKey) {
				continue
			}
			rawVal := rawMap[rawKey]
			if targetKey != rawKey {
				rawSchemaPath := rawKey
				if schemaPrefix != "" {
					rawSchemaPath = schemaPrefix + "." + rawKey
				}
				if isProtectedOrStrippedRawKey(schemaPrefix, rawKey, rawSchemaPath, rawVal) {
					return Rejection{
						Code:    CodeSourceProtectedPath,
						Field:   targetReportPath,
						Message: fmt.Sprintf("key %q collides case-insensitively with protected key %q", targetKey, rawKey),
					}
				}
				return Rejection{
					Code:    CodeSourceInvalidYAML,
					Field:   targetReportPath,
					Message: fmt.Sprintf("key %q collides case-insensitively with existing key %q", targetKey, rawKey),
				}
			}
			if err := checkTargetValueKeyCaseAgainstRaw(targetReportPath, targetSchemaPath, valuesAreData, rawVal, targetMap[targetKey]); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkTargetValueKeyCaseAgainstRaw(
	reportPath string,
	schemaPath string,
	inDataValue bool,
	rawVal any,
	targetVal any,
) error {
	if inDataValue {
		return nil
	}
	if targetChild, ok := targetVal.(map[string]any); ok {
		if rawChild, rawOK := rawVal.(map[string]any); rawOK {
			return checkTargetMapKeyCaseAgainstRaw(reportPath, schemaPath, inDataValue, rawChild, targetChild)
		}
		return nil
	}
	if targetSlice, ok := targetVal.([]any); ok {
		rawSlice, rawOK := rawVal.([]any)
		if !rawOK {
			return nil
		}
		itemSchemaPath := schemaPath + "[]"
		for i := 0; i < len(targetSlice) && i < len(rawSlice); i++ {
			itemReportPath := fmt.Sprintf("%s[%d]", reportPath, i)
			if err := checkTargetValueKeyCaseAgainstRaw(itemReportPath, itemSchemaPath, inDataValue, rawSlice[i], targetSlice[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

func isProtectedOrStrippedRawKey(schemaPrefix, rawKey, rawSchemaPath string, rawVal any) bool {
	lowerPrefix := strings.ToLower(schemaPrefix)
	lowerRawKey := strings.ToLower(rawKey)
	if schemaPrefix == "" && protectedSourceRoots[lowerRawKey] {
		return true
	}
	if subBlocked := protectedSourceSubtrees[lowerPrefix]; subBlocked != nil && subBlocked[lowerRawKey] {
		return true
	}
	if sensitiveSourceKeyNames[lowerRawKey] {
		return true
	}
	if hasProtectedSubtree(rawSchemaPath) {
		return true
	}
	return rawNodeHasProtectedOrStripped(rawSchemaPath, rawVal)
}

func rawPathHasProtectedOrStripped(rawDoc map[string]any, dotPath string) bool {
	if len(rawDoc) == 0 || dotPath == "" {
		return false
	}
	parts := strings.Split(dotPath, ".")
	found := false
	forEachRawPathFold(rawDoc, "", parts, func(matchedPath string, val any) {
		if found {
			return
		}
		if rawNodeHasProtectedOrStripped(matchedPath, val) {
			found = true
		}
	})
	return found
}

func forEachRawPathFold(current any, prefix string, parts []string, visit func(string, any)) {
	if len(parts) == 0 {
		visit(prefix, current)
		return
	}
	mapping, ok := current.(map[string]any)
	if !ok {
		return
	}
	for key, val := range mapping {
		if strings.EqualFold(key, parts[0]) {
			nextPrefix := key
			if prefix != "" {
				nextPrefix = prefix + "." + key
			}
			forEachRawPathFold(val, nextPrefix, parts[1:], visit)
		}
	}
}

func rawNodeHasProtectedOrStripped(prefix string, val any) bool {
	if cpa.IsConfigKVMapLeaf(prefix) {
		return false
	}
	if strings.EqualFold(prefix, "requests.proxy-url") && errors.Is(cpa.ValidateProxyURL(val), cpa.ErrProxyURLCredentials) {
		return true
	}
	switch typed := val.(type) {
	case map[string]any:
		subBlocked := protectedSourceSubtrees[strings.ToLower(prefix)]
		for key, child := range typed {
			lower := strings.ToLower(key)
			if prefix == "" && protectedSourceRoots[lower] {
				return true
			}
			if subBlocked != nil && subBlocked[lower] {
				return true
			}
			if sensitiveSourceKeyNames[lower] {
				return true
			}
			if strings.EqualFold(prefix, "requests") && lower == "proxy-url" {
				if errors.Is(cpa.ValidateProxyURL(child), cpa.ErrProxyURLCredentials) {
					return true
				}
			}
			childPath := key
			if prefix != "" {
				childPath = prefix + "." + key
			}
			if rawNodeHasProtectedOrStripped(childPath, child) {
				return true
			}
		}
		return false
	case []any:
		itemPrefix := prefix + "[]"
		for _, item := range typed {
			if rawNodeHasProtectedOrStripped(itemPrefix, item) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func isPayloadRulePath(path string) bool {
	lower := strings.ToLower(path)
	if _, ok := cpa.LookupGatewayPageRuleFamily(strings.TrimPrefix(lower, "requests.payload.")); ok {
		return strings.HasPrefix(lower, "requests.payload.")
	}
	return lower == "requests.payload"
}

func docHasPayloadSubtree(doc map[string]any) bool {
	for key, val := range doc {
		if !strings.EqualFold(key, "requests") {
			continue
		}
		reqMap, ok := val.(map[string]any)
		if !ok {
			continue
		}
		for childKey := range reqMap {
			if strings.EqualFold(childKey, "payload") {
				return true
			}
		}
	}
	return false
}

func touchesPayloadRules(rawDoc, baselineDoc, targetDoc map[string]any, edits []freshstore.ConfigGroupEdit) bool {
	hasPayload := docHasPayloadSubtree(rawDoc) || docHasPayloadSubtree(baselineDoc) || docHasPayloadSubtree(targetDoc)
	for _, edit := range edits {
		lower := strings.ToLower(edit.Path)
		if strings.HasPrefix(lower, "requests.payload.") || lower == "requests.payload" {
			return true
		}
		if lower == "requests" && hasPayload {
			return true
		}
	}
	return false
}

func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func targetDeletesOrNullsRequestsWithPayload(rawDoc, baselineDoc, targetDoc map[string]any) bool {
	if !docHasPayloadSubtree(rawDoc) && !docHasPayloadSubtree(baselineDoc) {
		return false
	}
	for key, val := range targetDoc {
		if strings.EqualFold(key, "requests") {
			return val == nil
		}
	}
	return true
}

func validateNoNilSourceMapLeaves(prefix string, m map[string]any) error {
	if cpa.IsConfigKVMapLeaf(prefix) || isPayloadRulePath(prefix) {
		return nil
	}
	for _, key := range sortedMapKeys(m) {
		path := prefix + "." + key
		val := m[key]
		if val == nil {
			return Rejection{Code: CodeSourceInvalidYAML, Field: path, Message: "null values cannot be added as new keys"}
		}
		if child, ok := val.(map[string]any); ok {
			if err := validateNoNilSourceMapLeaves(path, child); err != nil {
				return err
			}
		}
	}
	return nil
}
