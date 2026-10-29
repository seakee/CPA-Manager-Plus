// Package freshconfig owns the Manager-side write loop of the gateway
// configuration page. The page never edits YAML: it projects the scoped leaves
// of one CPA v8 configuration document, saves one page group at a time through
// CPA's own management endpoints, and journals every attempt so an unknown
// result is resolved by reading back and never by resending the write.
package freshconfig

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
)

var (
	// ErrInvalid rejects a request the page contract does not allow.
	ErrInvalid = errors.New("invalid gateway config request")
	// ErrNotFound reports an operation ID this Manager never journaled.
	ErrNotFound = errors.New("config operation not found")
	// ErrRulesReadOnly reports a payload rule write on a profile without an
	// exclusive write window. External CPAs stay rule-read-only by design.
	ErrRulesReadOnly = errors.New("payload rule families are read-only")
	// ErrReadOnly reports a profile whose configuration scope is frozen.
	ErrReadOnly = errors.New("gateway config is read-only")
)

// The bounded page codes this Manager owns. A page code is product knowledge
// the browser branches on, so the set is closed: a string CPA chose is journaled
// as evidence but is NEVER a page code.
const (
	// CodeCPARejected reports a write CPA parsed, validated and refused. The
	// page shows CPA's redacted diagnostic instead of guessing from a status.
	CodeCPARejected = "cpa_rejected_config"
	// CodeProxyURLCredentials names a proxy URL carrying inline credentials.
	CodeProxyURLCredentials = "proxy_url_credentials"
	// CodeInvalidConfig names a value outside the private profile's range.
	CodeInvalidConfig = "invalid_config"
)

// ManagerCode reports whether code is one of this Manager's own page codes. An
// unknown code is never passed through: the reply falls back to a bounded one.
func ManagerCode(code string) bool {
	switch code {
	case CodeProxyURLCredentials, CodeInvalidConfig:
		return true
	}
	return false
}

// Rejection is a refusal this Manager decided before CPA was asked: the request
// shape, a proxy URL with credentials, a private value outside its range. Its
// Code is one of the Manager's own bounded page codes.
type Rejection struct {
	Code    string
	Field   string
	Message string
}

func (r Rejection) Error() string {
	if r.Field != "" {
		return r.Code + ":" + r.Field
	}
	return r.Code
}

// CPARejection is CPA's own verdict on a write: it parsed, validated and
// refused, so the stored document is unchanged and the draft is still the
// user's. Code is CPA's string and stays in the journal as evidence; the page
// receives it as CodeCPARejected with the field and the redacted diagnostic.
type CPARejection struct {
	Code    string
	Field   string
	Message string
}

func (r CPARejection) Error() string {
	if r.Field != "" {
		return r.Code + ":" + r.Field
	}
	return r.Code
}

// Profile modes of one installation.
const (
	ModeManaged     = "managed"
	ModeExternal    = "external"
	ModePrivate     = "private"
	ModeUnavailable = "unavailable"
)

// Reasons are stable codes the page translates; they never carry a value.
const (
	ReasonRulesExternal   = "external_rule_write_unproven"
	ReasonPrivateFrozen   = "private_profile_scope_frozen"
	ReasonUnavailable     = "config_authority_unavailable"
	ReasonJournalInvalid  = "config_journal_invalid"
	ReasonUnknownOriginal = "original_operation_unknown"
	// ReasonLegacyUnresolved blocks a group whose pre-cutover field operation was
	// never resolved. It is distinct from ReasonUnknownOriginal because the page
	// has no group-level record to reconcile: the old field operation is settled
	// through its own journal.
	ReasonLegacyUnresolved = "legacy_operation_unresolved"
	ReasonReadUnavailable  = "config_read_unavailable"
	ReasonNotInstalled     = "installation_incomplete"
	// ReasonManagementRejected reports a user's CPA refusing the stored
	// management credential. It is distinct from an unavailable authority
	// because only rebinding the connection resolves it.
	ReasonManagementRejected = "external_management_key_rejected"
	// ReasonTargetMoved reports the committed External target no longer naming the
	// connection this Manager holds. The installation exists, so it is not an
	// unavailable authority: only rebinding the connection reaches it again.
	ReasonTargetMoved = "external_target_moved"
	// ReasonPluginManagedOnly answers every profile that does not own the
	// Supervisor-published plugin root. The plugin switch is the managed
	// authority's alone, so it fails closed instead of borrowing a value another
	// CPA owns.
	ReasonPluginManagedOnly = "plugin_switch_requires_managed_install"
	// ReasonVersionUnsupported answers a write to an External CPA that did not
	// prove the v8 contract this Manager writes. Reads stay open: the page can
	// still show the user what their CPA serves.
	ReasonVersionUnsupported = cpa.ReasonExternalVersionUnsupported
)

