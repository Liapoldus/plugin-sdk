package infrastructure

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/Liapoldus/plugin-sdk/v2/domain/interfaces"
	"github.com/Liapoldus/plugin-sdk/v2/domain/models"
)

// The response header names Core publishes for an exact-generation pull are
// registered in the contract asset under these keys. The values are the only
// header strings any production code sends or reads.
const (
	controlHeaderGeneration = "generation"
	controlHeaderDigest     = "sha256"
	controlHeaderSchema     = "schemaVersion"
	controlHeaderState      = "generationState"
	// controlGenerationSegment is the path parameter of the pull template.
	controlGenerationSegment = "generation"
)

var _ interfaces.ConfigurationSource = (*CoreConfigurationSource)(nil)

// CoreConfigurationSource reads one exact immutable generation from Core over
// the private mutual-TLS control API.
//
// The adapter transports and classifies. It never decides that a document may be
// applied: it preserves the exact bytes Core stored, the descriptors Core
// published and the durable slot Core reports, and it attributes only the
// failures it can actually see on the wire. Whether the document matches the
// Reload announcement, whether its digest matches its own bytes and whether it
// may be applied at all stay with the application layer, so exactly one owner
// decides each outcome.
type CoreConfigurationSource struct {
	contract  HTTPContract
	baseURL   *url.URL
	transport ControlTransport
	problems  controlProblemResolver
	headers   map[string]string
	deadline  int
}

// NewCoreConfigurationSource builds the exact-generation pull client. It fails
// closed on anything that could not produce a safe call: a base URL that is not
// a bare HTTPS origin, a missing transport, an incomplete pull contract, a
// response header the contract does not register, or a call family the contract
// describes no problem for.
func NewCoreConfigurationSource(contract HTTPContract, baseURL string, transport ControlTransport) (*CoreConfigurationSource, error) {
	parsed, valid := parseControlURL(baseURL)
	if !valid || transport == nil {
		return nil, ErrInvalidControlCall
	}
	pull := contract.Core.ConfigPull
	if pull.Method == "" || pull.PathTemplate == "" || pull.ResponseMediaType == "" ||
		pull.MaximumBytes <= 0 || len(pull.GenerationStates) == 0 ||
		contract.Deadlines.CoreConfigPullSeconds <= 0 {
		return nil, ErrInvalidControlCall
	}
	headers, err := controlPullHeaders(pull.ResponseHeaders)
	if err != nil {
		return nil, err
	}
	problems, err := newControlProblemResolver(contract, controlCallConfigPull)
	if err != nil {
		return nil, err
	}
	return &CoreConfigurationSource{
		contract:  contract,
		baseURL:   parsed,
		transport: transport,
		problems:  problems,
		headers:   headers,
		deadline:  contract.Deadlines.CoreConfigPullSeconds,
	}, nil
}

// ClassifyOutcome implements the optional adapter port of the application
// layer. It reports only what the pull actually saw, only for the pull family,
// and an empty outcome whenever the failure was not attributable to a published
// contract problem.
func (source *CoreConfigurationSource) ClassifyOutcome(err error) models.Outcome {
	if source == nil {
		return ""
	}
	return classifyControlOutcome(err, controlCallConfigPull)
}

