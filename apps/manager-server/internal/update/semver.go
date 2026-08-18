package update

import (
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

var releaseVersionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

func NormalizeVersion(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "dev") {
		return ""
	}
	if !strings.HasPrefix(value, "v") {
		value = "v" + value
	}
	if !releaseVersionPattern.MatchString(value) || !semver.IsValid(value) {
		return ""
	}
	return semver.Canonical(value)
}

func CompareVersions(current, latest string) (int, bool) {
	current = NormalizeVersion(current)
	latest = NormalizeVersion(latest)
	if current == "" || latest == "" {
		return 0, false
	}
	return semver.Compare(latest, current), true
}

func VersionAtLeast(current, minimum string) bool {
	current = NormalizeVersion(current)
	minimum = NormalizeVersion(minimum)
	return current != "" && minimum != "" && semver.Compare(current, minimum) >= 0
}
