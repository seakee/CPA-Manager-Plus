package freshrouting

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/routing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
)

// RoutingDocument projects FreshRoutingCheckpoint together with the derived
// missingInferenceCompletionRequired boolean for Fresh and canonical JSON views.
type RoutingDocument struct {
	OperationID                        bootstrap.OperationID          `json:"operationId,omitempty"`
	Revision                           uint64                         `json:"revision"`
	Target                             string                         `json:"target,omitempty"`
	InferenceTarget                    string                         `json:"inferenceTarget,omitempty"`
	SharedEntry                        bool                           `json:"sharedEntry,omitempty"`
	InferenceCommitted                 bool                           `json:"inferenceCommitted,omitempty"`
	MissingInferenceCompletionRequired bool                           `json:"missingInferenceCompletionRequired"`
	SuggestedControlBasePath           string                         `json:"suggestedControlBasePath,omitempty"`
	SuggestedInferenceBasePath         string                         `json:"suggestedInferenceBasePath,omitempty"`
	DraftControlBasePath               string                         `json:"draftControlBasePath,omitempty"`
	DraftInferenceBasePath             string                         `json:"draftInferenceBasePath,omitempty"`
	DraftSharedEntry                   bool                           `json:"draftSharedEntry,omitempty"`
	Stage                              bootstrap.PrefixOperationStage `json:"stage,omitempty"`
	Committed                          bool                           `json:"committed"`
}

// OperationDocument is the canonical wire view of FreshOperation.
type OperationDocument struct {
	Version          int                               `json:"version"`
	Identity         bootstrap.FreshIdentity           `json:"identity"`
	Revision         uint64                            `json:"revision"`
	AdminInitialized bool                              `json:"adminInitialized"`
	ProductComplete  bool                              `json:"productComplete"`
	GatewayPending   bool                              `json:"gatewayPending"`
	Routing          RoutingDocument                   `json:"routing"`
	Strategy         bootstrap.FreshStrategyCheckpoint `json:"strategy"`
}

// OperationViewOf projects an already-validated FreshOperation without rereading SQLite.
func OperationViewOf(op bootstrap.FreshOperation) OperationDocument {
	return OperationDocument{
		Version:          op.Version,
		Identity:         op.Identity,
		Revision:         op.Revision,
		AdminInitialized: op.AdminInitialized,
		ProductComplete:  op.ProductComplete,
		GatewayPending:   op.GatewayPending,
		Routing: RoutingDocument{
			OperationID:                        op.Routing.OperationID,
			Revision:                           op.Routing.Revision,
			Target:                             op.Routing.Target,
			InferenceTarget:                    op.Routing.InferenceTarget,
			SharedEntry:                        op.Routing.SharedEntry,
			InferenceCommitted:                 op.Routing.InferenceCommitted,
			MissingInferenceCompletionRequired: op.MissingInferenceCompletionRequired(),
			SuggestedControlBasePath:           op.Routing.SuggestedControlBasePath,
			SuggestedInferenceBasePath:         op.Routing.SuggestedInferenceBasePath,
			DraftControlBasePath:               op.Routing.DraftControlBasePath,
			DraftInferenceBasePath:             op.Routing.DraftInferenceBasePath,
			DraftSharedEntry:                   op.Routing.DraftSharedEntry,
			Stage:                              op.Routing.Stage,
			Committed:                          op.Routing.Committed,
		},
		Strategy: op.Strategy,
	}
}

// OperationView validates and fences the caller's loaded FreshOperation against
// the store's current identity and revision so mixed snapshots never escape.
func (s *Service) OperationView(ctx context.Context, op bootstrap.FreshOperation) (OperationDocument, error) {
	if err := op.Validate(); err != nil {
		return OperationDocument{}, err
	}
	if s != nil && s.store != nil {
		loaded, err := s.store.Load(ctx)
		if err != nil {
			return OperationDocument{}, err
		}
		if loaded.Operation.Identity != op.Identity || loaded.Operation.Revision != op.Revision {
			return OperationDocument{}, freshstore.ErrConflict
		}
		return OperationViewOf(loaded.Operation), nil
	}
	return OperationViewOf(op), nil
}

// RoutingProgress reports the committed/pending routing posture from SQLite.
type RoutingProgress struct {
	OperationID                        bootstrap.OperationID          `json:"operationId,omitempty"`
	Revision                           uint64                         `json:"revision"`
	Target                             string                         `json:"target,omitempty"`
	InferenceTarget                    string                         `json:"inferenceTarget,omitempty"`
	SharedEntry                        bool                           `json:"sharedEntry,omitempty"`
	Stage                              bootstrap.PrefixOperationStage `json:"stage,omitempty"`
	Committed                          bool                           `json:"committed"`
	InferenceCommitted                 bool                           `json:"inferenceCommitted"`
	MissingInferenceCompletionRequired bool                           `json:"missingInferenceCompletionRequired"`
	Strategy                           bootstrap.CPAStrategy          `json:"strategy,omitempty"`
}

