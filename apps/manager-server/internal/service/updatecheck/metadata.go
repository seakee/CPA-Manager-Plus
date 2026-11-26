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
const DocsBaseURL = "https://cpamp-docs.seakee.dev"
const IndexURL = "https://raw.githubusercontent.com/seakee/CPA-Manager-Plus/update-channel/update-index-v2.json"

var (
	sha256Pattern       = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	sourceCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	nativeAssetPattern  = regexp.MustCompile(`^cpa-manager-plus-native-v2_(.+)_(darwin|linux|windows)_(amd64|arm64)\.(?:tar\.gz|zip)$`)
	slotAssetPattern    = regexp.MustCompile(`^cpamp-slot_(.+)_linux_(amd64|arm64)\.tar\.gz$`)
)

type Target struct {
	Version           string `json:"version"`
	SchemaVersion     int    `json:"schema_version"`
	ReleaseInfoPath   string `json:"release_info_path"`
	ReleaseInfoSHA256 string `json:"release_info_sha256,omitempty"`
}
type Index struct {
	SchemaVersion int                `json:"schema_version"`
	Revision      int64              `json:"revision"`
	GeneratedAt   time.Time          `json:"generated_at"`
	Channels      map[string]*Target `json:"channels"`
}
type DockerImage struct {
	Role      string   `json:"role"`
	Name      string   `json:"name"`
	Tag       string   `json:"tag"`
	Digest    string   `json:"digest"`
	Platforms []string `json:"platforms"`
}
type ComposeDistribution struct {
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
}
type SlotAsset struct {
	Name             string `json:"name"`
	Platform         string `json:"platform"`
	SHA256           string `json:"sha256"`
	Size             int64  `json:"size"`
	MinBaseEpoch     int    `json:"min_base_epoch"`
	SchemaGeneration int    `json:"schema_generation"`
}
type SlotDistribution struct {
	Assets []SlotAsset `json:"assets"`
}
type ReleasePairing struct {
	Filename              string `json:"filename"`
	SourceCommit          string `json:"source_commit"`
	FullWebManifestSHA256 string `json:"fullweb_manifest_sha256"`
	APIContractSHA256     string `json:"api_contract_sha256"`
	ComposeSHA256         string `json:"compose_sha256"`
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
			VersionTag string        `json:"version_tag"`
			Images     []DockerImage `json:"images"`
		} `json:"docker"`
		Native struct {
			Assets []string `json:"assets"`
		} `json:"native"`
		Compose *ComposeDistribution `json:"compose,omitempty"`
		Slot    *SlotDistribution    `json:"slot,omitempty"`
	} `json:"distribution"`
	Compatibility struct {
		MinimumCPAVersion *string `json:"minimum_cpa_version"`
	} `json:"compatibility"`
	SupportedModes     []string            `json:"supported_modes,omitempty"`
	SupportedPlatforms map[string][]string `json:"supported_platforms,omitempty"`
	Pairing            *ReleasePairing     `json:"pairing,omitempty"`
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
func isV2OrNewer(v Version) bool {
	return len(v.core) == 3 && v.core[0] != "0" && v.core[0] != "1"
}
func isSafeFilename(name string) bool {
	return name != "" &&
		strings.TrimSpace(name) == name &&
		name != "." &&
		name != ".." &&
		!strings.ContainsAny(name, `/\`)
}
func hasDotPathSegment(rawPath string) bool {
	for _, seg := range strings.Split(rawPath, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}
func (idx Index) Validate() error {
	if idx.SchemaVersion != 2 || idx.Revision < 1 || idx.GeneratedAt.IsZero() {
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
		if err != nil || !strings.HasPrefix(t.Version, "v") || strings.Contains(t.Version, "+") || !isV2OrNewer(v) || !allowed(ch, v.Stage) {
			return errors.New("invalid channel target")
		}
		if t.SchemaVersion != 2 {
			return errors.New("invalid channel target schema")
		}
		if t.ReleaseInfoPath != "releases/download/"+t.Version+"/release-info.json" {
			return errors.New("invalid channel target path")
		}
		if t.ReleaseInfoSHA256 == "" || !sha256Pattern.MatchString(t.ReleaseInfoSHA256) {
			return errors.New("invalid channel target digest")
		}
	}
	return nil
}
func safeUpgradeGuideURL(raw string) bool {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return false
	}
	if strings.ContainsAny(raw, `\#?`+"\r\n\t ") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return false
	}
	lowerRawPath := strings.ToLower(u.RawPath)
	if strings.Contains(lowerRawPath, "%2e") || strings.Contains(lowerRawPath, "%2f") || hasDotPathSegment(u.Path) {
		return false
	}
	cleanPath := path.Clean(u.Path)
	switch u.Host {
	case "github.com":
		return strings.HasPrefix(cleanPath, "/seakee/CPA-Manager-Plus/") && len(cleanPath) > len("/seakee/CPA-Manager-Plus/")
	case "cpamp-docs.seakee.dev":
		return strings.HasPrefix(cleanPath, "/") && cleanPath != "/"
	default:
		return false
	}
}
func (info ReleaseInfo) Validate(tag string) error {
	v, err := ParseVersion(tag)
	if err != nil || v.Stage == "" || !strings.HasPrefix(tag, "v") || strings.Contains(tag, "+") || !isV2OrNewer(v) ||
		info.SchemaVersion != 2 || info.Release.Version != tag || info.Release.Stage != v.Stage ||
		(info.Release.Stage != "beta" && info.Release.Stage != "rc" && info.Release.Stage != "stable") ||
		!sourceCommitPattern.MatchString(info.Release.SourceCommit) {
		return errors.New("invalid release identity")
	}
	for _, lang := range []string{"zh", "en"} {
		if strings.TrimSpace(info.Content.Summary[lang]) == "" || len(info.Content.Summary[lang]) > 4096 ||
			info.Content.Notes[lang] != Repository+"/blob/"+tag+"/docs/release-notes/"+tag+"-"+lang+".md" {
			return errors.New("invalid release content")
		}
	}
	if info.Update.MinimumDirectUpgradeVersion != nil {
		if _, e := ParseVersion(*info.Update.MinimumDirectUpgradeVersion); e != nil {
			return e
		}
	}
	if !safeUpgradeGuideURL(info.Update.UpgradeGuideURL) {
		return errors.New("invalid upgrade guide")
	}
	expectedDockerTag := strings.TrimPrefix(tag, "v")
	if info.Distribution.Docker.VersionTag != expectedDockerTag {
		return errors.New("invalid docker distribution")
	}
	if len(info.Distribution.Docker.Images) != 4 {
		return errors.New("invalid docker image set")
	}
	seenImages := map[string]bool{}
	roleDigests := map[string]string{}
	for _, img := range info.Distribution.Docker.Images {
		var expectedTag string
		switch img.Role {
		case "slim":
			expectedTag = expectedDockerTag
		case "full":
			expectedTag = expectedDockerTag + "-full"
		default:
			return errors.New("invalid docker image role")
		}
		if img.Name != "seakee/cpa-manager-plus" && img.Name != "ghcr.io/seakee/cpa-manager-plus" {
			return errors.New("invalid docker image repository")
		}
		if img.Tag != expectedTag {
			return errors.New("invalid docker image tag")
		}
		if !sha256Pattern.MatchString(img.Digest) {
			return errors.New("invalid docker image digest")
		}
		if len(img.Platforms) == 0 {
			return errors.New("missing docker image platforms")
		}
		seenPlatforms := map[string]bool{}
		for _, p := range img.Platforms {
			parts := strings.Split(p, "/")
			if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" || seenPlatforms[p] {
				return errors.New("invalid docker image platform")
			}
			seenPlatforms[p] = true
		}
		key := img.Role + "\x00" + img.Name
		if seenImages[key] {
			return errors.New("duplicate docker image entry")
		}
		seenImages[key] = true
		if prev, ok := roleDigests[img.Role]; ok && prev != img.Digest {
			return errors.New("docker registry digest mismatch")
		}
		roleDigests[img.Role] = img.Digest
	}
	if len(roleDigests) != 2 || roleDigests["slim"] == "" || roleDigests["full"] == "" || roleDigests["slim"] == roleDigests["full"] {
		return errors.New("invalid docker variant digests")
	}
	if len(info.Distribution.Native.Assets) < 1 {
		return errors.New("invalid native distribution")
	}
	seenAssets := map[string]bool{}
	for _, asset := range info.Distribution.Native.Assets {
		m := nativeAssetPattern.FindStringSubmatch(asset)
		if m == nil || m[1] != tag || seenAssets[asset] {
			return errors.New("invalid native asset")
		}
		seenAssets[asset] = true
	}
	if info.Distribution.Compose != nil {
		if !isSafeFilename(info.Distribution.Compose.Filename) || !sha256Pattern.MatchString(info.Distribution.Compose.SHA256) {
			return errors.New("invalid compose distribution")
		}
	}
	if beta2, parseErr := ParseVersion("v2.0.0-beta.2"); parseErr == nil && Compare(v, beta2) >= 0 && info.Distribution.Slot == nil {
		return errors.New("missing slot distribution")
	}
	if info.Distribution.Slot != nil {
		if len(info.Distribution.Slot.Assets) < 1 {
			return errors.New("invalid slot distribution")
		}
		seenSlotNames := map[string]bool{}
		seenSlotPlatforms := map[string]bool{}
		for _, asset := range info.Distribution.Slot.Assets {
			if !isSafeFilename(asset.Name) {
				return errors.New("invalid slot asset")
			}
			m := slotAssetPattern.FindStringSubmatch(asset.Name)
			if m == nil || m[1] != tag {
				return errors.New("invalid slot asset")
			}
			expectedPlatform := "linux/" + m[2]
			if asset.Platform != expectedPlatform {
				return errors.New("invalid slot asset platform")
			}
			if !sha256Pattern.MatchString(asset.SHA256) || asset.Size <= 0 || asset.Size > MaxSlotArchiveBytes || asset.MinBaseEpoch < 1 || asset.SchemaGeneration < 1 {
				return errors.New("invalid slot asset metadata")
			}
			if seenSlotNames[asset.Name] || seenSlotPlatforms[asset.Platform] {
				return errors.New("duplicate slot asset")
			}
			seenSlotNames[asset.Name] = true
			seenSlotPlatforms[asset.Platform] = true
		}
	}
	if info.Compatibility.MinimumCPAVersion != nil {
		if _, e := ParseVersion(*info.Compatibility.MinimumCPAVersion); e != nil {
			return e
		}
	}
	if info.SupportedModes != nil {
		if len(info.SupportedModes) == 0 {
			return errors.New("empty supported modes")
		}
		seenModes := map[string]bool{}
		for _, mode := range info.SupportedModes {
			if strings.TrimSpace(mode) == "" || seenModes[mode] {
				return errors.New("invalid supported mode")
			}
			seenModes[mode] = true
		}
	}
	if info.SupportedPlatforms != nil {
		for kind, plats := range info.SupportedPlatforms {
			if strings.TrimSpace(kind) == "" || len(plats) == 0 {
				return errors.New("invalid supported platforms")
			}
			seenPlats := map[string]bool{}
			for _, p := range plats {
				parts := strings.Split(p, "/")
				if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" || seenPlats[p] {
					return errors.New("invalid supported platform")
				}
				seenPlats[p] = true
			}
		}
	}
	if info.Pairing != nil {
		if !isSafeFilename(info.Pairing.Filename) ||
			info.Pairing.SourceCommit != info.Release.SourceCommit ||
			!sha256Pattern.MatchString(info.Pairing.FullWebManifestSHA256) ||
			!sha256Pattern.MatchString(info.Pairing.APIContractSHA256) ||
			!sha256Pattern.MatchString(info.Pairing.ComposeSHA256) {
			return errors.New("invalid release pairing")
		}
		if info.Distribution.Compose != nil && info.Pairing.ComposeSHA256 != info.Distribution.Compose.SHA256 {
			return errors.New("release pairing compose digest mismatch")
		}
	}
	return nil
}