// ErrGenerationMismatch refuses a save the browser built against another CPA
// target generation. It is a typed conflict so the page can re-read and keep the
// draft instead of reporting a moved baseline.
var ErrGenerationMismatch = errors.New("gateway config target generation mismatch")

// ErrLegacyUnresolved refuses a save of a group that still carries a pre-cutover
// field operation. Resolving that operation reads the document, so a newer write
// would let it observe a value it never wrote.
var ErrLegacyUnresolved = errors.New("legacy config operation is unresolved")

// ErrPluginAuthority refuses the plugin runtime switch on any profile but the
// install-derived managed authority. The plugin root belongs to the Supervisor's
// destination, so no other profile can prove what the switch applies to.
var ErrPluginAuthority = errors.New("plugin switch requires the managed authority")

// ProfileLimits is the write shape one profile accepts. The private H1 profile is
// the only one that reserves a single leaf per operation and has no delete, so it
// is the only profile that carries limits at all.
type ProfileLimits struct {
	// SingleEdit means a group save carries exactly one "set" edit.
	SingleEdit bool `json:"single_edit"`
	// UseDefault is whether "restore the CPA default" is offered.
	UseDefault bool `json:"use_default"`
}

// Capability is what this installation may do with the page right now.
type Capability struct {
	Mode          string `json:"mode"`
	Readable      bool   `json:"readable"`
	Writable      bool   `json:"writable"`
	RulesWritable bool   `json:"rulesWritable"`
	Reason        string `json:"reason,omitempty"`
}