func (s *Service) RoutingProgress(ctx context.Context) (RoutingProgress, error) {
	r, err := s.store.Load(ctx)
	if err != nil {
		return RoutingProgress{}, err
	}
	if err := r.Operation.Validate(); err != nil {
		return RoutingProgress{}, err
	}
	op := r.Operation
	return RoutingProgress{
		OperationID:                        op.Routing.OperationID,
		Revision:                           op.Routing.Revision,
		Target:                             op.Routing.Target,
		InferenceTarget:                    op.Routing.InferenceTarget,
		SharedEntry:                        op.Routing.SharedEntry,
		Stage:                              op.Routing.Stage,
		Committed:                          op.Routing.Committed,
		InferenceCommitted:                 op.Routing.InferenceCommitted,
		MissingInferenceCompletionRequired: op.MissingInferenceCompletionRequired(),
		Strategy:                           op.Strategy.Strategy,
	}, nil
}

// AccessCompletionStatus is the authenticated response for GET/POST
// <controlBasePath>/api/v2/installation/access/completion.
type AccessCompletionStatus struct {
	Required                   bool   `json:"required"`
	Mode                       string `json:"mode"`
	ControlBasePath            string `json:"controlBasePath"`
	InferenceBasePath          string `json:"inferenceBasePath"`
	SharedEntry                bool   `json:"sharedEntry"`
	InferenceCommitted         bool   `json:"inferenceCommitted"`
	SuggestedInferenceBasePath string `json:"suggestedInferenceBasePath,omitempty"`
}

func accessCompletionStatusOf(op bootstrap.FreshOperation) AccessCompletionStatus {
	required := op.MissingInferenceCompletionRequired()
	out := AccessCompletionStatus{
		Required:           required,
		Mode:               string(op.Strategy.Strategy),
		ControlBasePath:    op.Routing.Target,
		InferenceBasePath:  op.Routing.InferenceTarget,
		SharedEntry:        op.Routing.SharedEntry,
		InferenceCommitted: op.Routing.InferenceCommitted,
	}
	if required {
		out.SuggestedInferenceBasePath = op.Routing.SuggestedInferenceBasePath
	}
	return out
}

