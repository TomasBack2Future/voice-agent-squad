package claims

import "errors"

var (
	ErrClaimTaken                   = errors.New("claims: item already claimed")
	ErrNotYours                     = errors.New("claims: claim held by another agent")
	ErrNotClaimed                   = errors.New("claims: no active claim on item")
	ErrReasonRequired               = errors.New("claims: --reason is required for force-release")
	ErrBlockedByOpen                = errors.New("claims: item has unresolved blockers")
	ErrItemNotFound                 = errors.New("claims: no item file matches the given id")
	ErrItemAlreadyDone              = errors.New("claims: item is already done")
	ErrExpectedHolderRequired       = errors.New("claims: expected holder is required for recovery")
	ErrRecoveryConfirmationRequired = errors.New("claims: explicit stopped-holder confirmation is required")
	ErrRecoveryEvidenceRequired     = errors.New("claims: holder session and recovery evidence are required")
	ErrHolderChanged                = errors.New("claims: active holder does not match expected holder")
	ErrHolderRecentlyActive         = errors.New("claims: expected holder has a recent active heartbeat")
	ErrProtectedResource            = errors.New("claims: protected environment requires fenced recovery")

	ErrConflictsWithActive = errors.New("claims: conflicts_with overlaps an active claim")
)