// FieldView is one projected leaf. It carries no YAML and no secret.
type FieldView struct {
	Path     string `json:"path"`
	Type     string `json:"type"`
	Kind     string `json:"kind"`
	Group    string `json:"group"`
	Presence string `json:"presence"`
	Value    any    `json:"value"`
	Editable bool   `json:"editable"`
	// Inherit is the value CPA resolves while the key is absent, for the leaves
	// where that is a third state rather than an implied false. The page renders
	// inherit / on / off and never invents an off.
	Inherit *bool  `json:"inherit,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// GroupView is one page group: its confirmed baseline and its latest operation.
type GroupView struct {
	ID        string                           `json:"id"`
	Prefix    string                           `json:"prefix"`
	Paths     []string                         `json:"paths"`
	Baseline  string                           `json:"baseline"`
	Writable  bool                             `json:"writable"`
	Reason    string                           `json:"reason,omitempty"`
	Persisted string                           `json:"persisted"`
	Effective string                           `json:"effective"`
	Operation *freshstore.ConfigGroupOperation `json:"operation,omitempty"`
}

// FamilyView is one whole payload rule family. Operation is the latest save of
// the shared rules group, so a family whose write is still unknown survives a
// page reload and can be reconciled from persisted state alone.
type FamilyView struct {
	Family    string                           `json:"family"`
	Path      string                           `json:"path"`
	Rules     any                              `json:"rules"`
	Baseline  string                           `json:"baseline"`
	Writable  bool                             `json:"writable"`
	Reason    string                           `json:"reason,omitempty"`
	ReadOnly  bool                             `json:"readOnly"`
	Operation *freshstore.ConfigGroupOperation `json:"operation,omitempty"`
}

// View is the whole page read model for one installation.
type View struct {
	Capability Capability `json:"capability"`
	// Generation is the CPA target generation this installation writes. The page
	// echoes it back with every save so an operation is bound to one target.
	Generation string `json:"generation"`
	// ProfileLimits is the write shape this profile accepts. It is present only
	// where the page must constrain itself (the private H1 profile); absence
	// means the group contract applies unchanged.
	ProfileLimits *ProfileLimits `json:"profile_limits,omitempty"`
	Groups        []GroupView    `json:"groups"`
	Fields        []FieldView    `json:"fields"`
	Families      []FamilyView   `json:"families"`
	// GroupsInvalid reports a group journal this store cannot prove. The page
	// stays readable and refuses every write.
	GroupsInvalid bool `json:"groupsInvalid"`
}

// configWire is the exact transport surface the page needs. Transport implements
// it against CPA; tests supply fakes.
type configWire interface {
	ReadConfigDocument(context.Context, string) (map[string]any, error)
	PatchConfigPath(context.Context, string, string, []byte) error
	PutConfigValue(context.Context, string, string, []byte) error
	DeleteConfigPath(context.Context, string, string) error
	ReadConfigFieldValue(context.Context, string, string) (string, any, error)
	ReconcileConfigField(context.Context, cpa.ConfigFieldSource, string, string, any) (cpa.ConfigFieldSource, error)
	ConfigFieldSource(context.Context) (cpa.ConfigFieldSource, error)
	HasRuntimeJournal() bool
	// The private H1/direct profile writes one leaf per operation through the
	// Host reservation protocol, not through the group PATCH path.
	ReadConfigField(context.Context, string, string) (cpa.ConfigFieldSource, string, any, error)
	BeginConfigField(context.Context, cpa.ConfigFieldSource, string, string, any) error
	PutConfigField(context.Context, cpa.ConfigFieldSource, string, string, string, any) error
	WriteH1ConfigField(context.Context, string, cpa.H1Step, string, string, any) error
}

// h1Wire is the H1 handoff bookkeeping the private profile and legacy
// reconciliation need.
type h1Wire interface {
	Claim(context.Context, *cpa.Transport, cpa.H1Intent) (*cpa.H1Parent, error)
	Begin(context.Context, *cpa.Transport, string, int) (cpa.H1Step, error)
	Commit(context.Context, *cpa.Transport, string, int) (cpa.H1Step, error)
	ParentRecord(context.Context, *cpa.Transport, string) (*cpa.H1Parent, error)
	Finish(context.Context, *cpa.Transport, string, string) error
}

// Service serializes every page write in this process. CPA remains the only
// validator and the only writer of its file.
type Service struct {
	mu        sync.Mutex
	store     freshstore.Store
	wire      configWire
	transport *cpa.Transport
	h1        h1Wire
	target    func(context.Context, uint64) (string, error)
	// managed marks the install-derived default profile; endpoint is the
	// committed destination CPA the page writes.
	managed  bool
	endpoint string
	// targets is the installation-profile discriminator of the authority this
	// page writes through. It is the key authority's Targets bound to the
	// gateway-configuration surface, so the page can never accept a checkpoint
	// the key surface refuses. Nil means this service holds no installation
	// authority at all.
	targets func(bootstrap.FreshOperation) bool
	// external resolves the committed External target through the installation
	// authority. It is nil for every other profile.
	external ExternalAuthority
	// externalWire builds the transport of that connection; nil selects the
	// CPA v8 transport.
	externalWire func(string) configWire
	// externalProbe reads the CPA version of that connection. A save is refused
	// unless it proves the v8 contract this Manager writes; nil selects the real
	// probe, exactly as the wire does.
	externalProbe func(context.Context, string, string) (cpa.ExternalVersionReport, error)
}

// New builds the service over the Fresh CAS store and the installation
// authority. The authority supplies the management credential of exactly one
// committed target; targets decides which durable checkpoint that authority
// owns. Both are nil for profiles without a key authority.
func New(st freshstore.Store, target func(context.Context, uint64) (string, error), targets func(bootstrap.FreshOperation) bool) *Service {
	transport := cpa.NewTransport()
	return &Service{
		store: st, wire: transport, transport: transport, target: target, targets: targets,
		// The probe is the live one until a test replaces it, exactly as the wire
		// is: a save must never be gated on a version nobody asked the CPA for.
		externalProbe: cpa.ObserveExternalVersion,
	}
}

// EnableH1 switches the service onto the Host H1 handoff journal. The private H1
// profile keeps its frozen scope: its page is readable and never writable.
func (s *Service) EnableH1(h *cpa.H1Coordinator) {
	s.h1 = h
	if s.transport != nil {
		s.transport.UseH1(h)
	}
}

// EnableManaged switches the service onto the install-derived managed authority:
// the CPA it writes is the committed destination address, never the private
// loopback one. NewTransport alone dials the private address, so a managed
// installation MUST select its endpoint before serving.
func (s *Service) EnableManaged(endpoint string) {
	transport := cpa.NewTransportForEndpoint(endpoint)
	s.transport = transport
	s.wire = transport
	s.managed = true
	s.endpoint = strings.TrimRight(endpoint, "/")
}

// ExternalAuthority resolves the committed External target at one generation:
// the address and the management credential of the CPA this installation owns.
// The page never reads the Manager settings itself, so only the authority that
// proved the committed connection can answer for it.
type ExternalAuthority func(context.Context, uint64) (string, string, error)

// EnableExternal switches the service onto the authority of a committed External
// install. External writes are best-effort scalars only: without an exclusive
// write window a whole rule family is read-only.
func (s *Service) EnableExternal(authority ExternalAuthority) {
	s.external = authority
}

// externalWriteGate asks the version probe about the authority a save resolved.
// It runs before the journal entry and before every write, so a CPA that does not
// prove the v8 contract is refused with a journal that never existed. Reads never
// call it: the page still shows what the user's CPA serves.
func (s *Service) externalWriteGate(ctx context.Context, auth authority) error {
	if auth.mode != ModeExternal || s.externalProbe == nil {
		return nil
	}
	report, err := s.externalProbe(ctx, auth.endpoint, auth.secret)
	if err != nil {
		// A refused credential and a moved target are the blockers of their own
		// reads; an unreachable CPA is not evidence about its version. The probe's
		// own refusal is what the page reports.
		if errors.Is(err, cpa.ErrManagementRejected) || errors.Is(err, cpa.ErrExternalTargetMoved) {
			return err
		}
		return cpa.ErrProofUnavailable
	}
	if report.WriteBlocked {
		return cpa.ErrExternalVersionUnsupported
	}
	return nil
}

// scope is one closed write surface over the shared journal, CAS and read-back
// machinery. The gateway page and the plugin runtime switch are two scopes: the
// page never projects a plugin leaf and the plugin route never projects a page
// group, so neither widens the other while both write through one journal.
type scope struct {
	// leafPaths and groupIDs are the ordered projections of this scope.
	leafPaths   func() []string
	groupIDs    func() []string
	lookupGroup func(string) (cpa.ConfigGroup, bool)
	lookupLeaf  func(string) (cpa.ConfigLeaf, bool)
	// parseValue decodes one exact written value of this scope's leaves.
	parseValue func(string, json.RawMessage) (any, error)
	// ruleFamilies marks the scope that owns the whole payload rule families.
	ruleFamilies bool
	// managedOnly marks a scope only the install-derived managed authority may
	// read or write.
	managedOnly bool
}

var gatewayConfigScope = scope{
	leafPaths:    cpa.ConfigLeafPaths,
	groupIDs:     cpa.ConfigGroupIDs,
	lookupGroup:  cpa.LookupConfigGroup,
	lookupLeaf:   cpa.LookupConfigLeaf,
	parseValue:   cpa.ParseConfigLeafValue,
	ruleFamilies: true,
}

// ownsGroup reports whether a journaled group belongs to this scope. The gateway
// page also owns the shared payload-rules journal, which is not a page group, so
// membership is a scope question and never a plain page-group lookup.
func (sc scope) ownsGroup(id string) bool {
	if sc.ruleFamilies && id == cpa.ConfigRulesGroupID {
		return true
	}
	_, ok := sc.lookupGroup(id)
	return ok
}

// pluginConfigScope is the Supervisor-owned plugin root switch: one managed-only
// group whose single leaf the admin may flip.
var pluginConfigScope = scope{
	leafPaths:   cpa.PluginConfigLeafPaths,
	groupIDs:    cpa.PluginConfigGroupIDs,
	lookupGroup: cpa.LookupPluginConfigGroup,
	lookupLeaf:  cpa.LookupPluginConfigLeaf,
	parseValue:  cpa.ParsePluginConfigLeafValue,
	managedOnly: true,
}

// authority is the resolved source of one page read or write.
type authority struct {
	mode     string
	wire     configWire
	secret   string
	capable  Capability
	readOnly string
	// limits is the write shape this profile accepts; nil means the page's own
	// group rules apply unchanged.
	limits *ProfileLimits
	// endpoint is the resolved CPA address of an External authority. The version
	// gate reads it before a write; every other profile leaves it empty.
	endpoint string
}

// resolve proves the profile this service was constructed with and returns the
// authority for exactly the committed target generation. A record that does not
// match that authority is never a write path. A managed-only scope resolves to a
// closed refusal on every other profile.
func (s *Service) resolve(ctx context.Context, r freshstore.Record, sc scope) (authority, error) {
	if sc.managedOnly && s.external != nil && !s.managed {
		// The plugin root belongs to the Supervisor-owned destination this Manager
		// stages. An External installation owns neither, so the scope is answered
		// before the External authority is even asked: the user's CPA receives no
		// request and its management credential is never used.
		return authority{
			mode:    ModeExternal,
			capable: Capability{Mode: ModeExternal, Reason: ReasonPluginManagedOnly},
		}, ErrPluginAuthority
	}
	auth, err := s.resolveProfile(ctx, r)
	if !sc.managedOnly {
		return auth, err
	}
	if err != nil || auth.mode != ModeManaged {
		// The plugin switch belongs to the Supervisor-owned destination, so a
		// profile that does not own that CPA fails closed: no read borrowed from
		// another CPA and no write to one.
		//
		// A transient proof failure on a managed install is not the same fact as
		// "this install does not own the plugin root": reporting the profile
		// reason would tell the operator their install is the wrong shape and
		// hide the switch. This service's own profile is managed, so a proof that
		// could not be taken right now stays ErrProofUnavailable and the caller
		// retries instead of concluding anything about the install.
		if err != nil && s.managed {
			return authority{
				mode:    auth.mode,
				capable: Capability{Mode: auth.mode, Reason: ReasonUnavailable},
			}, err
		}
		return authority{
			mode:    auth.mode,
			capable: Capability{Mode: auth.mode, Reason: ReasonPluginManagedOnly},
		}, ErrPluginAuthority
	}
	return auth, nil
}

func (s *Service) resolveProfile(ctx context.Context, r freshstore.Record) (authority, error) {
	op := r.Operation
	if s.target == nil && s.external == nil {
		return authority{mode: ModeUnavailable, capable: Capability{Mode: ModeUnavailable, Reason: ReasonUnavailable}}, cpa.ErrProofUnavailable
	}
	switch {
	case s.managed:
		if s.targets == nil || !s.targets(op) || op.Strategy.TargetGeneration == 0 {
			return authority{mode: ModeUnavailable, capable: Capability{Mode: ModeUnavailable, Reason: ReasonNotInstalled}}, cpa.ErrProofUnavailable
		}
		secret, err := s.target(ctx, op.Strategy.TargetGeneration)
		if err != nil || secret == "" {
			return authority{mode: ModeUnavailable, capable: Capability{Mode: ModeUnavailable, Reason: ReasonUnavailable}}, cpa.ErrProofUnavailable
		}
		return authority{
			mode:   ModeManaged,
			wire:   s.wire,
			secret: secret,
			capable: Capability{
				Mode: ModeManaged, Readable: true, Writable: true, RulesWritable: true,
			},
		}, nil
	case s.external != nil:
		// The profile question is asked of the one discriminator, so the page can
		// never accept a checkpoint the key surfaces refuse.
		if s.targets == nil || !s.targets(op) || op.Strategy.TargetGeneration == 0 {
			return authority{mode: ModeUnavailable, capable: Capability{Mode: ModeUnavailable, Reason: ReasonNotInstalled}}, cpa.ErrProofUnavailable
		}
		baseURL, secret, err := s.external(ctx, op.Strategy.TargetGeneration)
		if err != nil {
			// A management credential the user's CPA refuses is its own refusal:
			// only rebinding the connection can resolve it, so it is never folded
			// into an unavailable authority.
			if errors.Is(err, cpa.ErrManagementRejected) {
				return authority{mode: ModeUnavailable, capable: Capability{Mode: ModeUnavailable, Reason: ReasonManagementRejected}}, cpa.ErrManagementRejected
			}
			if errors.Is(err, cpa.ErrExternalTargetMoved) {
				return authority{mode: ModeUnavailable, capable: Capability{Mode: ModeUnavailable, Reason: ReasonTargetMoved}}, cpa.ErrExternalTargetMoved
			}
			return authority{mode: ModeUnavailable, capable: Capability{Mode: ModeUnavailable, Reason: ReasonUnavailable}}, cpa.ErrProofUnavailable
		}
		if baseURL == "" || secret == "" {
			return authority{mode: ModeUnavailable, capable: Capability{Mode: ModeUnavailable, Reason: ReasonUnavailable}}, cpa.ErrProofUnavailable
		}
		endpoint, err := cpa.NormalizeV8BaseURL(baseURL)
		if err != nil {
			return authority{mode: ModeUnavailable, capable: Capability{Mode: ModeUnavailable, Reason: ReasonUnavailable}}, cpa.ErrProofUnavailable
		}
		wire := s.externalWire
		var transport configWire
		if wire != nil {
			transport = wire(endpoint)
		} else {
			transport = cpa.NewTransportForEndpoint(endpoint)
		}
		return authority{
			mode:     ModeExternal,
			wire:     transport,
			secret:   secret,
			endpoint: endpoint,
			// An External CPA may have other writers. Scalars are best-effort;
			// a whole rule family needs an exclusive window this profile cannot
			// prove.
			capable: Capability{
				Mode: ModeExternal, Readable: true, Writable: true, RulesWritable: false, Reason: ReasonRulesExternal,
			},
		}, nil
	default:
		// The private H1/direct profile keeps its frozen six-leaf scope and the
		// Host reservation protocol: one leaf per operation, reserved by the
		// Runtime, no delete. The rest of the page is readable and read-only.
		if s.targets == nil || !s.targets(op) || op.Strategy.TargetGeneration == 0 {
			return authority{mode: ModePrivate, capable: Capability{Mode: ModePrivate, Readable: false, Reason: ReasonNotInstalled}}, cpa.ErrProofUnavailable
		}
		secret, err := s.target(ctx, op.Strategy.TargetGeneration)
		if err != nil || secret == "" {
			return authority{mode: ModePrivate, capable: Capability{Mode: ModePrivate, Readable: false, Reason: ReasonUnavailable}}, cpa.ErrProofUnavailable
		}
		return authority{
			mode:   ModePrivate,
			wire:   s.wire,
			secret: secret,
			capable: Capability{
				Mode: ModePrivate, Readable: true, Writable: true, RulesWritable: false,
				Reason: ReasonPrivateFrozen,
			},
			limits: &ProfileLimits{SingleEdit: true, UseDefault: false},
		}, nil
	}
}

// Read projects the whole gateway page.
func (s *Service) Read(ctx context.Context) (View, error) { return s.read(ctx, gatewayConfigScope) }

// ReadPlugins projects the plugin runtime switch. It is a scope of its own: the
// managed authority answers it, every other profile is closed.
func (s *Service) ReadPlugins(ctx context.Context) (View, error) {
	return s.read(ctx, pluginConfigScope)
}

func (s *Service) read(ctx context.Context, sc scope) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.store.Load(ctx)
	if err != nil {
		return View{}, err
	}
	view := View{
		Capability:    Capability{Mode: ModeUnavailable, Reason: ReasonUnavailable},
		Generation:    strconv.FormatUint(r.Operation.Strategy.TargetGeneration, 10),
		GroupsInvalid: r.ConfigGroupsInvalid,
		// A projection is always an array on the wire: a scope without groups,
		// leaves or families (the plugin switch has one of the first two and no
		// families) must not hand the page a null to branch on.
		Groups:   []GroupView{},
		Fields:   []FieldView{},
		Families: []FamilyView{},
	}
	auth, _ := s.resolve(ctx, r, sc)
	view.Capability = auth.capable
	view.ProfileLimits = auth.limits
	journal := r.ConfigGroups
	if view.GroupsInvalid {
		journal = nil
	}
	var document map[string]any
	if auth.capable.Readable && auth.wire != nil {
		document, err = auth.wire.ReadConfigDocument(ctx, auth.secret)
		if err != nil {
			view.Capability.Readable = false
			view.Capability.Writable = false
			view.Capability.RulesWritable = false
			view.Capability.Reason = ReasonReadUnavailable
			document = nil
		}
	}
	// A group whose latest operation is still unknown blocks its own writes. The
	// journal is shared, so a group outside this scope is never a blocker here.
	blocked := map[string]string{}
	for id, entry := range journal {
		if entry == nil || !sc.ownsGroup(id) {
			continue
		}
		if operation, ok := entry.Operations[entry.Latest]; ok && operation.State == "unknown" {
			blocked[id] = ReasonUnknownOriginal
		}
	}
	// A pre-cutover field operation that was never resolved blocks its group. It
	// has its own reason code because the page settles it through the field
	// journal, not through a group operation. That journal operation is also
	// surfaced AS the group's operation, on every profile: it carries the request
	// ID and the unknown state the page reconciles from, so the group can be
	// unlocked. Without it the page says "reconcile it" and has no way to.
	// The private H1 profile's own projection is a gateway-page concept. A
	// managed-only scope never resolves for that profile, so its groups are never
	// the private subset: it stays the closed projection of its own table.
	private := auth.mode == ModePrivate && !sc.managedOnly
	fieldOperations := map[string]*freshstore.ConfigGroupOperation{}
	if private {
		for _, id := range cpa.PrivateConfigGroups() {
			group, ok := cpa.LookupPrivateConfigGroup(id)
			if !ok {
				continue
			}
			operation := newestPrivateOperation(r, group)
			if operation == nil {
				continue
			}
			fieldOperations[id] = operation
			if operation.State == "unknown" {
				blocked[id] = ReasonUnknownOriginal
			}
		}
	} else {
		for _, id := range sc.groupIDs() {
			group, ok := sc.lookupGroup(id)
			if !ok {
				continue
			}
			// A group journal that already holds an unknown original owns this
			// group's reconcile: its operation is the newer write, and the page
			// must settle that one first.
			if blocked[id] != "" {
				continue
			}
			operation := newestUnresolvedFieldOperation(r, group)
			if operation == nil {
				continue
			}
			fieldOperations[id] = operation
			blocked[id] = ReasonLegacyUnresolved
		}
	}
	if sc.ruleFamilies && !private && unresolvedLegacyField(r, cpa.ConfigRulesGroupID) && blocked[cpa.ConfigRulesGroupID] == "" {
		blocked[cpa.ConfigRulesGroupID] = ReasonLegacyUnresolved
	}
	// A private-profile group is writable only if it holds a private leaf. Every
	// other group of that profile stays readable and read-only.
	groupIDs := sc.groupIDs()
	if private {
		groupIDs = cpa.PrivateConfigGroups()
	}
	for _, id := range groupIDs {
		group, ok := sc.lookupGroup(id)
		if private {
			group, ok = cpa.LookupPrivateConfigGroup(id)
		}
		if !ok {
			continue
		}
		entry := GroupView{
			ID: group.ID, Prefix: group.Prefix, Paths: append([]string(nil), group.Leaves...),
			Persisted: "unknown", Effective: "unknown",
		}
		if current, ok := journal[id]; ok && current != nil {
			if operation, ok := current.Operations[current.Latest]; ok {
				copied := operation.Clone()
				entry.Operation = &copied
				entry.Persisted = operation.State
			}
		}
		if operation, ok := fieldOperations[id]; ok {
			// A per-field journal holds no group entry, so its operation is what the
			// page reconciles from after a reload — on the private profile, where it
			// is also the live write path, and on managed/External installs, where it
			// is a pre-cutover original the group must settle before it can be saved.
			entry.Operation = operation
			entry.Persisted = operation.State
		}
		if document != nil {
			entry.Baseline = groupBaseline(document, group)
		}
		groupReason := auth.readOnly
		if !auth.capable.Writable {
			groupReason = firstReason(auth.capable.Reason, ReasonUnavailable)
		}
		if view.GroupsInvalid {
			groupReason = ReasonJournalInvalid
		}
		if reason, ok := blocked[id]; ok {
			groupReason = reason
		}
		entry.Reason = groupReason
		entry.Writable = auth.capable.Writable && !view.GroupsInvalid && blocked[id] == ""
		view.Groups = append(view.Groups, entry)
	}
	for _, path := range sc.leafPaths() {
		leaf, _ := sc.lookupLeaf(path)
		field := FieldView{Path: path, Type: string(leaf.Type), Kind: string(leaf.Kind), Group: leaf.Group, Presence: "unknown"}
		if inherited, ok := cpa.ConfigLeafInheritable(path); ok {
			field.Inherit = &inherited
		}
		if document != nil {
			value, presence := cpa.LookupConfigValue(document, path)
			field.Presence, field.Value = string(presence), value
		}
		field.Reason = auth.readOnly
		if view.GroupsInvalid {
			field.Reason = ReasonJournalInvalid
		}
		if reason, ok := blocked[leaf.Group]; ok {
			field.Reason = reason
		}
		field.Editable = auth.capable.Writable && !view.GroupsInvalid && blocked[leaf.Group] == "" && document != nil
		if private && field.Editable && !leaf.Private {
			// The Host reservation protocol writes one private leaf per operation
			// and nothing else, so the page must not offer the rest.
			field.Editable = false
			field.Reason = ReasonPrivateFrozen
		}
		if !field.Editable && field.Reason == "" {
			field.Reason = firstReason(auth.capable.Reason, ReasonUnavailable)
		}
		view.Fields = append(view.Fields, field)
	}
	if sc.ruleFamilies {
		var rulesOperation *freshstore.ConfigGroupOperation
		if entry := journal[cpa.ConfigRulesGroupID]; entry != nil {
			if operation, ok := entry.Operations[entry.Latest]; ok {
				copied := operation.Clone()
				rulesOperation = &copied
			}
		}
		for _, id := range cpa.ConfigRuleFamilyIDs() {
			family, _ := cpa.LookupConfigRuleFamily(id)
			item := FamilyView{Family: family.Family, Path: family.Path, ReadOnly: true, Operation: rulesOperation}
			if document != nil {
				if value, presence := cpa.LookupConfigValue(document, family.Path); presence == cpa.ConfigPresent {
					if rules, ok := value.([]any); ok {
						item.Rules = rules
					}
				}
				item.Baseline = familyBaseline(document, family.Path)
			}
			item.Writable = auth.capable.RulesWritable && !view.GroupsInvalid && blocked[cpa.ConfigRulesGroupID] == ""
			item.ReadOnly = !item.Writable
			switch reason, isBlocked := blocked[cpa.ConfigRulesGroupID]; {
			case view.GroupsInvalid:
				item.Reason = ReasonJournalInvalid
			case isBlocked:
				item.Reason = reason
			case !auth.capable.RulesWritable:
				item.Reason = firstReason(auth.capable.Reason, ReasonUnavailable)
			}
			view.Families = append(view.Families, item)
		}
	}
	return view, nil
}

// freshkeysValidRequestID keeps the request-ID contract of the retained field
// journal and the new group journal identical.
func freshkeysValidRequestID(id string) bool { return freshkeys.ValidRequestID(id) }

// unresolvedLegacyField reports a pre-cutover field operation of this group that
// was never resolved. Reconciling it reads the current document, so a newer save
// would let the reconcile read a value it did not write.
func unresolvedLegacyField(r freshstore.Record, group string) bool {
	for path, entry := range r.Config {
		if entry == nil || r.ConfigInvalid[path] || fieldGroup(path) != group {
			continue
		}
		if operation, ok := entry.Operations[entry.Latest]; ok && operation.State == "unknown" {
			return true
		}
	}
	return false
}

func firstReason(reasons ...string) string {
	for _, reason := range reasons {
		if reason != "" {
			return reason
		}
	}
	return ""
}

// groupBaseline fingerprints every leaf of one group, presence included, so an
// outside change to any of them is a conflict even when the user edited another.
func groupBaseline(document map[string]any, group cpa.ConfigGroup) string {
	snapshot := make(map[string]any, len(group.Leaves))
	for _, path := range group.Leaves {
		value, presence := cpa.LookupConfigValue(document, path)
		if presence != cpa.ConfigPresent {
			snapshot[path] = map[string]any{"presence": string(presence)}
			continue
		}
		snapshot[path] = map[string]any{"presence": string(presence), "value": value}
	}
	return canonicalDigest(snapshot)
}

// familyBaseline fingerprints one whole rule family.
func familyBaseline(document map[string]any, path string) string {
	value, presence := cpa.LookupConfigValue(document, path)
	if presence != cpa.ConfigPresent {
		return canonicalDigest(map[string]any{"presence": string(presence)})
	}
	return canonicalDigest(map[string]any{"presence": string(presence), "value": value})
}

func canonicalDigest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return sha256Hex(encoded)
}

// Operation returns the ORIGINAL operation of one request ID. Reconciliation is
// explicit and read-only: an unknown operation is never replayed.
func (s *Service) Operation(ctx context.Context, id string, reconcile bool) (freshstore.ConfigGroupOperation, error) {
	return s.operation(ctx, gatewayConfigScope, id, reconcile)
}

// PluginOperation returns the ORIGINAL plugin switch operation of one request ID,
// with the same read-back-only reconciliation as the gateway page.
func (s *Service) PluginOperation(ctx context.Context, id string, reconcile bool) (freshstore.ConfigGroupOperation, error) {
	return s.operation(ctx, pluginConfigScope, id, reconcile)
}

func (s *Service) operation(ctx context.Context, sc scope, id string, reconcile bool) (freshstore.ConfigGroupOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !freshkeys.ValidRequestID(id) {
		return freshstore.ConfigGroupOperation{}, ErrInvalid
	}
	r, err := s.store.Load(ctx)
	if err != nil {
		return freshstore.ConfigGroupOperation{}, err
	}
	for group, entry := range r.ConfigGroups {
		if entry == nil || !sc.ownsGroup(group) {
			continue
		}
		if operation, ok := entry.Operations[id]; ok {
			if !reconcile || operation.State != "unknown" {
				return operation, nil
			}
			auth, resolveErr := s.resolve(ctx, r, sc)
			if errors.Is(resolveErr, ErrPluginAuthority) {
				return operation, resolveErr
			}
			if resolveErr != nil || !auth.capable.Readable {
				return operation, cpa.ErrApplyUnknown
			}
			return s.reconcileGroup(ctx, r, auth, operation)
		}
	}
	// A field operation journaled before the page became group-based is still
	// readable and reconcilable; it is never writable again.
	for _, path := range cpa.ConfigLeafPaths() {
		if _, ok := sc.lookupLeaf(path); !ok {
			continue
		}
		entry := r.Config[path]
		if entry == nil {
			continue
		}
		operation, ok := entry.Operations[id]
		if !ok {
			continue
		}
		if !reconcile || operation.State != "unknown" {
			return legacyView(operation), nil
		}
		auth, resolveErr := s.resolve(ctx, r, sc)
		if resolveErr != nil {
			return legacyView(operation), cpa.ErrApplyUnknown
		}
		reconciled, reconcileErr := s.reconcileFieldOperation(ctx, r, auth, operation)
		if reconcileErr != nil {
			return legacyView(operation), reconcileErr
		}
		return legacyView(reconciled), nil
	}
	return freshstore.ConfigGroupOperation{}, ErrNotFound
}

// legacyView presents a pre-cutover field operation in the group operation
// shape, so one client can read both without a second contract.
func legacyView(operation freshstore.ConfigOperation) freshstore.ConfigGroupOperation {
	edits := []freshstore.ConfigGroupEdit{}
	if operation.Field != "" {
		edits = append(edits, freshstore.ConfigGroupEdit{Path: operation.Field, Action: "set", Value: operation.Value})
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].Path < edits[j].Path })
	return freshstore.ConfigGroupOperation{
		SourceID:   operation.SourceID,
		Generation: operation.Generation,
		RequestID:  operation.RequestID,
		Group:      fieldGroup(operation.Field),
		Target:     "group",
		Prefix:     operation.Field,
		Baseline:   strings.Repeat("0", 64),
		Edits:      edits,
		Code:       operation.Code,
		State:      operation.State,
		HTTP200:    operation.HTTP200,
	}
}

func fieldGroup(path string) string {
	if leaf, ok := cpa.LookupConfigLeaf(path); ok {
		return leaf.Group
	}
	return "legacy"
}

// SupportsPlugins proves whether this saved connection can serve plugin
// resources end to end. It answers from the CPA's own configuration document: a
// build that exposes the plugin section there can serve a plugin page, and one
// that cannot produce the document cannot serve the plugin switch either.
//
// It is a capability question about the connection, never a write, so it never
// touches the journal and never needs a profile that may write.
func (s *Service) SupportsPlugins(ctx context.Context) bool {
	r, err := s.store.Load(ctx)
	if err != nil {
		return false
	}
	auth, err := s.resolveProfile(ctx, r)
	if err != nil {
		return false
	}
	if auth.wire == nil || auth.secret == "" {
		return false
	}
	document, err := auth.wire.ReadConfigDocument(ctx, auth.secret)
	if err != nil {
		return false
	}
	plugins, ok := document["plugins"].(map[string]any)
	if !ok {
		return false
	}
	// The directory the Supervisor owns is what makes the namespace servable;
	// `enabled` says nothing about capability and may legitimately be false.
	_, hasDir := plugins["dir"]
	return hasDir
}
