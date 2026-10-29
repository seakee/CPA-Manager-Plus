package bootstrap

import (
	"errors"
	"strings"
	"time"
)

const (
	FreshContractVersion     = 1
	FreshClaimLifetime       = 10 * time.Minute
	FreshSessionLifetime     = 30 * time.Minute
	FixedBootstrapUIPath     = FixedBootstrapDiscoveryPath + "/ui"
	FixedBootstrapAssetsPath = FixedBootstrapDiscoveryPath + "/assets/"
	FixedBootstrapAPIPath    = FixedBootstrapDiscoveryPath + "/api/v2/"
)

// FreshIdentity is non-secret provenance established only by fenced real-store
// admission. Request-supplied identity is never sufficient to create it.
type FreshIdentity struct {
	AdmissionID    string      `json:"admissionId"`
	InstallationID string      `json:"installationId"`
	Generation     string      `json:"generation"`
	DatabasePath   string      `json:"databasePath"`
	OperationID    OperationID `json:"operationId"`
}

func (i FreshIdentity) Valid() bool {
	return i.AdmissionID != "" && i.InstallationID != "" && i.Generation != "" &&
		i.DatabasePath != "" && validOperationID(i.OperationID)
}

type FreshOwnership struct {
	Identity              FreshIdentity `json:"identity"`
	CurrentInstallationID string        `json:"currentInstallationId"`
	CurrentGeneration     string        `json:"currentGeneration"`
	CurrentOperationID    OperationID   `json:"currentOperationId"`
	Revision              uint64        `json:"revision"`
}

func (o FreshOwnership) Valid() bool {
	return o.Identity.Valid() && o.Revision > 0 &&
		o.Identity.InstallationID == o.CurrentInstallationID &&
		o.Identity.Generation == o.CurrentGeneration && o.Identity.OperationID == o.CurrentOperationID
}

// FreshOperation is the product checkpoint, not an authentication record or a
// general workflow engine. It never contains browser/Admin/CPA secrets.
type FreshOperation struct {
	Version          int                     `json:"version"`
	Identity         FreshIdentity           `json:"identity"`
	Revision         uint64                  `json:"revision"`
	AdminInitialized bool                    `json:"adminInitialized"`
	ProductComplete  bool                    `json:"productComplete"`
	GatewayPending   bool                    `json:"gatewayPending"`
	Routing          FreshRoutingCheckpoint  `json:"routing"`
	Strategy         FreshStrategyCheckpoint `json:"strategy"`
}

type FreshRoutingCheckpoint struct {
	OperationID OperationID          `json:"operationId,omitempty"`
	Revision    uint64               `json:"revision"`
	Target      string               `json:"target,omitempty"`
	Stage       PrefixOperationStage `json:"stage,omitempty"`
	Committed   bool                 `json:"committed"`
}

// MaxAdoptionDestinationRetry bounds how far adoption may advance the shared
// destination attempt counter. Adoption starts one attempt, and each terminal
// Supervisor failure retires it, so an operator gets the original attempt plus
// exactly one retry. A third attempt is not authorized: the second failure has
// no new information, and the operator must reconcile the target instead.
const MaxAdoptionDestinationRetry = 1

type FreshStrategyCheckpoint struct {
	Strategy                      CPAStrategy      `json:"strategy,omitempty"`
	TargetIdentity                string           `json:"targetIdentity,omitempty"`
	TargetArtifactID              string           `json:"targetArtifactId,omitempty"`
	TargetGeneration              uint64           `json:"targetGeneration"`
	Stage                         string           `json:"stage,omitempty"`
	Unsupported                   []string         `json:"unsupported,omitempty"`
	SourceIdentity                string           `json:"sourceIdentity,omitempty"`
	SourceClass                   AdoptSourceClass `json:"sourceClass,omitempty"`
	DestinationConnectionIdentity string           `json:"destinationConnectionIdentity,omitempty"`
	// DestinationAttempt is the durable index of the current Supervisor
	// destination operation for a Manager-staged destination. The Supervisor
	// journal never resumes a terminal operation, so a terminally failed stage or
	// start must advance this counter and re-issue the same destination under a
	// new operation ID. Attempt zero is the original operation ID, which keeps
	// every checkpoint written before this counter existed exactly idempotent.
	// A New Managed install may retire attempts as often as its target fails, but
	// an adoption is additionally bounded by MaxAdoptionDestinationRetry, and a
	// committed adoption can never advance past the attempt that succeeded.
	DestinationAttempt uint64 `json:"destinationAttempt,omitempty"`
	// PluginsEnabled is the install-time plugin opt-in the wizard persisted. It
	// is a product decision of this Fresh operation, never browser state: the
	// staged destination template reads it once and the Supervisor writes it.
	PluginsEnabled    bool                       `json:"pluginsEnabled,omitempty"`
	AdoptionDecisions []AdoptionResourceDecision `json:"adoptionDecisions,omitempty"`
	AdoptionResources []ResourceRef              `json:"adoptionResources,omitempty"`
}

