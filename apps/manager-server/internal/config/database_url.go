package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const usageDBURLEnvKey = "USAGE_DB_URL"
const usageDBPathEnvKey = "USAGE_DB_PATH"

type databaseURL struct {
	dataSourceName string
	path           string
	journalMode    string
	synchronous    int
	busyTimeout    int
}

type databasePragmas struct {
	journalMode string
	synchronous int
	busyTimeout int
}

func resolveDatabaseConfig(cfgFile fileConfig, cfgDir string, dataDir string) (databaseURL, error) {
	envURL := strings.TrimSpace(os.Getenv(usageDBURLEnvKey))
	envPath := strings.TrimSpace(os.Getenv(usageDBPathEnvKey))
	if envURL != "" && envPath != "" {
		return databaseURL{}, fmt.Errorf("%s and %s cannot both be set", usageDBURLEnvKey, usageDBPathEnvKey)
	}
	if envURL != "" {
		return parseDatabaseURL(envURL)
	}
	if envPath != "" {
		return databaseURL{path: envPath}, nil
	}

	fallbackPath := filepath.Join(dataDir, "usage.sqlite")
	if hasEnv("USAGE_DATA_DIR") {
		return databaseURL{path: fallbackPath}, nil
	}

	fileURL := strings.TrimSpace(cfgFile.DBURL)
	filePath := strings.TrimSpace(cfgFile.DBPath)
	if fileURL != "" && filePath != "" {
		return databaseURL{}, fmt.Errorf("config fields dbUrl and dbPath cannot both be set")
	}
	if fileURL != "" {
		return parseDatabaseURL(fileURL)
	}
	if filePath != "" {
		return databaseURL{path: resolveConfigPath(filePath, cfgDir)}, nil
	}
	return databaseURL{path: fallbackPath}, nil
}

func parseDatabaseURL(value string) (databaseURL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return databaseURL{}, fmt.Errorf("parse SQLite database URL: %w", sanitizedURLParseError(err))
	}
	if !strings.EqualFold(parsed.Scheme, "file") {
		return databaseURL{}, fmt.Errorf("SQLite database URL scheme must be file")
	}
	if parsed.Opaque != "" {
		return databaseURL{}, fmt.Errorf("SQLite database URL must use hierarchical file URI syntax")
	}
	if parsed.User != nil {
		return databaseURL{}, fmt.Errorf("SQLite database URL must not contain user information")
	}
	if parsed.Host != "" {
		return databaseURL{}, fmt.Errorf("SQLite database URL must not contain a host")
	}
	if parsed.Fragment != "" {
		return databaseURL{}, fmt.Errorf("SQLite database URL must not contain a fragment")
	}
	path, err := databaseURLPath(parsed)
	if err != nil {
		return databaseURL{}, err
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return databaseURL{}, fmt.Errorf("parse SQLite database URL query: %w", sanitizedURLParseError(err))
	}
	if err := validateDatabaseURIOptions(query); err != nil {
		return databaseURL{}, err
	}
	pragmas, err := validateDatabaseURLQuery(query)
	if err != nil {
		return databaseURL{}, err
	}
	return databaseURL{
		dataSourceName: strings.TrimSpace(value),
		path:           path,
		journalMode:    pragmas.journalMode,
		synchronous:    pragmas.synchronous,
		busyTimeout:    pragmas.busyTimeout,
	}, nil
}

func databaseURLPath(parsed *url.URL) (string, error) {
	uriPath := parsed.Path
	if uriPath == "" || strings.EqualFold(uriPath, ":memory:") || strings.EqualFold(uriPath, "/:memory:") {
		return "", fmt.Errorf("SQLite database URL must reference a persistent file")
	}
	if strings.HasPrefix(uriPath, "//") {
		return "", fmt.Errorf("SQLite database URL must not reference a network path")
	}
	path := filepath.FromSlash(uriPath)
	if runtime.GOOS == "windows" && len(path) >= 3 && (path[0] == '\\' || path[0] == '/') && path[2] == ':' {
		path = path[1:]
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("SQLite database URL path must be absolute")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve SQLite database URL path: %w", err)
	}
	return filepath.Clean(absolute), nil
}

