package presentation

import (
	"errors"

	"github.com/Liapoldus/plugin-sdk/v2/domain/models"
)

// ErrInvalidContracts is returned when the injected contract cannot produce a
// conformant handler set. It names no field, path, media type, status or code
// value, so it can be logged without disclosing the contract.
var ErrInvalidContracts = errors.New("invalid Plugin SDK contract")

// These are the contract's own logical problem keys. They are the names the
// contract registers each refusal under; the status and the code a key owns
// always come from the injected maps and never from this package. Declaring them
// once keeps a handler from spelling a contract key on its own and keeps the two
// slices below in step with the refusals this layer can actually raise.
const (
	notFoundKey             = "notFound"
	methodNotAllowedKey     = "methodNotAllowed"
	payloadOversizedKey     = "payloadOversized"
	unsupportedMediaTypeKey = "unsupportedMediaType"
	internalErrorKey        = "internalError"
	notReadyKey             = "notReady"
	invalidRequestKey       = "invalidRequest"
)

var (
	// transportProblemKeys are the five refusals this transport layer raises
	// itself, before or outside any lifecycle outcome: an unknown path, a wrong
	// method, a body or document past its limit, an unusable media type and a
	// failure it cannot describe. They are required to be present in
	// Contracts.Problems at construction time.
	transportProblemKeys = []string{
		notFoundKey,
		methodNotAllowedKey,
		payloadOversizedKey,
		unsupportedMediaTypeKey,
		internalErrorKey,
	}
	// errorProblemKeys are the contract problem keys that an outcome or a
	// document status can resolve to. They are required to be present in
	// Contracts.Errors at construction time, so a refusal always has a status and
	// a code to write. They exist for validation only: a handler reaches a
	// specific one through the contract's outcome mapping, never by name.
	errorProblemKeys = []string{
		"invalidRequest",
		"notPermitted",
		"unknownGeneration",
		"staleGeneration",
		"generationConflict",
		notReadyKey,
		"applyRejected",
		"documentMalformed",
		"documentOversized",
		"digestMismatch",
		"schemaVersionMismatch",
		"configurationUnavailable",
		"cancelled",
		"deadlineExceeded",
		"grantUnknown",
		"grantDenied",
		"grantExpired",
		"grantSpent",
	}
)

// notReadyProblem returns the status a replica that has no acknowledged
// generation is answered with, so a refusal to serve is still the readiness
// document and never an invented status.
func (contracts Contracts) notReadyProblem() (Problem, bool) {
	return contracts.problem(notReadyKey)
}

// problemForOutcome reports the contract outcome a refusal should publish next
// to its code. It exists so a handler never has to decide, on its own, what
// belongs in a problem document.
func problemForOutcome(outcome models.Outcome) string {
	if !outcome.Valid() {
		return ""
	}
	return string(outcome)
}
