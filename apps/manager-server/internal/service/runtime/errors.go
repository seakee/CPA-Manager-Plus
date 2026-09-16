package runtime

import (
	"errors"
	"fmt"
)

var ErrRuntimeMutationUnsupported = errors.New("runtime lifecycle mutation is unsupported")

type ProtocolErrorCode string

const (
	ProtocolErrorInvalidRequest                  ProtocolErrorCode = "invalid_request"
	ProtocolErrorUnsupportedOperation            ProtocolErrorCode = "unsupported_operation"
	ProtocolErrorRuntimeIdentityMismatch         ProtocolErrorCode = "runtime_identity_mismatch"
	ProtocolErrorStaleRuntimeGeneration          ProtocolErrorCode = "stale_runtime_generation"
	ProtocolErrorOperationIDConflict             ProtocolErrorCode = "operation_id_conflict"
	ProtocolErrorOperationStateConflict          ProtocolErrorCode = "operation_state_conflict"
	ProtocolErrorOperationPersistenceUnavailable ProtocolErrorCode = "operation_persistence_unavailable"
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
