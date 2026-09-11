package updatecheck

import (
	"errors"
	"regexp"
	"strings"
)

var versionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)
var numeric = regexp.MustCompile(`^[0-9]+$`)

type Version struct {
	core  []string
	pre   []string
	Stage string
}

func ParseVersion(tag string) (Version, error) {
	m := versionPattern.FindStringSubmatch(tag)
	if m == nil {
		return Version{}, errors.New("invalid semantic version")
	}
	v := Version{core: m[1:4], Stage: "stable"}
	if m[4] != "" {
		v.pre = strings.Split(m[4], ".")
		v.Stage = ""
		for _, p := range v.pre {
			if numeric.MatchString(p) && len(p) > 1 && p[0] == '0' {
				return Version{}, errors.New("leading zero in prerelease")
			}
		}
		if len(v.pre) == 2 && (v.pre[0] == "beta" || v.pre[0] == "rc") && numeric.MatchString(v.pre[1]) {
			v.Stage = v.pre[0]
		}
	}
	return v, nil
}
func numberCompare(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}
func Compare(a, b Version) int {
	for i := 0; i < 3; i++ {
		if c := numberCompare(a.core[i], b.core[i]); c != 0 {
			return c
		}
	}
	if len(a.pre) == 0 && len(b.pre) == 0 {
		return 0
	}
	if len(a.pre) == 0 {
		return 1
	}
	if len(b.pre) == 0 {
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, y := a.pre[i], b.pre[i]
		c := 0
		switch {
		case numeric.MatchString(x) && numeric.MatchString(y):
			c = numberCompare(x, y)
		case numeric.MatchString(x):
			c = -1
		case numeric.MatchString(y):
			c = 1
		default:
			c = strings.Compare(x, y)
		}
		if c != 0 {
			return c
		}
	}
	if len(a.pre) < len(b.pre) {
		return -1
	}
	if len(a.pre) > len(b.pre) {
		return 1
	}
	return 0
}
func allowed(channel, stage string) bool {
	return stage == "stable" || channel == "rc" && stage == "rc" || channel == "beta" && (stage == "beta" || stage == "rc")
}
