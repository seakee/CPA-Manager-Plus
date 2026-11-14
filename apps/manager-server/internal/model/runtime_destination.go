package model

import "errors"

// RuntimeDestinationRequest is a bounded CPA-native destination, not a caller
// path or a generic configuration writer. Secrets never enter Runtime journals.
type RuntimeDestinationRequest struct {
	OperationID               string            `json:"operationId"`
	ExpectedRuntimeIdentity   RuntimeIdentity   `json:"expectedRuntimeIdentity"`
	ExpectedRuntimeGeneration RuntimeGeneration `json:"expectedRuntimeGeneration"`
	ExpectedActiveArtifactID  RuntimeArtifactID `json:"expectedActiveArtifactId"`
	SourceIdentity            string            `json:"sourceIdentity"`
	SourceClass               string            `json:"sourceClass"`
	APIKeys                   []string          `json:"apiKeys"`
	ManagementKey             string            `json:"managementKey"`
	// PluginsEnabled is the install-time plugin opt-in. The plugin directory is
	// never carried here: the Supervisor owns it.
	PluginsEnabled bool `json:"pluginsEnabled,omitempty"`
}

func (r RuntimeDestinationRequest) Validate() error {
	if err := validateRuntimeOperationIdentity(r.OperationID, r.ExpectedRuntimeIdentity, r.ExpectedRuntimeGeneration); err != nil {
		return err
	}
	managed := r.SourceClass == "new_managed"
	if !managed && r.SourceClass != "network_existing" && r.SourceClass != "legacy_managed" {
		return errors.New("invalid destination source class")
	}
	if !r.ExpectedActiveArtifactID.IsValid() || r.SourceIdentity == "" || r.APIKeys == nil || len(r.ManagementKey) < 32 || len(r.ManagementKey) > 128 {
		return errors.New("invalid destination")
	}
	if managed && (len(r.APIKeys) != 1 || !ValidInitialClientKey(r.APIKeys[0]) || len(r.SourceIdentity) < len("managed:sha256:")) || !managed && len(r.APIKeys) == 0 {
		return errors.New("invalid destination keys")
	}
	for _, key := range r.APIKeys {
		if key == "" || len(key) > 4096 {
			return errors.New("invalid destination client key")
		}
	}
	return nil
}
