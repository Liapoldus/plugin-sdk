package models

// Outcome names one of the fixed contract outcomes of a lifecycle operation.
// The vocabulary is closed: an unknown outcome can never reach the wire.
type Outcome string

const (
	OutcomeApplied               Outcome = "applied"
	OutcomeAlreadyActive         Outcome = "alreadyActive"
	OutcomeInvalidRequest        Outcome = "invalidRequest"
	OutcomeNotPermitted          Outcome = "notPermitted"
	OutcomeUnknownGeneration     Outcome = "unknownGeneration"
	OutcomeStaleGeneration       Outcome = "staleGeneration"
	OutcomeGenerationConflict    Outcome = "generationConflict"
	OutcomeDigestMismatch        Outcome = "digestMismatch"
	OutcomeSchemaVersionMismatch Outcome = "schemaVersionMismatch"
	OutcomeDocumentMalformed     Outcome = "documentMalformed"
	OutcomeDocumentOversized     Outcome = "documentOversized"
	OutcomeApplyRejected         Outcome = "applyRejected"
	OutcomeGranted               Outcome = "granted"
	OutcomeGrantUnknown          Outcome = "grantUnknown"
	OutcomeGrantDenied           Outcome = "grantDenied"
	OutcomeGrantExpired          Outcome = "grantExpired"
	OutcomeGrantSpent            Outcome = "grantSpent"
	OutcomeSucceeded             Outcome = "succeeded"
	OutcomeCoreUnavailable       Outcome = "coreUnavailable"
	OutcomeCancelled             Outcome = "cancelled"
	OutcomeDeadlineExceeded      Outcome = "deadlineExceeded"
)

func (outcome Outcome) Valid() bool {
	switch outcome {
	case OutcomeApplied, OutcomeAlreadyActive, OutcomeInvalidRequest, OutcomeNotPermitted,
		OutcomeUnknownGeneration, OutcomeStaleGeneration, OutcomeGenerationConflict,
		OutcomeDigestMismatch, OutcomeSchemaVersionMismatch, OutcomeDocumentMalformed,
		OutcomeDocumentOversized, OutcomeApplyRejected, OutcomeGranted, OutcomeGrantUnknown,
		OutcomeGrantDenied, OutcomeGrantExpired, OutcomeGrantSpent, OutcomeSucceeded,
		OutcomeCoreUnavailable, OutcomeCancelled, OutcomeDeadlineExceeded:
		return true
	}
	return false
}

// Problem is the public-safe failure description of a lifecycle operation. It
// intentionally carries no cause, path, address, certificate or payload so it
// can be serialized directly to a peer or written to a log line.
type Problem struct {
	Outcome Outcome `json:"outcome"`
	Code    string  `json:"code"`
}
