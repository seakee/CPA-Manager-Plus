package cpa

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// Rule entries are validated for shape and for the one product rule CPAMP owns:
// a rule may not name the same path twice. CPA owns every semantic bound.

// ParseConfigRules decodes one whole payload rule family and rejects anything
// that is not an array of well-shaped rules for that family.
func ParseConfigRules(family string, raw json.RawMessage) ([]any, error) {
	entry, ok := gatewayPageFamilyIndex[family]
	if !ok {
		return nil, ErrInvalidConfigPath
	}
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, ErrInvalidConfigPath
	}
	rules, err := decodeConfigRules(raw)
	if err != nil {
		return nil, ErrInvalidConfigPath
	}
	encoded, err := json.Marshal(rules)
	if err != nil {
		return nil, ErrInvalidConfigPath
	}
	return rules, ValidateConfigRules(entry, encoded)
}

// decodeConfigRules keeps numbers as json.Number so a 64-bit integer in a rule
// parameter survives the round trip instead of collapsing into a float64 and
// losing its low digits. json.Number marshals back as the original literal.
func decodeConfigRules(raw []byte) ([]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var rules []any
	if decoder.Decode(&rules) != nil {
		return nil, ErrInvalidConfigPath
	}
	if rules == nil {
		return nil, ErrInvalidConfigPath
	}
	// Trailing data would make the value we validated differ from the value sent.
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrInvalidConfigPath
	}
	return rules, nil
}

// ValidateConfigRules re-checks an already-decoded family. The wire form is used
// so a caller cannot validate one shape and send another.
func ValidateConfigRules(entry ConfigRuleFamily, raw []byte) error {
	rules, err := decodeConfigRules(raw)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if err := validateConfigRule(entry, rule); err != nil {
			return err
		}
	}
	return nil
}

func validateConfigRule(entry ConfigRuleFamily, rule any) error {
	object, ok := rule.(map[string]any)
	if !ok {
		return ErrInvalidConfigPath
	}
	if entry.Replace {
		return validateReplaceConfigRule(object)
	}
	for key := range object {
		switch key {
		case "models", "params":
		default:
			return ErrInvalidConfigPath
		}
	}
	models, ok := object["models"].([]any)
	if !ok || len(models) == 0 {
		return ErrInvalidConfigPath
	}
	for _, model := range models {
		if err := validateConfigModel(model); err != nil {
			return err
		}
	}
	params, present := object["params"]
	if !present {
		return ErrInvalidConfigPath
	}
	if entry.Filter {
		paths, ok := params.([]any)
		if !ok {
			return ErrInvalidConfigPath
		}
		return rejectDuplicatePaths(paths)
	}
	values, ok := params.(map[string]any)
	if !ok {
		return ErrInvalidConfigPath
	}
	for path, value := range values {
		if strings.TrimSpace(path) == "" || path != strings.TrimSpace(path) {
			return ErrInvalidConfigPath
		}
		if entry.Raw {
			// A raw family stores the value as a JSON document in a string.
			text, ok := value.(string)
			if !ok || !json.Valid([]byte(text)) {
				return ErrInvalidConfigPath
			}
		}
	}
	return nil
}

func validateReplaceConfigRule(object map[string]any) error {
	for key := range object {
		switch key {
		case "models", "target", "replacements":
		default:
			return ErrInvalidConfigPath
		}
	}
	models, ok := object["models"].([]any)
	if !ok || len(models) == 0 {
		return ErrInvalidConfigPath
	}
	for _, model := range models {
		if err := validateConfigModel(model); err != nil {
			return err
		}
	}
	target, ok := object["target"].(map[string]any)
	if !ok {
		return ErrInvalidConfigPath
	}
	for key := range target {
		switch key {
		case "role", "positions", "all", "paths":
		default:
			return ErrInvalidConfigPath
		}
	}
	var role string
	if rawRole, hasRole := target["role"]; hasRole && rawRole != nil {
		s, ok := rawRole.(string)
		if !ok {
			return ErrInvalidConfigPath
		}
		role = s
	}
	allVal, hasAll := target["all"]
	allFlag := false
	if hasAll {
		flag, ok := allVal.(bool)
		if !ok {
			return ErrInvalidConfigPath
		}
		allFlag = flag
	}
	posVal, hasPos := target["positions"]
	var positions []any
	if hasPos && posVal != nil {
		list, ok := posVal.([]any)
		if !ok {
			return ErrInvalidConfigPath
		}
		positions = list
		for _, rawPos := range positions {
			if !isPositiveConfigInt(rawPos) {
				return ErrInvalidConfigPath
			}
		}
	}
	pathsVal, hasPaths := target["paths"]
	if hasPaths && pathsVal != nil {
		paths, err := parseReplaceTargetPaths(pathsVal)
		if err != nil || len(paths) == 0 {
			return ErrInvalidConfigPath
		}
		if role != "" || allFlag || len(positions) != 0 {
			return ErrInvalidConfigPath
		}
	} else {
		switch role {
		case "system", "developer", "user":
		default:
			return ErrInvalidConfigPath
		}
		if allFlag {
			if len(positions) != 0 {
				return ErrInvalidConfigPath
			}
		} else if len(positions) == 0 {
			return ErrInvalidConfigPath
		}
	}
	replacements, ok := object["replacements"].([]any)
	if !ok || len(replacements) == 0 {
		return ErrInvalidConfigPath
	}
	for _, rawEntry := range replacements {
		if err := validateReplaceEntry(rawEntry); err != nil {
			return err
		}
	}
	return nil
}

