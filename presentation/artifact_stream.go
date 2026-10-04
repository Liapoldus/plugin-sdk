package presentation

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/Liapoldus/plugin-sdk/domain/models"
)

type countedReader struct {
	reader io.Reader
	bytes  int64
}

func (reader *countedReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	reader.bytes += int64(count)
	return count, err
}

func (set *HandlerSet) handleArtifactStream(writer http.ResponseWriter, request *http.Request) {
	if set.artifacts == nil {
		set.writeTransportProblem(writer, notFoundKey)
		return
	}
	defer request.Body.Close()

	stream := set.contracts.ArtifactStream
	deadline := time.Now().Add(stream.Deadline)
	controller := http.NewResponseController(writer)
	if err := controller.SetReadDeadline(deadline); err != nil {
		set.writeTransportProblem(writer, internalErrorKey)
		return
	}
	if err := controller.SetWriteDeadline(deadline); err != nil {
		set.writeTransportProblem(writer, internalErrorKey)
		return
	}
	ctx, cancel := context.WithDeadline(request.Context(), deadline)
	defer cancel()
	request = request.WithContext(ctx)
	invocation, err := artifactInvocationFromHeaders(request.Header, stream.InvocationContext)
	if err != nil {
		set.writeErrorProblem(writer, invalidRequestKey)
		return
	}
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("content-type"))
	if err != nil || mediaType != stream.MediaType || parameters["boundary"] == "" {
		set.writeTransportProblem(writer, unsupportedMediaTypeKey)
		return
	}
	if request.ContentLength > stream.MaximumRequestBytes {
		set.writeTransportProblem(writer, payloadOversizedKey)
		return
	}
	counted := &countedReader{reader: http.MaxBytesReader(writer, request.Body, stream.MaximumRequestBytes)}
	reader := multipart.NewReader(counted, parameters["boundary"])

	metadataPart, err := reader.NextPart()
	if err != nil || metadataPart.FormName() != stream.Parts[0] || metadataPart.FileName() != "" {
		if tooLarge(err) {
			set.writeTransportProblem(writer, payloadOversizedKey)
			return
		}
		set.writeErrorProblem(writer, invalidRequestKey)
		return
	}
	metadataType, _, mediaErr := mime.ParseMediaType(metadataPart.Header.Get("content-type"))
	if mediaErr != nil || metadataType != stream.MetadataMediaType {
		_ = metadataPart.Close()
		set.writeTransportProblem(writer, unsupportedMediaTypeKey)
		return
	}
	metadata, err := io.ReadAll(io.LimitReader(metadataPart, stream.MaximumMetadataBytes+1))
	closeErr := metadataPart.Close()
	if err != nil || closeErr != nil {
		if tooLarge(err, closeErr) {
			set.writeTransportProblem(writer, payloadOversizedKey)
		} else {
			set.writeErrorProblem(writer, invalidRequestKey)
		}
		return
	}
	if int64(len(metadata)) > stream.MaximumMetadataBytes || !models.ValidJSONObject(metadata) {
		if int64(len(metadata)) > stream.MaximumMetadataBytes {
			set.writeTransportProblem(writer, payloadOversizedKey)
		} else {
			set.writeErrorProblem(writer, invalidRequestKey)
		}
		return
	}

	artifactPart, err := reader.NextPart()
	if err != nil || artifactPart.FormName() != stream.Parts[1] || artifactPart.FileName() != "" {
		if artifactPart != nil {
			_ = artifactPart.Close()
		}
		set.writeErrorProblem(writer, invalidRequestKey)
		return
	}
	artifactType, _, mediaErr := mime.ParseMediaType(artifactPart.Header.Get("content-type"))
	if mediaErr != nil || artifactType == "" {
		_ = artifactPart.Close()
		set.writeTransportProblem(writer, unsupportedMediaTypeKey)
		return
	}
	validated := &validatedArtifactReader{
		part: artifactPart, multipart: reader, counted: counted, contract: stream,
		metadataBytes: int64(len(metadata)),
	}
	result, acceptErr := set.artifacts.AcceptArtifact(request.Context(), ArtifactInput{
		Invocation: invocation, Metadata: metadata, ContentType: artifactType, Body: validated,
	})
	callbackConsumedCompleteBody := validated.verified
	// The validating reader reports EOF only after it has verified the complete
	// multipart envelope. Drain a callback that deliberately stopped early, but
	// never treat its result as accepted unless full validation reached EOF.
	_, drainErr := io.Copy(io.Discard, validated)
	if tooLarge(acceptErr, drainErr, validated.failure) {
		set.writeTransportProblem(writer, payloadOversizedKey)
		return
	}
	if acceptErr != nil || drainErr != nil || !callbackConsumedCompleteBody {
		set.writeErrorProblem(writer, invalidRequestKey)
		return
	}
	if !validArtifactResponse(result, stream) {
		set.writeTransportProblem(writer, internalErrorKey)
		return
	}
	set.writeDocument(writer, set.contracts.ContentTypes.JSON, result.StatusCode, result.Body)
}

