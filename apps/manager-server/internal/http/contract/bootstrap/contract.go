// Package bootstrapcontract contains versioned wire DTOs for the future
// Manager Bootstrap transport. It deliberately has no handlers or writable
// operations in Phase4-01.
package bootstrapcontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	domain "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

const (
	StatusContractVersion = "cpamp.phase4-01.status.v1"
	PlanContractVersion   = "cpamp.phase4-01.plan.v1"
)

// DiscoveryDTO contains the complete unauthenticated response vocabulary. The
// struct has no source, path, key, archive, operation, mutation or resource
// fields by design.
type DiscoveryDTO struct {
	ContractVersion        string              `json:"contract_version"`
	Status                 domain.PublicStatus `json:"status"`
	Mode                   *domain.Mode        `json:"mode,omitempty"`
	AuthenticationRequired bool                `json:"authentication_required"`
	AuthEntry              domain.AuthEntry    `json:"auth_entry"`
	Reason                 domain.PublicReason `json:"reason"`
}

func DiscoveryFromDomain(value domain.PublicDiscovery) (DiscoveryDTO, error) {
	if err := value.Validate(); err != nil {
		return DiscoveryDTO{}, err
	}
	return DiscoveryDTO{
		ContractVersion:        value.ContractVersion,
		Status:                 value.Status,
		Mode:                   value.Mode,
		AuthenticationRequired: value.AuthenticationRequired,
		AuthEntry:              value.AuthEntry,
		Reason:                 value.Reason,
	}, nil
}

func (d DiscoveryDTO) Validate() error {
	return (domain.PublicDiscovery{
		ContractVersion:        d.ContractVersion,
		Status:                 d.Status,
		Mode:                   d.Mode,
		AuthenticationRequired: d.AuthenticationRequired,
		AuthEntry:              d.AuthEntry,
		Reason:                 d.Reason,
	}).Validate()
}

func (d DiscoveryDTO) MarshalJSON() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	type wire DiscoveryDTO
	return json.Marshal(wire(d))
}

// UnmarshalJSON is strict so a future server cannot accidentally accept a
// sensitive or misspelled discovery field and silently ignore it.
func (d *DiscoveryDTO) UnmarshalJSON(data []byte) error {
	type wire DiscoveryDTO
	var value wire
	if err := decodeStrict(data, &value); err != nil {
		return err
	}
	decoded := DiscoveryDTO(value)
	if err := decoded.Validate(); err != nil {
		return err
	}
	*d = decoded
	return nil
}

type BootstrapStatusDTO struct {
	FreshOwnership  *domain.FreshOwnership      `json:"fresh_ownership,omitempty"`
	ContractVersion string                      `json:"contract_version"`
	Mode            domain.Mode                 `json:"mode"`
	AdminCredential domain.AdminCredentialState `json:"admin_credential_state"`
	Session         domain.SessionState         `json:"session_state"`
}

func StatusFromDomain(value domain.StatusSnapshot) (BootstrapStatusDTO, error) {
	if err := value.Validate(); err != nil {
		return BootstrapStatusDTO{}, err
	}
	return BootstrapStatusDTO{
		FreshOwnership:  value.FreshOwnership,
		ContractVersion: StatusContractVersion,
		Mode:            value.Mode,
		AdminCredential: value.AdminCredential,
		Session:         value.Session,
	}, nil
}

func (d BootstrapStatusDTO) Validate() error {
	if d.ContractVersion != StatusContractVersion {
		return fmt.Errorf("unsupported status contract version %q", d.ContractVersion)
	}
	return (domain.StatusSnapshot{
		FreshOwnership:  d.FreshOwnership,
		Mode:            d.Mode,
		AdminCredential: d.AdminCredential,
		Session:         d.Session,
	}).Validate()
}

func (d BootstrapStatusDTO) MarshalJSON() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	type wire BootstrapStatusDTO
	return json.Marshal(wire(d))
}

func (d *BootstrapStatusDTO) UnmarshalJSON(data []byte) error {
	type wire BootstrapStatusDTO
	var value wire
	if err := decodeStrict(data, &value); err != nil {
		return err
	}
	decoded := BootstrapStatusDTO(value)
	if err := decoded.Validate(); err != nil {
		return err
	}
	*d = decoded
	return nil
}

type PlanQualification string

const (
	QualificationUnknown   PlanQualification = "unknown"
	QualificationPending   PlanQualification = "pending"
	QualificationQualified PlanQualification = "qualified"
)

// BootstrapPlanDTO is a future authenticated, read-only projection. It has no
// operation ID or writable action; unknown qualification remains explicit.
type BootstrapPlanDTO struct {
	ContractVersion        string             `json:"contract_version"`
	Mode                   domain.Mode        `json:"mode"`
	CPAMPAction            domain.CPAMPAction `json:"cpamp_action"`
	CPAStrategy            domain.CPAStrategy `json:"cpa_strategy"`
	UsageAction            domain.UsageAction `json:"usage_action"`
	BackupRequired         bool               `json:"backup_required"`
	AuthenticationRequired bool               `json:"authentication_required"`
	Mutations              []string           `json:"mutations"`
	Preserved              []string           `json:"preserved"`
	Unsupported            []string           `json:"unsupported"`
	EstimatedSteps         []string           `json:"estimated_steps"`
	Qualification          PlanQualification  `json:"qualification"`
}

