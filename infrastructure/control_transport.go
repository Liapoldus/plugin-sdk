package infrastructure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Liapoldus/plugin-sdk/v2/domain/models"
)

// The SDK owns no endpoint path, limit, status, code, deadline or media type of
// its own: every value below is read from the versioned contract asset that
// contract.go embeds. The identifiers in this file are only the logical names
// the asset uses to register a path, a deadline, an outcome family or a
// response header; none of them is a contract value.

// controlCallKind names the contract family a control-plane call belongs to. It
// selects which outcome vocabulary a failure may be attributed to, so a status
// the contract reuses across families is never resolved against the wrong one.
type controlCallKind string

const (
	controlCallConfigPull controlCallKind = "configPull"
	controlCallReload     controlCallKind = "reload"
	controlCallSecret     controlCallKind = "secretRedemption"
)

var (
	// ErrInvalidControlCall rejects a control-plane client that was handed a
	// configuration which could never produce a safe call. It names no endpoint,
	// address, path or value.
	ErrInvalidControlCall = errors.New("invalid Plugin SDK control-plane client configuration")
	// ErrControlPlaneUnusable reports a response the SDK could not interpret as
	// the document the contract describes. It is deliberately unattributed: an
	// unusable answer is the SDK's own classification and never borrows a
	// contract outcome the peer did not publish.
	ErrControlPlaneUnusable = errors.New("unusable Plugin SDK control-plane response")
	// ErrControlRequestRefused reports a peer that answered with a published
	// contract problem.
	ErrControlRequestRefused = errors.New("refused Plugin SDK control-plane request")
	// ErrControlTransportFailed reports a call that produced no usable response
	// at all: a refused handshake, a reset connection or a lost peer.
	ErrControlTransportFailed = errors.New("unanswered Plugin SDK control-plane call")
	// ErrControlResponseOversized reports a body larger than its contract limit.
	ErrControlResponseOversized = errors.New("oversized Plugin SDK control-plane response")
	// ErrControlRequestOversized reports a request body larger than its contract
	// limit. The SDK never sends it, so a caller learns its own input was
	// unusable rather than discovering it from a peer.
	ErrControlRequestOversized = errors.New("oversized Plugin SDK control-plane request")
	// ErrControlMediaType reports a response media type the contract does not
	// allow for that document.
	ErrControlMediaType = errors.New("unusable Plugin SDK control-plane response media type")
	// ErrControlDocument reports a body that is not exactly the single JSON
	// object the contract describes.
	ErrControlDocument = errors.New("unusable Plugin SDK control-plane document")
)

// ControlTransport performs one control-plane request.
//
// *MutualTLSClient is the production implementation. It is HTTPS-only, requires
// a client certificate, pins the operator trust pool and refuses a redirect, so
// replica credentials can never reach another origin. The interface exists so a
// test fixture can supply its own keypair without production gaining a second,
// weaker way to build a control client.
type ControlTransport interface {
	Do(request *http.Request) (*http.Response, error)
}

// controlPlaneError is the one failure type the control-plane adapters return.
// It carries the contract outcome the adapter is entitled to claim and a
// public-safe reason, and never a cause, address, path, certificate, document or
// secret. The kind gates attribution: an outcome one call family observed is
// never reported for another.
type controlPlaneError struct {
	kind    controlCallKind
	outcome models.Outcome
	reason  error
}

func (failure *controlPlaneError) Error() string {
	if failure.outcome != "" {
		return "Plugin SDK control-plane " + string(failure.kind) + " call refused: " + string(failure.outcome)
	}
	return "Plugin SDK control-plane " + string(failure.kind) + " call produced no usable result"
}

func (failure *controlPlaneError) Unwrap() error {
	return failure.reason
}

func controlFailure(kind controlCallKind, outcome models.Outcome, reason error) error {
	return &controlPlaneError{kind: kind, outcome: outcome, reason: reason}
}

func controlUnusable(kind controlCallKind, reason error) error {
	return controlFailure(kind, "", reason)
}

// classifyControlOutcome reports the outcome an adapter observed for exactly the
// call family it failed in. An empty result means the adapter saw nothing it may
// attribute, and the caller's own transport classification then decides.
func classifyControlOutcome(err error, kind controlCallKind) models.Outcome {
	var failure *controlPlaneError
	if errors.As(err, &failure) && failure.kind == kind && failure.outcome.Valid() {
		return failure.outcome
	}
	return ""
}

// controlBodyFailure attributes a body read failure. Only the size limit has a
// contract outcome of its own, because only a size limit describes the document
// Core actually produced; every other read failure is a lost peer.
func controlBodyFailure(kind controlCallKind, err error) error {
	if errors.Is(err, ErrControlResponseOversized) {
		return controlFailure(kind, models.OutcomeDocumentOversized, err)
	}
	return controlUnusable(kind, err)
}

// controlProblemResolver maps a status the SDK actually saw back to the bounded
// outcome that owns it, restricted to one contract family.
//
// The contract reuses statuses across outcomes, so a status alone is frequently
// ambiguous even inside one family. The resolver therefore trusts a published
// problem code first and falls back to a status only when exactly one outcome of
// the family claims it. A response that is neither attributable nor
// self-describing is reported as unusable rather than guessed at.
type controlProblemResolver struct {
	byCode   map[string]controlProblem
	byStatus map[int][]models.Outcome
}