func artifactInvocationFromHeaders(headers http.Header, contract ArtifactInvocationContract) (models.ArtifactInvocation, error) {
	values := make(map[string]string, len(contract.Headers))
	total := 0
	for logical, name := range contract.Headers {
		candidates := headers.Values(name)
		if len(candidates) > 1 {
			return models.ArtifactInvocation{}, models.ErrInvalidArtifactInvocation
		}
		if len(candidates) == 0 || candidates[0] == "" {
			if hasString(contract.Required, logical) {
				return models.ArtifactInvocation{}, models.ErrInvalidArtifactInvocation
			}
			continue
		}
		value := candidates[0]
		for index := range value {
			if value[index] < 0x20 || value[index] == 0x7f {
				return models.ArtifactInvocation{}, models.ErrInvalidArtifactInvocation
			}
		}
		total += len(logical) + len(name) + len(value)
		values[logical] = value
	}
	if total > contract.MaximumBytes {
		return models.ArtifactInvocation{}, models.ErrInvalidArtifactInvocation
	}
	invocation := models.ArtifactInvocation{
		CallerID: values["callerId"], InstanceID: values["instanceId"],
		PageID: values["pageId"], ActionID: values["actionId"],
		SurfaceDigest: values["surfaceDigest"], IdempotencyKey: values["idempotencyKey"],
		RequestID: values["requestId"], IfMatch: values["ifMatch"],
	}
	return invocation, invocation.Validate()
}

func hasString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type validatedArtifactReader struct {
	part          *multipart.Part
	multipart     *multipart.Reader
	counted       *countedReader
	contract      ArtifactStreamContract
	metadataBytes int64
	artifactBytes int64
	verified      bool
	failure       error
}

func (reader *validatedArtifactReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if reader.failure != nil {
		return 0, reader.failure
	}
	if reader.verified {
		return 0, io.EOF
	}
	remaining := reader.contract.MaximumArtifactBytes - reader.artifactBytes
	if remaining < int64(len(buffer)) {
		buffer = buffer[:remaining+1]
	}
	count, err := reader.part.Read(buffer)
	reader.artifactBytes += int64(count)
	if reader.artifactBytes > reader.contract.MaximumArtifactBytes {
		reader.failure = &http.MaxBytesError{Limit: reader.contract.MaximumArtifactBytes}
		return 0, reader.failure
	}
	if !errors.Is(err, io.EOF) {
		return count, err
	}
	if reader.artifactBytes < reader.contract.MinimumArtifactBytes {
		reader.failure = io.ErrUnexpectedEOF
		return count, reader.failure
	}
	if closeErr := reader.part.Close(); closeErr != nil {
		reader.failure = closeErr
		return count, closeErr
	}
	_, trailingErr := reader.multipart.NextPart()
	if !errors.Is(trailingErr, io.EOF) {
		if trailingErr == nil {
			reader.failure = io.ErrUnexpectedEOF
		} else {
			reader.failure = trailingErr
		}
		return count, reader.failure
	}
	overhead := reader.counted.bytes - reader.metadataBytes - reader.artifactBytes
	if overhead < 0 || overhead > reader.contract.MaximumMultipartOverheadBytes || reader.counted.bytes > reader.contract.MaximumRequestBytes {
		reader.failure = &http.MaxBytesError{Limit: reader.contract.MaximumMultipartOverheadBytes}
		return count, reader.failure
	}
	reader.verified = true
	return count, io.EOF
}

func validArtifactResponse(response ArtifactResponse, contract ArtifactStreamContract) bool {
	if response.StatusCode == contract.AcceptedStatus {
		return int64(len(response.Body)) <= contract.MaximumReceiptBytes && models.ValidJSONObject(response.Body)
	}
	return response.StatusCode >= 400 && response.StatusCode < 600 &&
		int64(len(response.Body)) <= contract.MaximumReceiptBytes && models.ValidJSONObject(response.Body)
}

func tooLarge(errs ...error) bool {
	for _, err := range errs {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			return true
		}
	}
	return false
}