func validateDatabaseURIOptions(query url.Values) error {
	for _, value := range queryValuesFold(query, "mode") {
		if strings.EqualFold(strings.TrimSpace(value), "memory") {
			return fmt.Errorf("SQLite database URL mode must reference a persistent file")
		}
	}
	for _, key := range []string{"immutable", "nolock"} {
		for _, value := range queryValuesFold(query, key) {
			if !isExplicitFalse(value) {
				return fmt.Errorf("SQLite database URL must not enable %s", key)
			}
		}
	}
	if len(queryValuesFold(query, "vfs")) > 0 {
		return fmt.Errorf("SQLite database URL must not select a custom VFS")
	}
	return nil
}

func validateDatabaseURLQuery(query url.Values) (databasePragmas, error) {
	txLocks := query["_txlock"]
	if len(txLocks) != 1 || !strings.EqualFold(strings.TrimSpace(txLocks[0]), "immediate") {
		return databasePragmas{}, fmt.Errorf("SQLite database URL must set _txlock=immediate")
	}

	required := map[string]string{}
	for index, raw := range query["_pragma"] {
		name, value, ok := splitPragma(raw)
		if !ok {
			return databasePragmas{}, fmt.Errorf("SQLite database URL contains invalid _pragma at position %d", index+1)
		}
		name = pragmaBaseName(name)
		switch name {
		case "foreign_keys", "journal_mode", "synchronous", "busy_timeout":
			if _, exists := required[name]; exists {
				return databasePragmas{}, fmt.Errorf("SQLite database URL contains duplicate %s pragma", name)
			}
			required[name] = value
		}
	}

	if value := strings.ToLower(required["foreign_keys"]); value != "1" && value != "on" {
		return databasePragmas{}, fmt.Errorf("SQLite database URL must set foreign_keys(1) or foreign_keys(ON)")
	}
	journalMode := strings.ToLower(required["journal_mode"])
	if journalMode != "wal" && journalMode != "delete" {
		return databasePragmas{}, fmt.Errorf("SQLite database URL journal_mode must be WAL or DELETE")
	}
	synchronous := 0
	switch strings.ToLower(required["synchronous"]) {
	case "full":
		synchronous = 2
	case "extra":
		synchronous = 3
	default:
		return databasePragmas{}, fmt.Errorf("SQLite database URL synchronous must be FULL or EXTRA")
	}
	busyTimeout, err := strconv.Atoi(strings.TrimSpace(required["busy_timeout"]))
	if err != nil || busyTimeout <= 0 {
		return databasePragmas{}, fmt.Errorf("SQLite database URL busy_timeout must be a positive integer")
	}
	return databasePragmas{
		journalMode: journalMode,
		synchronous: synchronous,
		busyTimeout: busyTimeout,
	}, nil
}

func splitPragma(raw string) (string, string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, ";\r\n\x00") {
		return "", "", false
	}
	var name string
	var value string
	if open := strings.IndexByte(raw, '('); open > 0 && strings.HasSuffix(raw, ")") {
		name = strings.TrimSpace(raw[:open])
		value = strings.TrimSpace(raw[open+1 : len(raw)-1])
	} else if equals := strings.IndexByte(raw, '='); equals > 0 {
		name = strings.TrimSpace(raw[:equals])
		value = strings.TrimSpace(raw[equals+1:])
	} else {
		return "", "", false
	}
	if !validPragmaName(name) || value == "" {
		return "", "", false
	}
	return strings.ToLower(name), value, true
}

func validPragmaName(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func pragmaBaseName(name string) string {
	if separator := strings.LastIndexByte(name, '.'); separator >= 0 {
		return name[separator+1:]
	}
	return name
}

func queryValuesFold(query url.Values, key string) []string {
	var values []string
	for candidate, candidateValues := range query {
		if strings.EqualFold(candidate, key) {
			values = append(values, candidateValues...)
		}
	}
	return values
}

func sanitizedURLParseError(err error) error {
	var urlError *url.Error
	if errors.As(err, &urlError) && urlError.Err != nil {
		return urlError.Err
	}
	return err
}

func isExplicitFalse(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no", "off":
		return true
	default:
		return false
	}
}