type controlProblem struct {
	status  int
	outcome models.Outcome
}

func newControlProblemResolver(contract HTTPContract, kind controlCallKind) (controlProblemResolver, error) {
	resolver := controlProblemResolver{
		byCode:   make(map[string]controlProblem),
		byStatus: make(map[int][]models.Outcome),
	}
	for _, outcome := range controlCallOutcomes(contract, kind) {
		// A success outcome is never published as an HTTP problem, so it can
		// never be the answer to a failure.
		if contract.IsSuccessOutcome(outcome) {
			continue
		}
		// An outcome the contract registers no problem for is equally
		// unattributable: no peer publishes it on the wire, because there is no
		// HTTP answer in which a peer could. The vocabulary still keeps the name
		// for a call that never reached a peer at all, so skipping it here is
		// what keeps the family resolvable at all. Refusing the whole family
		// instead would leave every control-plane constructor unusable.
		key, published := contract.OutcomeProblems[outcome]
		if !published {
			continue
		}
		// The asset may name a problem key it does not define. coreUnavailable is
		// exactly that case: it is a locally observed outcome, so no peer can
		// publish it in an HTTP answer. An absent key is therefore unattributable
		// and is skipped, while a key the asset does define is resolved below and
		// still fails closed when it is malformed. Skipping and failing closed are
		// kept apart on the presence of the key itself, never on the outcome name.
		if _, registered := contract.Errors[key]; !registered {
			continue
		}
		// A problem the contract does register is resolved, not skipped: a
		// registered key that names no usable status or code is a broken asset
		// and must fail closed rather than silently widen this family.
		problem, err := contract.Problem(key)
		if err != nil {
			return controlProblemResolver{}, err
		}
		entry := controlProblem{status: problem.Status, outcome: models.Outcome(outcome)}
		if _, exists := resolver.byCode[problem.Code]; !exists {
			resolver.byCode[problem.Code] = entry
		}
		resolver.byStatus[problem.Status] = append(resolver.byStatus[problem.Status], entry.outcome)
	}
	if len(resolver.byCode) == 0 {
		return controlProblemResolver{}, ErrInvalidControlCall
	}
	return resolver, nil
}

func controlCallOutcomes(contract HTTPContract, kind controlCallKind) []string {
	switch kind {
	case controlCallConfigPull:
		return contract.Outcomes.ConfigPull
	case controlCallReload:
		return contract.Outcomes.Reload
	case controlCallSecret:
		return contract.Outcomes.SecretRedemption
	default:
		return nil
	}
}

// resolve attributes a status, and the problem code the peer published with it,
// to one outcome of this call family. The code only counts when the status
// agrees with it, so a body that contradicts its own status line cannot smuggle
// an unrelated outcome into the result.
func (resolver controlProblemResolver) resolve(status int, problem models.Problem) (models.Outcome, bool) {
	if entry, ok := resolver.byCode[problem.Code]; ok && entry.status == status {
		return entry.outcome, true
	}
	if entry, ok := resolver.byCode[string(problem.Outcome)]; ok && entry.status == status {
		return entry.outcome, true
	}
	candidates := resolver.byStatus[status]
	if len(candidates) == 1 {
		return candidates[0], true
	}
	return "", false
}

// decodeControlProblem reads the problem document the SDK itself serves. A body
// that is not exactly one JSON object, or that carries no code, yields the zero
// problem so the caller falls back to the status.
func decodeControlProblem(contents []byte) models.Problem {
	var problem models.Problem
	if !models.ValidJSONObject(contents) {
		return models.Problem{}
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&problem); err != nil {
		return models.Problem{}
	}
	return problem
}

// readControlBody reads at most maximum bytes. It reads one byte past the limit
// so an oversized body is detected without ever buffering an unbounded peer
// response.
func readControlBody(body io.Reader, maximum int64) ([]byte, error) {
	if body == nil || maximum <= 0 {
		return nil, ErrControlPlaneUnusable
	}
	contents, err := io.ReadAll(io.LimitReader(body, maximum+1))
	if err != nil {
		return nil, ErrControlTransportFailed
	}
	if int64(len(contents)) > maximum {
		return nil, ErrControlResponseOversized
	}
	return contents, nil
}

// requireControlMediaType compares the response media type with the contract
// value. Both sides are parsed, because the contract publishes parameterised
// media types such as the metrics exposition type: a charset or a version
// parameter describes the same document, and comparing an unparsed contract
// string against a parsed header would refuse a correct response forever.
func requireControlMediaType(response *http.Response, expected string) error {
	if response == nil || expected == "" {
		return ErrControlMediaType
	}
	expectedType, _, err := mime.ParseMediaType(expected)
	if err != nil || expectedType == "" {
		return ErrControlMediaType
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("content-type"))
	if err != nil || !strings.EqualFold(mediaType, expectedType) {
		return ErrControlMediaType
	}
	return nil
}

