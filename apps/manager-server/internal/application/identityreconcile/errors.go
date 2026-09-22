package identityreconcile

import "errors"

var (
	// ErrRuntimeUnavailable indicates that the runtime status could not be observed.
	ErrRuntimeUnavailable = errors.New("runtime unavailable")

	// ErrRuntimeNotReady indicates that the runtime is not in ready state.
	ErrRuntimeNotReady = errors.New("runtime not ready")

	// ErrRuntimeObservationIncomplete indicates that runtime status is missing essential identity or generation.
	ErrRuntimeObservationIncomplete = errors.New("runtime observation incomplete")

	// ErrRuntimeFenceChanged indicates that the runtime fence (identity or generation) changed during capture, or post-status was not ready.
	ErrRuntimeFenceChanged = errors.New("runtime fence changed during capture")

	// ErrCredentialInventoryFailed indicates that credential inventory fetch failed or was incomplete.
	ErrCredentialInventoryFailed = errors.New("credential inventory incomplete or failed")

	// ErrAPIKeyInventoryFailed indicates that API key inventory fetch failed or was malformed.
	ErrAPIKeyInventoryFailed = errors.New("api-key inventory malformed or failed")

	// ErrReconciliationConflict indicates a persistence or CAS conflict during snapshot application.
	ErrReconciliationConflict = errors.New("reconciliation persistence conflict")

	// ErrConnectionResolutionFailed indicates failure to resolve the CPA management connection.
	ErrConnectionResolutionFailed = errors.New("failed to resolve CPA management connection")
)