// EnsureMissingInferenceSuggestionAuthorized ensures a persisted 16-char CSPRNG
// inference prefix suggestion exists on an authenticated pre-inference v2 store.
func (s *Service) EnsureMissingInferenceSuggestionAuthorized(
	ctx context.Context,
	loaded freshstore.Record,
	validate func(freshstore.Record) error,
) (AccessCompletionStatus, error) {
	if validate == nil {
		return AccessCompletionStatus{}, fresh.ErrAuthority
	}
	if err := validate(loaded); err != nil {
		return AccessCompletionStatus{}, err
	}
	if err := loaded.Operation.Validate(); err != nil {
		return AccessCompletionStatus{}, err
	}
	if !loaded.Operation.MissingInferenceCompletionRequired() || loaded.Operation.Routing.SuggestedInferenceBasePath != "" {
		return accessCompletionStatusOf(loaded.Operation), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.store.Load(ctx)
	if err != nil {
		return AccessCompletionStatus{}, err
	}
	if cur.Operation.Identity != loaded.Operation.Identity {
		return AccessCompletionStatus{}, freshstore.ErrConflict
	}
	if err := validate(cur); err != nil {
		return AccessCompletionStatus{}, err
	}
	if !cur.Operation.MissingInferenceCompletionRequired() || cur.Operation.Routing.SuggestedInferenceBasePath != "" {
		return accessCompletionStatusOf(cur.Operation), nil
	}
	_, suggestion, err := bootstrap.GenerateSuggestedPrefixPair(func(candidate string) bool {
		return routing.ReservedNamespaceCollision(candidate) ||
			bootstrap.SameOrNestedBasePath(cur.Operation.Routing.Target, candidate)
	})
	if err != nil {
		return AccessCompletionStatus{}, err
	}
	updated, err := s.store.Update(ctx, cur.Operation.Identity, cur.Operation.Revision, func(rec *freshstore.Record) error {
		if err := validate(*rec); err != nil {
			return err
		}
		if rec.Operation.Routing.SuggestedInferenceBasePath == "" {
			rec.Operation.Routing.SuggestedInferenceBasePath = suggestion
		}
		return nil
	})
	if err != nil {
		return AccessCompletionStatus{}, err
	}
	return accessCompletionStatusOf(updated.Operation), nil
}

// CompleteMissingInferenceAuthorized performs the one-time authenticated
// inference-prefix setup for an existing Managed/Adopt v2 installation whose
// controlBasePath is already committed without an inferenceBasePath.
func (s *Service) CompleteMissingInferenceAuthorized(
	ctx context.Context,
	loaded freshstore.Record,
	inferenceBasePath string,
	sharedEntry bool,
	validate func(freshstore.Record) error,
) (AccessCompletionStatus, error) {
	if validate == nil {
		return AccessCompletionStatus{}, fresh.ErrAuthority
	}
	if err := validate(loaded); err != nil {
		return AccessCompletionStatus{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	r, err := s.store.Load(ctx)
	if err != nil {
		return AccessCompletionStatus{}, err
	}
	if r.Operation.Identity != loaded.Operation.Identity {
		return AccessCompletionStatus{}, freshstore.ErrConflict
	}
	if err := validate(r); err != nil {
		return AccessCompletionStatus{}, err
	}
	if err := r.Operation.Validate(); err != nil {
		return AccessCompletionStatus{}, err
	}
	switch r.Operation.Strategy.Strategy {
	case bootstrap.ConnectExternal:
		return AccessCompletionStatus{}, &bootstrap.AccessPrefixError{
			Code:   bootstrap.CodeExternalInferencePrefixForbidden,
			Detail: "external installations do not configure a managed inference prefix",
		}
	case bootstrap.NewManaged, bootstrap.AdoptExisting:
	default:
		return AccessCompletionStatus{}, ErrConflict
	}
	if !r.Operation.Routing.Committed || r.Operation.Routing.Target == "" {
		return AccessCompletionStatus{}, ErrConflict
	}
	if r.Operation.Routing.InferenceCommitted {
		if r.Operation.Routing.InferenceTarget == inferenceBasePath && r.Operation.Routing.SharedEntry == sharedEntry {
			return accessCompletionStatusOf(r.Operation), nil
		}
		return AccessCompletionStatus{}, &bootstrap.AccessPrefixError{
			Code:   bootstrap.CodeInferencePrefixAlreadyCommitted,
			Detail: "inference prefix is already committed",
		}
	}
	if err := routing.ValidateAccessPrefixPair(r.Operation.Routing.Target, inferenceBasePath, sharedEntry, false); err != nil {
		return AccessCompletionStatus{}, err
	}
	if s.driver == nil {
		return AccessCompletionStatus{}, ErrBlocked
	}

	// If an earlier attempt persisted durable intent before probe completion,
	// resume only that exact immutable target/sharedEntry pair.
	if r.Operation.Routing.InferenceTarget != "" {
		if r.Operation.Routing.InferenceTarget != inferenceBasePath || r.Operation.Routing.SharedEntry != sharedEntry {
			return AccessCompletionStatus{}, &bootstrap.AccessPrefixError{
				Code:   bootstrap.CodeInferencePrefixAlreadyCommitted,
				Detail: "pending inference prefix checkpoint already exists for a different target",
			}
		}
	} else {
		actual, err := s.driver.Inspect(ctx)
		if err != nil {
			return AccessCompletionStatus{}, err
		}
		if actual.OperationID != "" && (actual.OperationID != r.Operation.Identity.OperationID ||
			actual.InstallationID != r.Operation.Identity.InstallationID ||
			actual.Generation != r.Operation.Identity.Generation ||
			actual.Target != r.Operation.Routing.Target ||
			(actual.InferenceTarget != "" && (actual.InferenceTarget != inferenceBasePath || actual.SharedEntry != sharedEntry))) {
			return AccessCompletionStatus{}, ErrConflict
		}
		r, err = s.store.Update(ctx, r.Operation.Identity, r.Operation.Revision, func(cur *freshstore.Record) error {
			if err := validate(*cur); err != nil {
				return err
			}
			if !cur.Operation.Routing.Committed || cur.Operation.Routing.InferenceCommitted || cur.Operation.Routing.InferenceTarget != "" {
				return ErrConflict
			}
			cur.Operation.Routing.InferenceTarget = inferenceBasePath
			cur.Operation.Routing.SharedEntry = sharedEntry
			cur.Operation.Routing.Revision++
			return nil
		})
		if err != nil {
			return AccessCompletionStatus{}, err
		}
	}

	wanted := StateFor(r.Operation)
	if err := s.driver.Apply(ctx, wanted); err != nil {
		return AccessCompletionStatus{}, err
	}
	if err := s.driver.Verify(ctx, wanted); err != nil {
		return AccessCompletionStatus{}, err
	}
	r, err = s.store.Update(ctx, r.Operation.Identity, r.Operation.Revision, func(cur *freshstore.Record) error {
		if err := validate(*cur); err != nil {
			return err
		}
		if cur.Operation.Routing.InferenceTarget != inferenceBasePath || cur.Operation.Routing.SharedEntry != sharedEntry {
			return ErrConflict
		}
		cur.Operation.Routing.InferenceCommitted = true
		return nil
	})
	if err != nil {
		return AccessCompletionStatus{}, err
	}
	if err := s.driver.Apply(ctx, StateFor(r.Operation)); err != nil {
		return AccessCompletionStatus{}, err
	}
	return accessCompletionStatusOf(r.Operation), nil
}

// AccessCompletionAuthFunc resolves the caller's authenticated Fresh record and
// returns a transaction-level validate closure enforcing Admin authority.
type AccessCompletionAuthFunc func(r *http.Request) (freshstore.Record, func(freshstore.Record) error, error)

// NewAccessCompletionHandler wraps ServeAccessCompletion as an http.Handler.
func (s *Service) NewAccessCompletionHandler(auth AccessCompletionAuthFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s == nil || auth == nil {
			writeCompletionError(w, http.StatusUnauthorized, "unauthorized", "admin authentication required")
			return
		}
		loaded, validate, err := auth(r)
		if err != nil || validate == nil {
			writeCompletionError(w, http.StatusUnauthorized, "unauthorized", "admin authentication required")
			return
		}
		s.ServeAccessCompletion(w, r, loaded, validate)
	})
}

// ServeAccessCompletion serves GET and POST for
// <controlBasePath>/api/v2/installation/access/completion using the caller's
// loaded record and CAS-fenced validate closure.
func (s *Service) ServeAccessCompletion(
	w http.ResponseWriter,
	r *http.Request,
	loaded freshstore.Record,
	validate func(freshstore.Record) error,
) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if s == nil || validate == nil {
		writeCompletionError(w, http.StatusUnauthorized, "unauthorized", "admin authentication required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		status, err := s.EnsureMissingInferenceSuggestionAuthorized(r.Context(), loaded, validate)
		if err != nil {
			writeMappedCompletionError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	case http.MethodPost:
		var req struct {
			InferenceBasePath string `json:"inferenceBasePath"`
			SharedEntry       bool   `json:"sharedEntry,omitempty"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
		dec.DisallowUnknownFields()
		if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
			writeCompletionError(w, http.StatusBadRequest, string(bootstrap.CodeInvalidAccessPrefix), "invalid access completion request body")
			return
		}
		status, err := s.CompleteMissingInferenceAuthorized(r.Context(), loaded, req.InferenceBasePath, req.SharedEntry, validate)
		if err != nil {
			writeMappedCompletionError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func writeMappedCompletionError(w http.ResponseWriter, err error) {
	if errors.Is(err, fresh.ErrAuthority) {
		writeCompletionError(w, http.StatusUnauthorized, "unauthorized", "admin authentication required")
		return
	}
	if code, ok := bootstrap.AccessPrefixCodeOf(err); ok {
		status := http.StatusBadRequest
		switch code {
		case bootstrap.CodeReservedPrefixCollision,
			bootstrap.CodePrefixOverlapForbidden,
			bootstrap.CodeSharedPrefixConfirmationRequired,
			bootstrap.CodeRoutingConflict,
			bootstrap.CodeInferencePrefixAlreadyCommitted:
			status = http.StatusConflict
		}
		writeCompletionError(w, status, string(code), err.Error())
		return
	}
	switch {
	case errors.Is(err, ErrConflict), errors.Is(err, freshstore.ErrConflict):
		writeCompletionError(w, http.StatusConflict, string(bootstrap.CodeRoutingConflict), "routing state conflict")
	case errors.Is(err, ErrUnverified):
		writeCompletionError(w, http.StatusServiceUnavailable, "routing_verification_failed", "public routing verification failed")
	default:
		writeCompletionError(w, http.StatusServiceUnavailable, "routing_unavailable", "routing service unavailable")
	}
}

func writeCompletionError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Code      string `json:"code"`
		UpperCode string `json:"Code"`
		Error     struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{
		Code:      code,
		UpperCode: code,
		Error: struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{Code: code, Message: message},
	})
}
