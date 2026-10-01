package presentation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/Liapoldus/plugin-sdk/domain/models"
)

// handleHealth answers the liveness endpoint with the injected status and the
// injected body. It reads no dependency and applies no policy: liveness is a
// statement that this process is running and serving, which is the only fact the
// contract allows it to state.
func (set *HandlerSet) handleHealth(writer http.ResponseWriter, _ *http.Request) {
	set.writeJSON(writer, set.contracts.ContentTypes.JSON, set.contracts.HealthStatus, set.contracts.HealthBody)
}

// handleReady answers the readiness endpoint with the readiness document exactly
// as the replica reports it. One read is bounded by the contract readiness
// deadline, so a provider that cannot answer in time cannot hold the response
// open; a replica that has not acknowledged a generation, and a replica whose
// read ran out of time, both answer the same readiness document under the
// contract's notReady status. The document is never replaced by a problem body,
// because Core reads the pending generation and the applied generation from it.
func (set *HandlerSet) handleReady(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), set.contracts.ReadinessDeadline)
	defer cancel()

	readiness := set.readiness.Readiness(ctx)
	status := http.StatusOK
	if !readiness.Ready {
		problem, ok := set.contracts.notReadyProblem()
		if !ok {
			set.writeTransportProblem(writer, internalErrorKey)
			return
		}
		status = problem.Status
	}
	set.writeJSON(writer, set.contracts.Readiness.MediaType, status, readiness)
}

// handleIdentity answers the bootstrap identity endpoint. The contract version
// on the wire is the injected one, so a replica can never advertise a contract
// it is not answering for, and a document that fails the domain's own identity
// rules is refused rather than published.
func (set *HandlerSet) handleIdentity(writer http.ResponseWriter, _ *http.Request) {
	registration, err := set.registration.Registration(set.contracts.ContractVersion)
	if err != nil {
		set.writeTransportProblem(writer, internalErrorKey)
		return
	}
	registration.ContractVersion = set.contracts.ContractVersion
	if err := registration.Validate(); err != nil {
		set.writeTransportProblem(writer, internalErrorKey)
		return
	}
	set.writeJSON(writer, set.contracts.Registration.MediaType, http.StatusOK, registration)
}

// handleManifest answers with the plugin's own manifest bytes.
func (set *HandlerSet) handleManifest(writer http.ResponseWriter, request *http.Request) {
	set.serveMetadata(writer, request, set.metadata.Manifest, set.contracts.Manifest)
}

// handleConfigurationSchema answers with the plugin's own configuration schema
// bytes.
func (set *HandlerSet) handleConfigurationSchema(writer http.ResponseWriter, request *http.Request) {
	set.serveMetadata(writer, request, set.metadata.ConfigurationSchema, set.contracts.ConfigurationSchema)
}

// serveMetadata publishes one plugin-owned document.
//
// The bytes are written exactly as the plugin produced them: the manifest and the
// configuration schema are owned by the plugin, and re-encoding them here would
// silently change a document Core and the plugin both read. The layer therefore
// only refuses, and it refuses on the two facts the contract makes its own: a
// document that is not exactly one JSON object, and a document larger than the
// lower of the document's own limit and the metadata cap.
func (set *HandlerSet) serveMetadata(
	writer http.ResponseWriter,
	request *http.Request,
	fetch func(context.Context) ([]byte, error),
	document DocumentContract,
) {
	contents, err := fetch(request.Context())
	if err != nil {
		set.writeTransportProblem(writer, internalErrorKey)
		return
	}
	maximum := document.MaximumBytes
	if set.contracts.MaximumMetadataBytes < maximum {
		maximum = set.contracts.MaximumMetadataBytes
	}
	if int64(len(contents)) > maximum {
		set.writeTransportProblem(writer, payloadOversizedKey)
		return
	}
	if !models.ValidJSONObject(contents) {
		set.writeTransportProblem(writer, internalErrorKey)
		return
	}
	set.writeDocument(writer, document.MediaType, http.StatusOK, contents)
}

// handleReload is the only write path.
//
// The notification is read under the contract media type and the contract limit,
// decoded as exactly one object with no unknown field and no trailing value, and
// checked by the domain's own descriptor rules. Only then is the use case called,
// and a 2xx is written only for an acknowledgement the contract calls a success:
// the applied generation and an idempotent repeat of it. Every refusal is
// answered from the contract's own outcome mapping, so the status, the code and
// the reported outcome always agree with what the lifecycle actually decided.
func (set *HandlerSet) handleReload(writer http.ResponseWriter, request *http.Request) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("content-type"))
	if err != nil || mediaType != set.contracts.ReloadRequest.MediaType {
		set.writeTransportProblem(writer, unsupportedMediaTypeKey)
		return
	}
	maximum := set.contracts.ReloadRequest.MaximumBytes
	if request.ContentLength > maximum {
		set.writeTransportProblem(writer, payloadOversizedKey)
		return
	}
	// MaxBytesReader stops the read at the contract limit for a body that arrives
	// without a declared length, so the notification is bounded before it is ever
	// inspected and an oversized body is refused without being buffered whole.
	request.Body = http.MaxBytesReader(writer, request.Body, maximum)
	contents, err := io.ReadAll(request.Body)
	if err != nil {
		if oversized(err) {
			set.writeTransportProblem(writer, payloadOversizedKey)
			return
		}
		set.writeOutcomeProblem(writer, models.OutcomeInvalidRequest)
		return
	}
	// A notification with a duplicate key has two possible meanings, and the
	// lifecycle would silently honour the last one. It is refused for the same
	// reason the configuration document is: one key, one meaning, everywhere.
	if !models.ValidJSONObject(contents) {
		set.writeOutcomeProblem(writer, models.OutcomeInvalidRequest)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()

	var notification models.Reload
	if err := decoder.Decode(&notification); err != nil {
		set.writeOutcomeProblem(writer, models.OutcomeInvalidRequest)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		set.writeOutcomeProblem(writer, models.OutcomeInvalidRequest)
		return
	}
	if err := notification.Validate(); err != nil {
		set.writeOutcomeProblem(writer, models.OutcomeInvalidRequest)
		return
	}

	acknowledgement, err := set.lifecycle.Reload(request.Context(), notification)
	if err != nil {
		set.writeOutcomeProblem(writer, acknowledgement.Outcome)
		return
	}
	// An acknowledgement without a success outcome is not a success, and a
	// success outcome on an error is a wiring fault. Neither is answered 2xx.
	if !set.contracts.isSuccessOutcome(acknowledgement.Outcome) {
		set.writeTransportProblem(writer, internalErrorKey)
		return
	}
	set.writeJSON(writer, set.contracts.ReloadAcknowledgement.MediaType, http.StatusOK, acknowledgement)
}

// oversized reports whether a decode failed because the reader stopped at the
// contract limit rather than because the document was malformed.
func oversized(err error) bool {
	var limit *http.MaxBytesError
	return errors.As(err, &limit)
}

// handleMetrics writes the exposition the instrumentation collector renders.
// The handler adds no metric name, no label and no value of its own, and the
// exposition is buffered so a collector that fails to render produces the
// contract's own internalError refusal instead of a truncated body under a 200.
func (set *HandlerSet) handleMetrics(writer http.ResponseWriter, _ *http.Request) {
	var rendered bytes.Buffer
	if err := set.metrics.WriteText(&rendered); err != nil {
		set.writeTransportProblem(writer, internalErrorKey)
		return
	}
	set.writeDocument(writer, set.contracts.ContentTypes.Metrics, http.StatusOK, rendered.Bytes())
}
