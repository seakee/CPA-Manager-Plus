package updatecheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

const Repository = "https://github.com/seakee/CPA-Manager-Plus"
const IndexURL = "https://raw.githubusercontent.com/seakee/CPA-Manager-Plus/update-channel/update-index.json"

type Target struct {
	Version string `json:"version"`
}
type Index struct {
	SchemaVersion int                `json:"schema_version"`
	Revision      int64              `json:"revision"`
	GeneratedAt   time.Time          `json:"generated_at"`
	Channels      map[string]*Target `json:"channels"`
}
type ReleaseInfo struct {
	SchemaVersion int `json:"schema_version"`
	Release       struct {
		Version      string `json:"version"`
		Stage        string `json:"stage"`
		SourceCommit string `json:"source_commit"`
	} `json:"release"`
	Content struct {
		Summary map[string]string `json:"summary"`
		Notes   map[string]string `json:"notes"`
	} `json:"content"`
	Update struct {
		Breaking                    bool    `json:"breaking"`
		MigrationRequired           bool    `json:"migration_required"`
		MinimumDirectUpgradeVersion *string `json:"minimum_direct_upgrade_version"`
		UpgradeGuideURL             string  `json:"upgrade_guide_url"`
	} `json:"update"`
	Distribution struct {
		Docker struct {
			Image      string `json:"image"`
			VersionTag string `json:"version_tag"`
		} `json:"docker"`
		Native struct {
			Assets []string `json:"assets"`
		} `json:"native"`
	} `json:"distribution"`
	Compatibility struct {
		MinimumCPAVersion *string `json:"minimum_cpa_version"`
	} `json:"compatibility"`
}

// Require explicit compatibility and migration decisions; JSON null must not
// silently become false or an omitted lower-bound policy.
func (info *ReleaseInfo) UnmarshalJSON(data []byte) error {
	type plain ReleaseInfo
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var raw map[string]map[string]json.RawMessage
	// schema_version is not an object, so inspect only the policy sections.
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	raw = map[string]map[string]json.RawMessage{}
	for _, section := range []string{"update", "compatibility"} {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(root[section], &fields); err != nil {
			return err
		}
		raw[section] = fields
	}
	for _, key := range []string{"breaking", "migration_required"} {
		value := bytes.TrimSpace(raw["update"][key])
		if !bytes.Equal(value, []byte("true")) && !bytes.Equal(value, []byte("false")) {
			return errors.New("missing explicit update policy")
		}
	}
	if _, ok := raw["update"]["minimum_direct_upgrade_version"]; !ok {
		return errors.New("missing upgrade boundary")
	}
	if _, ok := raw["compatibility"]["minimum_cpa_version"]; !ok {
		return errors.New("missing compatibility boundary")
	}
	*info = ReleaseInfo(decoded)
	return nil
}
func (idx Index) Validate() error {
	if idx.SchemaVersion != 1 || idx.Revision < 1 || idx.GeneratedAt.IsZero() {
		return errors.New("invalid update index")
	}
	for _, ch := range []string{"stable", "rc", "beta"} {
		t, ok := idx.Channels[ch]
		if !ok {
			return errors.New("missing channel")
		}
		if t == nil {
			continue
		}
		v, err := ParseVersion(t.Version)
		if err != nil || !strings.HasPrefix(t.Version, "v") || strings.Contains(t.Version, "+") || !allowed(ch, v.Stage) {
			return errors.New("invalid channel target")
		}
	}
	return nil
}
func safeLink(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil && !strings.Contains(u.Path, `\`) && strings.HasPrefix(path.Clean(u.Path), "/seakee/CPA-Manager-Plus/")
}
func (info ReleaseInfo) Validate(tag string) error {
	v, err := ParseVersion(tag)
	if err != nil || v.Stage == "" || !strings.HasPrefix(tag, "v") || strings.Contains(tag, "+") || info.SchemaVersion != 1 || info.Release.Version != tag || info.Release.Stage != v.Stage || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(info.Release.SourceCommit) {
		return errors.New("invalid release identity")
	}
	for _, lang := range []string{"zh", "en"} {
		if strings.TrimSpace(info.Content.Summary[lang]) == "" || len(info.Content.Summary[lang]) > 4096 || info.Content.Notes[lang] != Repository+"/blob/"+tag+"/docs/release-notes/"+tag+"-"+lang+".md" {
			return errors.New("invalid release content")
		}
	}
	if info.Update.MinimumDirectUpgradeVersion != nil {
		if _, e := ParseVersion(*info.Update.MinimumDirectUpgradeVersion); e != nil {
			return e
		}
	}
	if !safeLink(info.Update.UpgradeGuideURL) {
		return errors.New("invalid upgrade guide")
	}
	if info.Distribution.Docker.Image != "seakee/cpa-manager-plus" || info.Distribution.Docker.VersionTag != tag {
		return errors.New("invalid docker distribution")
	}
	if len(info.Distribution.Native.Assets) != 6 {
		return errors.New("invalid native distribution")
	}
	seen := map[string]bool{}
	for _, asset := range info.Distribution.Native.Assets {
		valid := false
		for _, os := range []string{"darwin", "linux", "windows"} {
			for _, arch := range []string{"amd64", "arm64"} {
				ext := ".tar.gz"
				if os == "windows" {
					ext = ".zip"
				}
				if asset == "cpa-manager-plus_"+tag+"_"+os+"_"+arch+ext {
					valid = true
				}
			}
		}
		if !valid || seen[asset] {
			return errors.New("invalid native asset")
		}
		seen[asset] = true
	}
	if info.Compatibility.MinimumCPAVersion != nil {
		if _, e := ParseVersion(*info.Compatibility.MinimumCPAVersion); e != nil {
			return e
		}
	}
	return nil
}