// ValidateAuthenticatedPlanAccess is the transport boundary for this
// read-only projection. Phase4-01 freezes the requirement without adding a
// handler, session carrier, or writable operation.
func ValidateAuthenticatedPlanAccess(authenticated bool, plan BootstrapPlanDTO) error {
	if !authenticated {
		return errors.New("BootstrapPlan requires authenticated Manager access")
	}
	return plan.Validate()
}

func (d BootstrapPlanDTO) Validate() error {
	if d.ContractVersion != PlanContractVersion {
		return fmt.Errorf("unsupported plan contract version %q", d.ContractVersion)
	}
	if !validPlanMode(d.Mode) {
		return fmt.Errorf("unknown plan mode %q", d.Mode)
	}
	if !validCPAMPAction(d.CPAMPAction) {
		return fmt.Errorf("unknown CPAMP action %q", d.CPAMPAction)
	}
	if !validCPAStrategy(d.CPAStrategy) {
		return fmt.Errorf("unknown CPA strategy %q", d.CPAStrategy)
	}
	if !validUsageAction(d.UsageAction) {
		return fmt.Errorf("unknown usage action %q", d.UsageAction)
	}
	switch d.Qualification {
	case QualificationUnknown, QualificationPending, QualificationQualified:
	default:
		return fmt.Errorf("unknown plan qualification %q", d.Qualification)
	}
	if d.Mode == domain.Completed {
		return errors.New("completed plan is not a fixed discovery payload")
	}
	if !d.AuthenticationRequired {
		return errors.New("BootstrapPlan requires Manager authentication")
	}
	if d.Mutations == nil || d.Preserved == nil || d.Unsupported == nil || d.EstimatedSteps == nil {
		return errors.New("plan effect lists must be explicit")
	}
	if err := validateDisjointLists(d.Mutations, d.Preserved, d.Unsupported); err != nil {
		return err
	}
	return validatePlanTextList("estimated_steps", d.EstimatedSteps)
}

func (d BootstrapPlanDTO) MarshalJSON() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	type wire BootstrapPlanDTO
	return json.Marshal(wire(d))
}

func validPlanMode(value domain.Mode) bool {
	switch value {
	case domain.Fresh, domain.Upgrade, domain.UnsupportedUpgrade, domain.Recovery, domain.Completed:
		return true
	default:
		return false
	}
}

func validCPAMPAction(value domain.CPAMPAction) bool {
	switch value {
	case domain.Initialize, domain.MigrateV1:
		return true
	default:
		return false
	}
}

func validCPAStrategy(value domain.CPAStrategy) bool {
	switch value {
	case domain.NewManaged, domain.AdoptExisting, domain.ConnectExternal, domain.MigrateLegacy:
		return true
	default:
		return false
	}
}

func validUsageAction(value domain.UsageAction) bool {
	switch value {
	case domain.NoUsageAction, domain.PreserveUsage:
		return true
	default:
		return false
	}
}

func (d *BootstrapPlanDTO) UnmarshalJSON(data []byte) error {
	type wire BootstrapPlanDTO
	var value wire
	if err := decodeStrict(data, &value); err != nil {
		return err
	}
	decoded := BootstrapPlanDTO(value)
	if err := decoded.Validate(); err != nil {
		return err
	}
	*d = decoded
	return nil
}

func validateDisjointLists(groups ...[]string) error {
	if len(groups) != 3 {
		return errors.New("plan effect groups are incomplete")
	}
	seen := map[string]string{}
	for _, group := range []struct {
		name   string
		values []string
	}{
		{name: "mutations", values: groups[0]},
		{name: "preserved", values: groups[1]},
		{name: "unsupported", values: groups[2]},
	} {
		local := map[string]bool{}
		for _, value := range group.values {
			if value == "" || strings.TrimSpace(value) != value {
				return fmt.Errorf("%s contains an empty or padded value", group.name)
			}
			if local[value] {
				return fmt.Errorf("%s contains duplicate value %q", group.name, value)
			}
			local[value] = true
			if previous, ok := seen[value]; ok {
				return fmt.Errorf("plan effect %q appears in %s and %s", value, previous, group.name)
			}
			seen[value] = group.name
		}
	}
	return nil
}

func validatePlanTextList(name string, values []string) error {
	if values == nil {
		return fmt.Errorf("%s must be explicit", name)
	}
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("%s contains an empty or padded value", name)
		}
		if seen[value] {
			return fmt.Errorf("%s contains duplicate value %q", name, value)
		}
		seen[value] = true
	}
	return nil
}

func decodeStrict(data []byte, target any) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return fmt.Errorf("trailing JSON: %w", err)
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errors.New("duplicate JSON object key")
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("unexpected JSON delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