func (c FreshStrategyCheckpoint) validate() error {
	if c.Strategy == "" {
		if c.TargetIdentity != "" || c.TargetArtifactID != "" || c.TargetGeneration != 0 || c.Stage != "" || len(c.Unsupported) != 0 || c.SourceIdentity != "" || c.SourceClass != "" || c.DestinationConnectionIdentity != "" || c.DestinationAttempt != 0 || c.PluginsEnabled || len(c.AdoptionDecisions) != 0 || len(c.AdoptionResources) != 0 {
			return errors.New("unowned Fresh strategy checkpoint")
		}
		return nil
	}
	if c.Strategy == NewManaged {
		if c.SourceIdentity != "" || c.SourceClass != "" || len(c.AdoptionDecisions) != 0 || len(c.AdoptionResources) != 0 {
			return errors.New("foreign managed checkpoint source")
		}
		if c.DestinationConnectionIdentity != "" && (!strings.HasPrefix(c.DestinationConnectionIdentity, "managed:") || !validContentDigest(ContentDigest(strings.TrimPrefix(c.DestinationConnectionIdentity, "managed:")))) {
			return errors.New("invalid managed destination identity")
		}
	} else if c.Strategy != AdoptExisting && (c.SourceIdentity != "" || c.SourceClass != "" || c.DestinationConnectionIdentity != "" || c.DestinationAttempt != 0 || c.PluginsEnabled || len(c.AdoptionDecisions) != 0 || len(c.AdoptionResources) != 0) {
		return errors.New("foreign adoption checkpoint")
	}
	// Plugins are a Supervisor-owned resource of the Manager's own destination, so
	// the opt-in belongs to the strategies that stage one: New Managed and Adopt.
	// An external CPA is the user's own process and keeps its own plugin posture.
	if c.PluginsEnabled && c.Strategy != NewManaged && c.Strategy != AdoptExisting {
		return errors.New("foreign plugin opt-in")
	}
	if strings.TrimSpace(c.TargetIdentity) == "" || c.TargetGeneration == 0 || len(c.Unsupported) != 0 {
		return errors.New("invalid Fresh strategy target")
	}
	switch c.Strategy {
	case NewManaged:
		if !validContentDigest(ContentDigest(c.TargetArtifactID)) || (c.Stage != "staged" && c.Stage != "starting" && c.Stage != "committed") {
			return errors.New("invalid managed Fresh strategy checkpoint")
		}
	case ConnectExternal:
		if !strings.HasPrefix(c.TargetIdentity, "external:") || !validContentDigest(ContentDigest(strings.TrimPrefix(c.TargetIdentity, "external:"))) || c.TargetGeneration != 1 || c.TargetArtifactID != "" || (c.Stage != "validated" && c.Stage != "committed") {
			return errors.New("invalid external Fresh strategy checkpoint")
		}
	case AdoptExisting:
		if c.DestinationAttempt > MaxAdoptionDestinationRetry || !validContentDigest(ContentDigest(c.TargetArtifactID)) || (c.Stage != "staged" && c.Stage != "committed") ||
			(c.SourceClass != AdoptNetworkExisting && c.SourceClass != AdoptLegacyManaged) || !validExternalIdentity(c.SourceIdentity) || !validExternalIdentity(c.DestinationConnectionIdentity) || c.SourceIdentity == c.DestinationConnectionIdentity ||
			!validFreshAdoptionInventory(c.AdoptionResources, c.AdoptionDecisions) {
			return errors.New("invalid adoption checkpoint")
		}
	default:
		return errors.New("unsupported Fresh strategy checkpoint")
	}
	return nil
}

func validExternalIdentity(s string) bool {
	return strings.HasPrefix(s, "external:") && validContentDigest(ContentDigest(strings.TrimPrefix(s, "external:")))
}

func (o FreshOperation) Ownership() FreshOwnership {
	return FreshOwnership{Identity: o.Identity, CurrentInstallationID: o.Identity.InstallationID,
		CurrentGeneration: o.Identity.Generation, CurrentOperationID: o.Identity.OperationID, Revision: o.Revision}
}

func (o FreshOperation) Validate() error {
	if o.Version != FreshContractVersion || !o.Ownership().Valid() || !o.GatewayPending {
		return errors.New("invalid Fresh ownership checkpoint")
	}
	if err := o.Strategy.validate(); err != nil {
		return err
	}
	if o.Strategy.Strategy != "" && !o.AdminInitialized {
		return errors.New("Fresh strategy requires initialized Admin")
	}
	if o.Routing.OperationID == "" {
		if o.Routing.Revision != 0 || o.Routing.Target != "" || o.Routing.Stage != "" || o.Routing.Committed {
			return errors.New("unowned initial routing checkpoint")
		}
	} else {
		if o.Routing.OperationID != o.Identity.OperationID {
			return errors.New("foreign initial routing owner")
		}
		if o.Routing.Revision == 0 || !ValidControlBasePath(o.Routing.Target) {
			return errors.New("invalid initial routing target")
		}
		switch o.Routing.Stage {
		case PrefixIntentPersisted, PrefixRouteActivated, PrefixHealthVerified:
			if o.Routing.Committed {
				return errors.New("inconsistent initial routing commit")
			}
		case PrefixCommitted:
			if !o.Routing.Committed {
				return errors.New("inconsistent initial routing commit")
			}
		default:
			return errors.New("invalid initial routing stage")
		}
	}
	if o.Routing.Committed && (!o.AdminInitialized || o.Routing.Stage != PrefixCommitted || o.Routing.Target == "" || o.Routing.Revision == 0) {
		return errors.New("unverified Fresh route commit")
	}
	if o.ProductComplete && (!o.AdminInitialized || !o.Routing.Committed || o.Strategy.Stage != "committed") {
		return errors.New("Fresh product completion is not established")
	}
	return nil
}
