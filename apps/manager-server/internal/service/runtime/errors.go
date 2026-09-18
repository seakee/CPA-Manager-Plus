package runtime

import (
	"errors"
	"fmt"
)

var (
	ErrRuntimeMutationUnsupported    = errors.New("runtime lifecycle mutation is unsupported")
	ErrRuntimeObservationUnsupported = errors.New("runtime operation observation is unsupported")
)

type ProtocolErrorCode string

const (
	ProtocolErrorInvalidRequest                  ProtocolErrorCode = "invalid_request"
	ProtocolErrorUnsupportedOperation            ProtocolErrorCode = "unsupported_operation"
	ProtocolErrorRuntimeIdentityMismatch         ProtocolErrorCode = "runtime_identity_mismatch"
	ProtocolErrorStaleRuntimeGeneration          ProtocolErrorCode = "stale_runtime_generation"
	ProtocolErrorOperationIDConflict             ProtocolErrorCode = "operation_id_conflict"
	ProtocolErrorOperationNotFound               ProtocolErrorCode = "operation_not_found"
	ProtocolErrorOperationStateConflict          ProtocolErrorCode = "operation_state_conflict"
	ProtocolErrorOperationPersistenceUnavailable ProtocolErrorCode = "operation_persistence_unavailable"
	ProtocolErrorActiveArtifactUnavailable       ProtocolErrorCode = "active_artifact_unavailable"
	ProtocolErrorActiveArtifactMismatch          ProtocolErrorCode = "active_artifact_mismatch"
	ProtocolErrorUnsupportedStagingPlatform      ProtocolErrorCode = "unsupported_staging_platform"
	ProtocolErrorReleaseMetadataInvalid          ProtocolErrorCode = "release_metadata_invalid"
	ProtocolErrorTargetStageUnavailable          ProtocolErrorCode = "target_stage_unavailable"
	ProtocolErrorTargetStageCorrupt              ProtocolErrorCode = "target_stage_corrupt"
	ProtocolErrorInternal                        ProtocolErrorCode = "internal_error"
)

// ProtocolError preserves the Runtime Protocol's stable machine-readable code
// without incorporating a remote message or response body into loggable text.
type ProtocolError struct {
	Code       ProtocolErrorCode
	HTTPStatus int
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("Runtime Protocol error %q (HTTP %d)", e.Code, e.HTTPStatus)
}

func ErrorCode(err error) (ProtocolErrorCode, bool) {
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) {
		return "", false
	}
	return protocolErr.Code, true
}