// readControlExposition reads a control-plane response body whose format the SDK
// does not interpret. The metrics exposition is such a document: its grammar and
// its metric names belong to the replica that published them, and the SDK
// forwards the exact bytes without parsing them. Only the two properties the
// contract actually owns are enforced here, namely the media type and the size
// bound, so a correct non-JSON exposition is never refused for not being JSON.
func readControlExposition(response *http.Response, document DocumentContract, kind controlCallKind) ([]byte, error) {
	expected, err := controlMediaType(document.MediaType)
	if err != nil {
		return nil, controlUnusable(kind, err)
	}
	if err := requireControlMediaType(response, expected); err != nil {
		return nil, controlUnusable(kind, err)
	}
	contents, err := readControlBody(response.Body, document.MaximumBytes)
	if err != nil {
		return nil, controlBodyFailure(kind, err)
	}
	return contents, nil
}

// requireControlFields checks that a response document is one JSON object that
// carries every field the contract marks required, without interpreting any
// other field it holds.
func requireControlFields(contents []byte, required []string) error {
	if !models.ValidJSONObject(contents) {
		return ErrControlDocument
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(contents, &fields); err != nil {
		return ErrControlDocument
	}
	for _, field := range required {
		if _, present := fields[field]; !present {
			return ErrControlDocument
		}
	}
	return nil
}

// decodeControlDocument decodes exactly one JSON value into target, refusing an
// unknown field and a trailing value. A peer can therefore not smuggle a second
// document or an unrecognised field past a typed acknowledgement.
func decodeControlDocument(contents []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrControlDocument
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrControlDocument
	}
	return nil
}

// controlCallContext bounds one call with the contract deadline of its own
// operation. The SDK never shares a single whole-request timeout across
// operations whose contract deadlines differ.
func controlCallContext(ctx context.Context, seconds int) (context.Context, context.CancelFunc, error) {
	if seconds <= 0 {
		return nil, nil, ErrInvalidControlCall
	}
	bounded, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	return bounded, cancel, nil
}

// performControlCall runs one control-plane request. A transport that yields
// nothing usable is a cancelled call, an expired call or a lost peer, and is
// classified from the context alone; no transport error string is ever parsed or
// propagated.
func performControlCall(transport ControlTransport, request *http.Request, kind controlCallKind) (*http.Response, error) {
	if transport == nil || request == nil {
		return nil, controlUnusable(kind, ErrInvalidControlCall)
	}
	response, err := transport.Do(request)
	if err == nil && response != nil {
		return response, nil
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return nil, controlFailure(kind, models.OutcomeDeadlineExceeded, ErrControlTransportFailed)
	case errors.Is(err, context.Canceled):
		return nil, controlFailure(kind, models.OutcomeCancelled, ErrControlTransportFailed)
	default:
		return nil, controlUnusable(kind, ErrControlTransportFailed)
	}
}

func performArtifactControlCall(transport ArtifactControlTransport, request *http.Request) (*http.Response, error) {
	if transport == nil || request == nil {
		return nil, controlUnusable(controlCallReload, ErrInvalidControlCall)
	}
	response, err := transport.DoArtifact(request)
	if err == nil && response != nil {
		return response, nil
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(request.Context().Err(), context.DeadlineExceeded):
		return nil, controlFailure(controlCallReload, models.OutcomeDeadlineExceeded, ErrControlTransportFailed)
	case errors.Is(err, context.Canceled) || errors.Is(request.Context().Err(), context.Canceled):
		return nil, controlFailure(controlCallReload, models.OutcomeCancelled, ErrControlTransportFailed)
	default:
		return nil, controlUnusable(controlCallReload, ErrControlTransportFailed)
	}
}

func performAdminActionControlCall(transport AdminActionControlTransport, request *http.Request) (*http.Response, error) {
	if transport == nil || request == nil {
		return nil, controlUnusable(controlCallReload, ErrInvalidControlCall)
	}
	response, err := transport.DoAdminAction(request)
	if err == nil && response != nil {
		return response, nil
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(request.Context().Err(), context.DeadlineExceeded):
		return nil, controlFailure(controlCallReload, models.OutcomeDeadlineExceeded, ErrControlTransportFailed)
	case errors.Is(err, context.Canceled) || errors.Is(request.Context().Err(), context.Canceled):
		return nil, controlFailure(controlCallReload, models.OutcomeCancelled, ErrControlTransportFailed)
	default:
		return nil, controlUnusable(controlCallReload, ErrControlTransportFailed)
	}
}

// requireControlRequestSize refuses a request body the contract would not accept
// before it is sent, so an oversized call never reaches a peer.
func requireControlRequestSize(contents []byte, maximum int64) error {
	if maximum <= 0 {
		return ErrInvalidControlCall
	}
	if int64(len(contents)) > maximum {
		return ErrControlRequestOversized
	}
	return nil
}

// hasControlHeader reports whether a response carries a contract header at all.
// An empty value and an absent header are the same defect, and neither is
// defaulted.
func hasControlHeader(response *http.Response, name string) bool {
	return response != nil && name != "" && strings.TrimSpace(response.Header.Get(name)) != ""
}