func isPositiveConfigInt(raw any) bool {
	switch typed := raw.(type) {
	case json.Number:
		n, err := typed.Int64()
		return err == nil && n > 0
	case int:
		return typed > 0
	case int64:
		return typed > 0
	case float64:
		return typed > 0 && typed == float64(int64(typed))
	default:
		return false
	}
}

func parseReplaceTargetPaths(raw any) ([]string, error) {
	switch typed := raw.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if strings.TrimSpace(item) == "" {
				return nil, ErrInvalidConfigPath
			}
			out = append(out, item)
		}
		return out, nil
	case []any:
		out := make([]string, 0, len(typed))
		for _, rawItem := range typed {
			item, ok := rawItem.(string)
			if !ok || strings.TrimSpace(item) == "" {
				return nil, ErrInvalidConfigPath
			}
			out = append(out, item)
		}
		return out, nil
	default:
		return nil, ErrInvalidConfigPath
	}
}

func validateReplaceEntry(raw any) error {
	switch typed := raw.(type) {
	case map[string]string:
		switch len(typed) {
		case 1:
			for k := range typed {
				if k == "" {
					return ErrInvalidConfigPath
				}
			}
			return nil
		case 2:
			if _, hasPattern := typed["pattern"]; !hasPattern {
				return ErrInvalidConfigPath
			}
			if _, hasReplacement := typed["replacement"]; !hasReplacement {
				return ErrInvalidConfigPath
			}
			return nil
		default:
			return ErrInvalidConfigPath
		}
	case map[string]any:
		switch len(typed) {
		case 1:
			for k, v := range typed {
				if k == "" {
					return ErrInvalidConfigPath
				}
				if _, ok := v.(string); !ok {
					return ErrInvalidConfigPath
				}
			}
			return nil
		case 2:
			rawPattern, hasPattern := typed["pattern"]
			rawReplacement, hasReplacement := typed["replacement"]
			if !hasPattern || !hasReplacement {
				return ErrInvalidConfigPath
			}
			if _, ok := rawPattern.(string); !ok {
				return ErrInvalidConfigPath
			}
			if _, ok := rawReplacement.(string); !ok {
				return ErrInvalidConfigPath
			}
			return nil
		default:
			return ErrInvalidConfigPath
		}
	default:
		return ErrInvalidConfigPath
	}
}
func validateConfigModel(model any) error {
	object, ok := model.(map[string]any)
	if !ok {
		return ErrInvalidConfigPath
	}
	for key, value := range object {
		switch key {
		case "name":
			name, ok := value.(string)
			if !ok || strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
				return ErrInvalidConfigPath
			}
		case "protocol", "from-protocol":
			if _, ok := value.(string); !ok {
				return ErrInvalidConfigPath
			}
		case "headers":
			headers, ok := value.(map[string]any)
			if !ok {
				return ErrInvalidConfigPath
			}
			seen := map[string]bool{}
			for name, header := range headers {
				if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
					return ErrInvalidConfigPath
				}
				if _, ok := header.(string); !ok {
					return ErrInvalidConfigPath
				}
				folded := strings.ToLower(name)
				if seen[folded] {
					return ErrInvalidConfigPath
				}
				seen[folded] = true
			}
		case "match", "not-match":
			entries, ok := value.([]any)
			if !ok {
				return ErrInvalidConfigPath
			}
			seen := map[string]bool{}
			for _, item := range entries {
				condition, ok := item.(map[string]any)
				if !ok || len(condition) != 1 {
					return ErrInvalidConfigPath
				}
				for path := range condition {
					if strings.TrimSpace(path) == "" || path != strings.TrimSpace(path) || seen[path] {
						return ErrInvalidConfigPath
					}
					seen[path] = true
				}
			}
		case "exist", "not-exist":
			entries, ok := value.([]any)
			if !ok {
				return ErrInvalidConfigPath
			}
			if err := rejectDuplicatePaths(entries); err != nil {
				return err
			}
		default:
			return ErrInvalidConfigPath
		}
	}
	return nil
}

func rejectDuplicatePaths(entries []any) error {
	seen := map[string]bool{}
	for _, entry := range entries {
		path, ok := entry.(string)
		if !ok || strings.TrimSpace(path) == "" || path != strings.TrimSpace(path) {
			return ErrInvalidConfigPath
		}
		if seen[path] {
			return ErrInvalidConfigPath
		}
		seen[path] = true
	}
	return nil
}
