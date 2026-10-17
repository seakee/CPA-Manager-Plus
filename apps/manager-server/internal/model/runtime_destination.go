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
}

func (r RuntimeDestinationRequest) Validate() error {
	if err := validateRuntimeOperationIdentity(r.OperationID, r.ExpectedRuntimeIdentity, r.ExpectedRuntimeGeneration); err != nil {
		return err
	}
	if !r.ExpectedActiveArtifactID.IsValid() || r.SourceIdentity == "" || (r.SourceClass != "network_existing" && r.SourceClass != "legacy_managed") || r.APIKeys == nil || len(r.APIKeys) > 128 || len(r.ManagementKey) < 32 || len(r.ManagementKey) > 128 {
		return errors.New("invalid adoption destination")
	}
	for _, key := range r.APIKeys {
		if key == "" || len(key) > 4096 {
			return errors.New("invalid adoption client key")
		}
	}
	return nil
}
