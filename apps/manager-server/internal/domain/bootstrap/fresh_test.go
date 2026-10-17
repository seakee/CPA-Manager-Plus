package bootstrap

import (
	"strings"
	"testing"
)

func ownedFresh() FreshOperation {
	return FreshOperation{Version: FreshContractVersion, Identity: FreshIdentity{
		AdmissionID: "admission-a", InstallationID: "installation-a", Generation: "generation-a",
		DatabasePath: "/data/manager.sqlite", OperationID: "fresh-operation-a",
	}, Revision: 1, GatewayPending: true}
}

func TestFreshContinuationRequiresCurrentOwnership(t *testing.T) {
	op := ownedFresh()
	ownership := op.Ownership()
	for _, admin := range []AdminCredentialState{AdminCredentialUninitialized, AdminCredentialInitialized} {
		evidence := ClassificationEvidence{AdminCredential: admin, Session: SessionMissing, FreshOwnership: &ownership}
		if got, err := Classify(evidence); err != nil || got != Fresh {
			t.Fatalf("owned %s: %s %v", admin, got, err)
		}
		if err := (StatusSnapshot{Mode: Fresh, AdminCredential: admin, Session: SessionExpired, FreshOwnership: &ownership}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, mutate := range []func(*FreshOwnership){
		func(o *FreshOwnership) { o.CurrentGeneration = "other-generation" },
		func(o *FreshOwnership) { o.CurrentInstallationID = "other-installation" },
		func(o *FreshOwnership) { o.CurrentOperationID = "other-operation" },
		func(o *FreshOwnership) { o.Revision = 0 },
	} {
		bad := ownership
		mutate(&bad)
		got, err := Classify(ClassificationEvidence{AdminCredential: AdminCredentialInitialized, Session: SessionAuthenticated, FreshOwnership: &bad})
		if err != nil || got != Recovery {
			t.Fatalf("mismatched ownership: %s %v", got, err)
		}
		if (StatusSnapshot{Mode: Fresh, AdminCredential: AdminCredentialInitialized, Session: SessionAuthenticated, FreshOwnership: &bad}).Validate() == nil {
			t.Fatal("invalid status accepted")
		}
	}
	if got, err := Classify(ClassificationEvidence{AdminCredential: AdminCredentialInitialized, Session: SessionAuthenticated}); err != nil || got != Recovery {
		t.Fatalf("admin alone created Fresh: %s %v", got, err)
	}
}

func TestFreshCheckpointRejectsForeignRouteOwnerAndFalseCompletion(t *testing.T) {
	op := ownedFresh()
	if err := op.Validate(); err != nil {
		t.Fatal(err)
	}
	op.Routing.OperationID = "foreign-prefix-operation"
	if op.Validate() == nil {
		t.Fatal("foreign routing owner accepted")
	}
	op.Routing.OperationID = op.Identity.OperationID
	op.ProductComplete = true
	if op.Validate() == nil {
		t.Fatal("product completed without routing/strategy")
	}
	if CanActivatePublicGateway(GatewayActivationPreconditions{ProductInitialized: true}) {
		t.Fatal("product completion activated Gateway")
	}
}

func TestFreshRouteCheckpointRequiresOwnedCompleteShape(t *testing.T) {
	op := ownedFresh()
	valid := FreshRoutingCheckpoint{OperationID: op.Identity.OperationID, Revision: 1, Target: "/control", Stage: PrefixIntentPersisted}
	op.Routing = valid
	if err := op.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*FreshRoutingCheckpoint){
		func(c *FreshRoutingCheckpoint) { c.OperationID = "" },
		func(c *FreshRoutingCheckpoint) { c.Revision = 0 },
		func(c *FreshRoutingCheckpoint) { c.Target = "" },
		func(c *FreshRoutingCheckpoint) { c.Stage = "unknown" },
		func(c *FreshRoutingCheckpoint) { c.Stage = PrefixCommitted },
		func(c *FreshRoutingCheckpoint) { c.Committed = true },
	} {
		op.Routing = valid
		mutate(&op.Routing)
		if op.Validate() == nil {
			t.Fatal("malformed route checkpoint accepted")
		}
	}
}

func TestFreshStrategyCheckpointRequiresCompleteShape(t *testing.T) {
	managed := FreshStrategyCheckpoint{Strategy: NewManaged, Stage: "staged", TargetIdentity: "runtime-one", TargetGeneration: 3, TargetArtifactID: "sha256:" + strings.Repeat("a", 64)}
	external := FreshStrategyCheckpoint{Strategy: ConnectExternal, Stage: "committed", TargetIdentity: "external:sha256:" + strings.Repeat("b", 64), TargetGeneration: 1}
	adopt := FreshStrategyCheckpoint{
		Strategy: AdoptExisting, Stage: "staged", TargetIdentity: "runtime-adopt", TargetGeneration: 4,
		TargetArtifactID: "sha256:" + strings.Repeat("c", 64),
		SourceIdentity:   "external:sha256:" + strings.Repeat("d", 64), SourceClass: AdoptNetworkExisting,
		DestinationConnectionIdentity: "external:sha256:" + strings.Repeat("e", 64),
		AdoptionResources:             []ResourceRef{"client-api-keys", "cpa-auth-files", "cpa-config-server"},
		AdoptionDecisions: []AdoptionResourceDecision{
			{ResourceRef: "client-api-keys", Kind: APIKeys, Scope: ClientAPIKeys, ReadEvidenceRef: "preflight:keys-read", ReplayEvidenceRef: "preflight:keys-replay", VerifyEvidenceRef: "preflight:keys-verify", Qualification: CapabilityQualified, Disposition: Migrate},
			{ResourceRef: "cpa-auth-files", Kind: Credentials, Scope: CPAAuthFiles, Qualification: CapabilityPending, ManualAction: ReauthenticateAfterAdoption, Disposition: ManualAction},
			{ResourceRef: "cpa-config-server", Kind: CPAConfiguration, Scope: ManagementAPIFields, Qualification: CapabilityPending, ManualAction: ReconfigureAfterAdoption, Disposition: ManualAction},
		},
	}
	for _, checkpoint := range []FreshStrategyCheckpoint{{}, managed, external, adopt} {
		op := ownedFresh()
		op.AdminInitialized = true
		op.Strategy = checkpoint
		if err := op.Validate(); err != nil {
			t.Fatalf("valid checkpoint: %+v: %v", checkpoint, err)
		}
	}
	for name, checkpoint := range map[string]FreshStrategyCheckpoint{
		"stage alone":       {Stage: "committed"},
		"target alone":      {TargetIdentity: "runtime-one"},
		"generation alone":  {TargetGeneration: 1},
		"artifact alone":    {TargetArtifactID: managed.TargetArtifactID},
		"unsupported alone": {Unsupported: []string{"unknown"}},
		"strategy alone":    {Strategy: NewManaged},
		"unknown strategy":  {Strategy: "unknown", Stage: "committed", TargetIdentity: "target", TargetGeneration: 1},
		"partial adopt":     {Strategy: AdoptExisting, Stage: "committed", TargetIdentity: "target", TargetGeneration: 1},
	} {
		t.Run(name, func(t *testing.T) {
			op := ownedFresh()
			op.AdminInitialized = true
			op.Strategy = checkpoint
			if op.Validate() == nil {
				t.Fatal("partial or unsupported strategy accepted")
			}
		})
	}
	for _, checkpoint := range []FreshStrategyCheckpoint{managed, external, adopt} {
		for name, mutate := range map[string]func(*FreshStrategyCheckpoint){
			"missing target":     func(c *FreshStrategyCheckpoint) { c.TargetIdentity = "" },
			"missing generation": func(c *FreshStrategyCheckpoint) { c.TargetGeneration = 0 },
			"unknown stage":      func(c *FreshStrategyCheckpoint) { c.Stage = "unknown" },
			"invalid artifact":   func(c *FreshStrategyCheckpoint) { c.TargetArtifactID = "sha256:invalid" },
		} {
			t.Run(string(checkpoint.Strategy)+"/"+name, func(t *testing.T) {
				op := ownedFresh()
				op.AdminInitialized = true
				op.Strategy = checkpoint
				mutate(&op.Strategy)
				if op.Validate() == nil {
					t.Fatal("malformed strategy accepted")
				}
			})
		}
	}
	op := ownedFresh()
	op.AdminInitialized = true
	op.Strategy = adopt
	op.Strategy.AdoptionDecisions = op.Strategy.AdoptionDecisions[:1]
	if op.Validate() == nil {
		t.Fatal("adoption checkpoint silently dropped discovered manual resources")
	}
}

func freshKeysDecision() AdoptionResourceDecision {
	return AdoptionResourceDecision{ResourceRef: AdoptionRefClientAPIKeys, Kind: APIKeys, Scope: ClientAPIKeys, ReadEvidenceRef: "preflight:keys-read", ReplayEvidenceRef: "preflight:keys-replay", VerifyEvidenceRef: "preflight:keys-verify", Qualification: CapabilityQualified, Disposition: Migrate}
}

func freshManualDecision(ref ResourceRef) AdoptionResourceDecision {
	decision, _ := FreshAdoptionDecision(ref, AdoptionResourceDecision{})
	return decision
}

func TestFreshAdoptionInventoryRequiresExactlyOneDecisionPerResource(t *testing.T) {
	keys := freshKeysDecision()
	base := func() FreshStrategyCheckpoint {
		return FreshStrategyCheckpoint{
			Strategy: AdoptExisting, Stage: "staged", TargetIdentity: "runtime-adopt", TargetGeneration: 4,
			TargetArtifactID: "sha256:" + strings.Repeat("c", 64),
			SourceIdentity:   "external:sha256:" + strings.Repeat("d", 64), SourceClass: AdoptLegacyManaged,
			DestinationConnectionIdentity: "external:sha256:" + strings.Repeat("e", 64),
			AdoptionResources:             []ResourceRef{AdoptionRefClientAPIKeys, AdoptionRefCPAAuthFiles},
			AdoptionDecisions:             []AdoptionResourceDecision{keys, freshManualDecision(AdoptionRefCPAAuthFiles)},
		}
	}
	if err := base().validate(); err != nil {
		t.Fatalf("valid inventory rejected: %v", err)
	}
	for name, mutate := range map[string]func(*FreshStrategyCheckpoint){
		"duplicate resource": func(c *FreshStrategyCheckpoint) {
			c.AdoptionResources = []ResourceRef{AdoptionRefClientAPIKeys, AdoptionRefClientAPIKeys}
		},
		"missing decision": func(c *FreshStrategyCheckpoint) { c.AdoptionDecisions = c.AdoptionDecisions[:1] },
		"extra decision": func(c *FreshStrategyCheckpoint) {
			c.AdoptionDecisions = append(c.AdoptionDecisions, freshManualDecision(AdoptionRefConfigUnknown))
		},
		"unknown resource": func(c *FreshStrategyCheckpoint) {
			c.AdoptionResources = append(c.AdoptionResources, "attacker-supplied")
			c.AdoptionDecisions = append(c.AdoptionDecisions, AdoptionResourceDecision{ResourceRef: "attacker-supplied", Kind: CPAConfiguration, Scope: ManagementAPIFields, Qualification: CapabilityPending, ManualAction: ReconfigureAfterAdoption, Disposition: ManualAction})
		},
		"migrating unknown resource": func(c *FreshStrategyCheckpoint) {
			c.AdoptionResources = append(c.AdoptionResources, AdoptionRefConfigUnknown)
			c.AdoptionDecisions = append(c.AdoptionDecisions, AdoptionResourceDecision{ResourceRef: AdoptionRefConfigUnknown, Kind: CPAConfiguration, Scope: ManagementAPIFields, Qualification: CapabilityQualified, Disposition: Migrate})
		},
		"provider resource migrated": func(c *FreshStrategyCheckpoint) {
			c.AdoptionResources = append(c.AdoptionResources, AdoptionRefProviderStateProviders)
			c.AdoptionDecisions = append(c.AdoptionDecisions, AdoptionResourceDecision{ResourceRef: AdoptionRefProviderStateProviders, Kind: ProviderRuntimeState, Scope: ProviderState, Qualification: CapabilityQualified, Disposition: Migrate})
		},
		"missing client keys": func(c *FreshStrategyCheckpoint) {
			c.AdoptionResources = []ResourceRef{AdoptionRefCPAAuthFiles}
			c.AdoptionDecisions = c.AdoptionDecisions[1:]
		},
		"missing auth files": func(c *FreshStrategyCheckpoint) {
			c.AdoptionResources = []ResourceRef{AdoptionRefClientAPIKeys}
			c.AdoptionDecisions = c.AdoptionDecisions[:1]
		},
		"inventory without decisions": func(c *FreshStrategyCheckpoint) { c.AdoptionDecisions = nil },
		"decisions without inventory": func(c *FreshStrategyCheckpoint) { c.AdoptionResources = nil },
	} {
		t.Run(name, func(t *testing.T) {
			op := ownedFresh()
			op.AdminInitialized = true
			op.Strategy = base()
			mutate(&op.Strategy)
			if op.Validate() == nil {
				t.Fatal("malformed adoption inventory accepted")
			}
		})
	}
}

func TestFreshAdoptionInventoryKeepsManualResourcesExplicit(t *testing.T) {
	if _, ok := FreshAdoptionDecision(AdoptionRefClientAPIKeys, freshManualDecision(AdoptionRefCPAAuthFiles)); ok {
		t.Fatal("unknown client-key shape accepted")
	}
	if _, ok := FreshAdoptionDecision(AdoptionRefClientAPIKeys, AdoptionResourceDecision{}); ok {
		t.Fatal("unqualified client-key migration accepted")
	}
	for _, ref := range []ResourceRef{AdoptionRefConfigServer, AdoptionRefConfigManagement, AdoptionRefConfigRouting, AdoptionRefConfigLogging, AdoptionRefConfigUsage, AdoptionRefConfigPlugins, AdoptionRefConfigObservability, AdoptionRefConfigUnknown, AdoptionRefProviderStateProviders, AdoptionRefProviderStateAPIKeys, AdoptionRefProviderStateClient} {
		decision, ok := FreshAdoptionDecision(ref, AdoptionResourceDecision{})
		if !ok || decision.Disposition != ManualAction || decision.Qualification != CapabilityPending || decision.ManualAction == "" || decision.ResourceRef != ref {
			t.Fatalf("resource %q was not a bounded manual action: %+v", ref, decision)
		}
	}
	if _, ok := FreshAdoptionDecision("cpa-configuration", AdoptionResourceDecision{}); ok {
		t.Fatal("retired aggregate configuration ref accepted")
	}
}
