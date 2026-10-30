package upgrade

import (
	"errors"
	"fmt"
)

// UpgradeErrorCode is the stable, machine-readable half of an Upgrade failure.
// The human half is operator text and carries no credential and no absolute
// host path.
type UpgradeErrorCode string

const (
	// UpgradeErrorTargetModified reports that the v2 data root no longer holds
	// what the recorded copy-in materialized: a member is missing, its digest
	// changed, the archive tree gained an entry the backup does not describe, or
	// the database is not the migrated store. Nothing was overwritten.
	UpgradeErrorTargetModified UpgradeErrorCode = "upgrade_target_modified"
	// UpgradeErrorSourceChanged reports that the 1.x source changed while the
	// copy-in held its fence. The members this run moved were rolled back and
	// nothing was recorded.
	UpgradeErrorSourceChanged UpgradeErrorCode = "upgrade_source_changed"
	// UpgradeErrorReconcileMismatch reports that the copied-in store and the 1.x
	// source disagree, or that the source can never complete a day again, and
	// the operator has not acknowledged those days. Nothing was marked and the
	// operation row did not advance.
	UpgradeErrorReconcileMismatch UpgradeErrorCode = "upgrade_reconcile_mismatch"
	// UpgradeErrorReconcileTargetIncomplete reports that the v2 store cannot show
	// a domain the 1.x source holds days of: the table is missing, or not one day
	// could be compared. That is lost data, not a disagreement, so no
	// acknowledgement can waive it. Nothing was marked and the operation row did
	// not advance.
	UpgradeErrorReconcileTargetIncomplete UpgradeErrorCode = "upgrade_reconcile_target_incomplete"
	// UpgradeErrorReconcileNothingCompared reports that the run established no
	// operation domain at all: the 1.x source holds none of the compared tables,
	// so nothing was compared. A run without a comparison has no result to
	// record, so the operation row did not advance and nothing was marked
	// instead of a recorded stage that names no domain.
	UpgradeErrorReconcileNothingCompared UpgradeErrorCode = "upgrade_reconcile_nothing_compared"
)

// UpgradeError is a typed Upgrade failure. Callers match it with errors.Is
// against the exported sentinels, or read Code for reporting.
type UpgradeError struct {
	Code    UpgradeErrorCode
	Message string
	err     error
}

func (e *UpgradeError) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	if e.err == nil {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.err)
}

func (e *UpgradeError) Unwrap() error { return e.err }

// Is matches by code, so a wrapped failure still reports its kind.
func (e *UpgradeError) Is(target error) bool {
	other, ok := target.(*UpgradeError)
	return ok && other != nil && other.Code == e.Code
}

// Sentinels for errors.Is. They carry no message of their own.
var (
	ErrTargetModified            = &UpgradeError{Code: UpgradeErrorTargetModified}
	ErrSourceChanged             = &UpgradeError{Code: UpgradeErrorSourceChanged}
	ErrReconcileMismatch         = &UpgradeError{Code: UpgradeErrorReconcileMismatch}
	ErrReconcileTargetIncomplete = &UpgradeError{Code: UpgradeErrorReconcileTargetIncomplete}
	ErrReconcileNothingCompared  = &UpgradeError{Code: UpgradeErrorReconcileNothingCompared}
)

func newUpgradeError(code UpgradeErrorCode, message string, err error) error {
	return &UpgradeError{Code: code, Message: message, err: err}
}

// UpgradeErrorCodeOf reports the code of the first typed Upgrade failure in
// err's chain.
func UpgradeErrorCodeOf(err error) (UpgradeErrorCode, bool) {
	var typed *UpgradeError
	if errors.As(err, &typed) && typed != nil {
		return typed.Code, true
	}
	return "", false
}
