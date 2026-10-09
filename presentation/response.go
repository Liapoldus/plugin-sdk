package presentation

import (
	"encoding/json"
	"net/http"

	"github.com/Liapoldus/plugin-sdk/domain/models"
)

// problemDocument is the only refusal body this layer writes. The outcome is
// included when the refusal belongs to a lifecycle operation and omitted for a
// transport refusal that happened before an outcome existed, so a client can
// tell a Core-announced lifecycle failure from a request the transport itself
// turned away. It carries no cause, path, address, document or secret.
type problemDocument struct {
	Outcome string `json:"outcome,omitempty"`
	Code    string `json:"code"`
}

// writeDocument writes exact bytes under the injected media type and status.
func (set *HandlerSet) writeDocument(writer http.ResponseWriter, mediaType string, status int, contents []byte) {
	writer.Header().Set("content-type", mediaType)
	writer.WriteHeader(status)
	if _, err := writer.Write(contents); err != nil {
		// The status is committed. Abort rather than publish a truncated document.
		panic(http.ErrAbortHandler)
	}
}

// writeJSON serialises one value this layer owns and writes it under the
// injected media type and status. A value that cannot be serialised never
// reaches the wire as a partial body; it is replaced by the contract's own
// internalError refusal.
func (set *HandlerSet) writeJSON(writer http.ResponseWriter, mediaType string, status int, value any) {
	contents, err := json.Marshal(value)
	if err != nil {
		set.writeTransportProblem(writer, internalErrorKey)
		return
	}
	set.writeDocument(writer, mediaType, status, contents)
}

// writeTransportProblem answers a refusal this transport layer raised on its own:
// a status and a code taken from the injected transport problems, with no
// outcome. A key the contract does not register degrades to the contract's own
// internalError refusal rather than to a status invented here.
func (set *HandlerSet) writeTransportProblem(writer http.ResponseWriter, key string) {
	problem, ok := set.contracts.transportProblem(key)
	if !ok {
		problem, ok = set.contracts.transportProblem(internalErrorKey)
		if !ok {
			return
		}
	}
	// problemDocument holds two strings, so this cannot fail; the result is used
	// directly to keep a refusal from recursing through the writer above.
	contents, err := json.Marshal(problemDocument{Code: problem.Code})
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	set.writeDocument(writer, set.contracts.ContentTypes.JSON, problem.Status, contents)
}

// writeErrorProblem writes a contract error without inventing a lifecycle
// outcome. Artifact callbacks may reject product metadata before accepting the
// stream; the SDK publishes only the contract-owned status and code.
func (set *HandlerSet) writeErrorProblem(writer http.ResponseWriter, key string) {
	problem, ok := set.contracts.problem(key)
	if !ok {
		set.writeTransportProblem(writer, internalErrorKey)
		return
	}
	contents, err := json.Marshal(problemDocument{Code: problem.Code})
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	set.writeDocument(writer, set.contracts.ContentTypes.JSON, problem.Status, contents)
}

// writeOutcomeProblem answers a refusal that belongs to a lifecycle operation:
// the contract outcome on the wire next to the status and code that outcome owns.
// An outcome the contract registers no specific problem for, and an outcome
// outside the closed vocabulary, both degrade to the contract's own internalError
// status and code. The reason the lifecycle returned is never inspected, parsed
// or forwarded, so no cause can leak into a response.
func (set *HandlerSet) writeOutcomeProblem(writer http.ResponseWriter, outcome models.Outcome) {
	problem, ok := set.contracts.outcomeProblem(outcome)
	if !ok {
		return
	}
	contents, err := json.Marshal(problemDocument{
		Outcome: problemForOutcome(outcome),
		Code:    problem.Code,
	})
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	set.writeDocument(writer, set.contracts.ContentTypes.JSON, problem.Status, contents)
}