// PullExact reads the one generation Core named in the Reload notification. It
// never lists generations, never substitutes another document and never
// normalises a descriptor: the bytes and the header values are handed on exactly
// as Core published them.
//
// A refused call is attributed to the contract outcome Core published, so the
// caller learns a stale generation from a stale generation and not from a
// generic transport failure. A successful response that does not describe a
// document is refused without a claimed outcome, because the SDK cannot tell a
// malformed document from a lost peer and the application layer owns the
// document decision.
func (source *CoreConfigurationSource) PullExact(ctx context.Context, generation string) (interfaces.PullResult, error) {
	if source == nil {
		return interfaces.PullResult{}, controlUnusable(controlCallConfigPull, ErrInvalidControlCall)
	}
	if !models.ValidGeneration(generation) {
		return interfaces.PullResult{}, controlUnusable(controlCallConfigPull, models.ErrInvalidConfiguration)
	}
	pull := source.contract.Core.ConfigPull
	target, err := source.contract.ControlURL(source.baseURL, pull.PathTemplate, generation, controlGenerationSegment)
	if err != nil {
		return interfaces.PullResult{}, controlUnusable(controlCallConfigPull, err)
	}
	callCtx, cancel, err := controlCallContext(ctx, source.deadline)
	if err != nil {
		return interfaces.PullResult{}, controlUnusable(controlCallConfigPull, err)
	}
	defer cancel()

	request, err := http.NewRequestWithContext(callCtx, pull.Method, target.String(), nil)
	if err != nil {
		return interfaces.PullResult{}, controlUnusable(controlCallConfigPull, ErrControlTransportFailed)
	}
	request.Header.Set("accept", pull.ResponseMediaType)
	response, err := performControlCall(source.transport, request, controlCallConfigPull)
	if err != nil {
		return interfaces.PullResult{}, err
	}
	defer closeResource(response.Body)

	if response.StatusCode != http.StatusOK {
		return interfaces.PullResult{}, source.refusal(response)
	}
	if err := requireControlMediaType(response, pull.ResponseMediaType); err != nil {
		return interfaces.PullResult{}, controlUnusable(controlCallConfigPull, err)
	}
	for _, name := range source.headers {
		if !hasControlHeader(response, name) {
			return interfaces.PullResult{}, controlUnusable(controlCallConfigPull, ErrControlPlaneUnusable)
		}
	}
	// The descriptors are used exactly as published. A generation that is not the
	// one that was requested, a digest that is not well formed and a schema
	// version that is not named are all left to the application layer, which
	// reports them as the distinct contract outcomes they are.
	pulledGeneration := response.Header.Get(source.headers[controlHeaderGeneration])
	digest := response.Header.Get(source.headers[controlHeaderDigest])
	schemaVersion := response.Header.Get(source.headers[controlHeaderSchema])
	if !models.ValidGeneration(pulledGeneration) || !models.ValidDigest(digest) || schemaVersion == "" {
		return interfaces.PullResult{}, controlUnusable(controlCallConfigPull, ErrControlPlaneUnusable)
	}
	state, err := source.generationState(response.Header.Get(source.headers[controlHeaderState]))
	if err != nil {
		return interfaces.PullResult{}, err
	}
	rawJSON, err := readControlBody(response.Body, pull.MaximumBytes)
	if err != nil {
		return interfaces.PullResult{}, controlBodyFailure(controlCallConfigPull, err)
	}
	configuration, err := models.NewConfiguration(pulledGeneration, schemaVersion, digest, rawJSON)
	if errors.Is(err, models.ErrInvalidDocument) {
		// The descriptors are syntactically sound and the exact bytes are
		// preserved, so only the document is at fault. Handing it on keeps the
		// documentMalformed decision with the owner of verify-then-apply policy
		// instead of turning a bad document into a transport failure here.
		return interfaces.PullResult{
			Configuration: models.Configuration{
				Generation:    pulledGeneration,
				SchemaVersion: schemaVersion,
				SHA256:        digest,
				RawJSON:       rawJSON,
			},
			State: state,
		}, nil
	}
	if err != nil {
		return interfaces.PullResult{}, controlUnusable(controlCallConfigPull, ErrControlPlaneUnusable)
	}
	return interfaces.PullResult{Configuration: configuration, State: state}, nil
}

// refusal attributes a non-success answer to the problem Core published. A body
// the SDK cannot read is not a licence to guess: the call stays unattributed and
// the caller's own transport classification decides.
func (source *CoreConfigurationSource) refusal(response *http.Response) error {
	contents, err := readControlBody(response.Body, source.contract.Core.ConfigPull.MaximumBytes)
	if err != nil {
		return controlUnusable(controlCallConfigPull, ErrControlRequestRefused)
	}
	outcome, attributed := source.problems.resolve(response.StatusCode, decodeControlProblem(contents))
	if !attributed {
		return controlUnusable(controlCallConfigPull, ErrControlRequestRefused)
	}
	return controlFailure(controlCallConfigPull, outcome, ErrControlRequestRefused)
}

// generationState maps the durable slot Core reported to the port value the
// application layer decides on. A slot the contract registers but the SDK does
// not know is refused instead of being treated as desired or as stale, so a
// contract change can never be silently answered with a guess.
func (source *CoreConfigurationSource) generationState(published string) (interfaces.GenerationState, error) {
	for _, allowed := range source.contract.Core.ConfigPull.GenerationStates {
		if allowed != published {
			continue
		}
		switch state := interfaces.GenerationState(published); state {
		case interfaces.GenerationStateActive, interfaces.GenerationStatePrevious:
			return state, nil
		}
	}
	return "", controlUnusable(controlCallConfigPull, ErrControlPlaneUnusable)
}

// controlPullHeaders requires every response header the contract registers. A
// contract that dropped one would leave the pull unable to describe what it
// received, so it is refused at construction rather than at the first call.
func controlPullHeaders(registered map[string]string) (map[string]string, error) {
	headers := make(map[string]string, len(controlHeaderNames))
	for _, key := range controlHeaderNames {
		name, present := registered[key]
		if !present || name == "" {
			return nil, ErrInvalidControlCall
		}
		headers[key] = name
	}
	return headers, nil
}

var controlHeaderNames = [...]string{
	controlHeaderGeneration,
	controlHeaderDigest,
	controlHeaderSchema,
	controlHeaderState,
}
