package infrastructure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/Liapoldus/plugin-sdk/v2/domain/interfaces"
	"github.com/Liapoldus/plugin-sdk/v2/domain/models"
)

// The plugin-to-Core secret routes are registered in the contract asset under
// their logical keys. They are the only endpoint keys this file may use: no
// path, method, member name, limit or media type is written here.

// controlSecretHandleSegment is the path parameter of the redemption template. A
// handle is an opaque bearer value, so it is percent-encoded into exactly one
// path position and never reaches a log, a metric or an error.
const controlSecretHandleSegment = "handle"

var (
	// ErrSecretGrantSpent reports that this process already offered a handle to
	// Core. A grant is one-use, so the SDK never puts the same handle on the wire
	// twice: a second attempt is refused locally and attributed to the contract
	// outcome the first attempt earned. The handle is not part of the message.
	ErrSecretGrantSpent = errors.New("plugin secret grant was already offered to Core")
	// ErrSecretGrantTrackingFull reports that the bounded spent-handle set is
	// full. The SDK can then no longer prove a later handle is unused, so it
	// refuses the redemption instead of offering a one-use handle it might not be
	// able to refuse a second time.
	ErrSecretGrantTrackingFull = errors.New("plugin secret grant replay tracking is full")
)

var _ interfaces.SecretBroker = (*CoreSecretBroker)(nil)

// CoreSecretBroker is the plugin-side client of the Core secret-grant API. It
// asks Core for a scoped, expiring, one-use grant for one reference and purpose,
// and later exchanges that grant for the value Core published.
//
// The broker transports and classifies. It never decides policy: it does not
// interpret a secret reference, does not evaluate whether a value may be applied,
// does not decide that a grant has expired and does not read a wall clock. Those
// decisions belong to the application layer, which owns grant tracking and the
// decision to use a value at all. The broker adds exactly the one thing the
// application layer cannot: a transport-level one-use guard, so no handle this
// process already put on the wire is ever put on it a second time.
//
// Every call is a single request with no retry and no redirect following. A
// redemption is one-use, so repeating a call whose outcome is unknown would burn
// the grant on an answer the SDK never sees.
//
// CoreSecretBroker is safe for concurrent use.
type CoreSecretBroker struct {
	contract  HTTPContract
	baseURL   *url.URL
	transport ControlTransport
	problems  controlProblemResolver
	issue     SecretEndpointContract
	redeem    SecretEndpointContract
	grantTTL  int
	redeemTTL int
	spentMu   sync.Mutex
	spent     map[string]struct{}
	spentCap  int
}

// NewCoreSecretBroker builds the plugin-side secret-grant client.
//
// It fails closed on anything that could not produce a safe call: a base URL that
// is not a bare HTTPS origin, a missing transport, a contract that no longer
// registers both secret-grant routes with a method, a media type, a byte limit
// and a required field, a grant Core would accept more than once, an identifier
// limit the contract declares as absent, a path template that would not keep a
// handle in one path position, a replay-tracking bound that would leave the
// one-use guard unable to remember a handle, and a call family the contract
// describes no problem for.
func NewCoreSecretBroker(contract HTTPContract, baseURL string, transport ControlTransport, maximumTrackedRedemptions int) (*CoreSecretBroker, error) {
	parsed, valid := parseControlURL(baseURL)
	if !valid || transport == nil || maximumTrackedRedemptions <= 0 {
		return nil, ErrInvalidControlCall
	}
	grant := contract.Core.SecretGrant
	if controlSecretEndpointIncomplete(grant.Issue) || controlSecretEndpointIncomplete(grant.Redemption) {
		return nil, ErrInvalidControlCall
	}
	if grant.HandleMaximumLength <= 0 || grant.ReferenceMaximumLength <= 0 || grant.PurposeMaximumLength <= 0 {
		return nil, ErrInvalidControlCall
	}
	if grant.RedemptionUseLimit != 1 {
		// A grant Core would honour more than once cannot be guarded here, so a
		// contract that loosened the use limit is refused rather than served.
		return nil, ErrInvalidControlCall
	}
	if contract.Deadlines.CoreSecretGrantSeconds <= 0 || contract.Deadlines.CoreSecretRedemptionSeconds <= 0 {
		return nil, ErrInvalidControlCall
	}
	if err := requireControlMediaTypes([]string{
		grant.Issue.RequestMediaType,
		grant.Issue.ResponseMediaType,
		grant.Redemption.RequestMediaType,
		grant.Redemption.ResponseMediaType,
	}); err != nil {
		return nil, err
	}
	if !controlSecretTemplateUsable(grant.Issue.PathTemplate) ||
		!controlSecretTemplateUsable(grant.Redemption.PathTemplate, controlSecretHandleSegment) {
		return nil, ErrInvalidControlCall
	}
	problems, err := newControlProblemResolver(contract, controlCallSecret)
	if err != nil {
		return nil, err
	}
	return &CoreSecretBroker{
		contract:  contract,
		baseURL:   parsed,
		transport: transport,
		problems:  problems,
		issue:     grant.Issue,
		redeem:    grant.Redemption,
		grantTTL:  contract.Deadlines.CoreSecretGrantSeconds,
		redeemTTL: contract.Deadlines.CoreSecretRedemptionSeconds,
		spent:     make(map[string]struct{}, maximumTrackedRedemptions),
		spentCap:  maximumTrackedRedemptions,
	}, nil
}

// ClassifyOutcome implements the optional adapter port of the application layer.
// It reports only what a secret call actually saw, only for the plugin-to-Core
// secret family, and an empty outcome whenever the failure was not attributable to
// a published contract problem.
func (broker *CoreSecretBroker) ClassifyOutcome(err error) models.Outcome {
	if broker == nil {
		return ""
	}
	return classifyControlOutcome(err, controlCallSecret)
}

// CloseIdleConnections releases pooled control connections when the transport
// supports it, so a rotated replica credential is not served over an already
// established connection. A transport that pools nothing is not an error.
func (broker *CoreSecretBroker) CloseIdleConnections() {
	if broker == nil {
		return
	}
	if closer, ok := broker.transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// IssueGrant asks Core for a one-use grant bound to this replica, the generation
// the request names and one declared purpose.
//
// The request is checked against the domain syntax and against the contract's own
// identifier limits and request size before anything is sent, so an unusable
// request is refused locally instead of consuming a grant on Core. A published
// grant is returned exactly as Core described it, so the application layer can
// compare its reference, purpose and generation against what it asked for.
//
// A refused call is attributed to the contract outcome Core published, so the
// caller learns a denied grant from a denied grant and not from a generic
// transport failure.
func (broker *CoreSecretBroker) IssueGrant(ctx context.Context, request models.SecretGrantRequest) (models.SecretGrant, error) {
	if broker == nil {
		return models.SecretGrant{}, controlUnusable(controlCallSecret, ErrInvalidControlCall)
	}
	if err := broker.requestUsable(request); err != nil {
		return models.SecretGrant{}, err
	}
	document := broker.issue
	target, err := controlEndpointURL(broker.baseURL, document.PathTemplate)
	if err != nil {
		return models.SecretGrant{}, controlUnusable(controlCallSecret, err)
	}
	response, err := broker.call(ctx, broker.grantTTL, target, document, request)
	if err != nil {
		return models.SecretGrant{}, err
	}
	defer closeResource(response.Body)
	if !controlIsSuccess(response.StatusCode) {
		return models.SecretGrant{}, broker.refusal(response, document.MaximumResponseBytes)
	}
	body, err := broker.document(response, DocumentContract{
		MediaType:    document.ResponseMediaType,
		MaximumBytes: document.MaximumResponseBytes,
	})
	if err != nil {
		return models.SecretGrant{}, err
	}
	var published models.SecretGrant
	if err := decodeControlDocument(body, &published); err != nil {
		return models.SecretGrant{}, controlUnusable(controlCallSecret, err)
	}
	if err := broker.grantUsable(published, request); err != nil {
		return models.SecretGrant{}, err
	}
	return published, nil
}

// Redeem exchanges one grant handle for the value Core published.
//
// The handle is claimed before it is offered, because a one-use grant cannot be
// recovered from an unknown outcome: a timeout, a lost peer and a successful
// redemption are indistinguishable afterwards, and a second request would either
// be refused by Core or, worse, consumed by it. The same reasoning refuses a
// replay locally without a call at all.
//
// The value is returned as the exact document Core published, bounded by the
// contract response limit and typed by the contract media type. The contract
// registers no member for the redemption document, so the SDK names none: it hands
// the bytes on without interpreting them, and the plugin that asked for the secret
// owns what they mean.
//
// The transport buffer is cleared on every path, including the successful one,
// because the value handed to the caller is a private copy.
func (broker *CoreSecretBroker) Redeem(ctx context.Context, redemption models.SecretRedemption) (models.SecretValue, error) {
	if broker == nil {
		return models.SecretValue{}, controlUnusable(controlCallSecret, ErrInvalidControlCall)
	}
	if err := broker.redemptionUsable(redemption); err != nil {
		return models.SecretValue{}, err
	}
	if err := broker.claimHandle(redemption.Handle); err != nil {
		return models.SecretValue{}, err
	}
	document := broker.redeem
	target, err := broker.contract.ControlURL(broker.baseURL, document.PathTemplate,
		redemption.Handle, controlSecretHandleSegment)
	if err != nil {
		return models.SecretValue{}, controlUnusable(controlCallSecret, err)
	}
	response, err := broker.call(ctx, broker.redeemTTL, target, document, redemption)
	if err != nil {
		return models.SecretValue{}, err
	}
	defer closeResource(response.Body)
	if !controlIsSuccess(response.StatusCode) {
		// A spent or expired grant is an answer, not a lost peer. The bytes that
		// came with it are a problem document, never a value, and are dropped.
		return models.SecretValue{}, broker.refusal(response, document.MaximumResponseBytes)
	}
	body, err := broker.document(response, DocumentContract{
		MediaType:    document.ResponseMediaType,
		MaximumBytes: document.MaximumResponseBytes,
	})
	if err != nil {
		return models.SecretValue{}, err
	}
	defer controlZeroBytes(body)
	if len(body) == 0 {
		return models.SecretValue{}, controlUnusable(controlCallSecret, ErrControlDocument)
	}
	return models.NewSecretValue(body), nil
}

// claimHandle records a handle as offered exactly once. A handle already in the
// set is a replay, and a set at its bound is a broker that can no longer prove a
// later handle is unused: both are refused before anything is sent.
func (broker *CoreSecretBroker) claimHandle(handle string) error {
	broker.spentMu.Lock()
	defer broker.spentMu.Unlock()
	if _, replayed := broker.spent[handle]; replayed {
		return controlFailure(controlCallSecret, models.OutcomeGrantSpent, ErrSecretGrantSpent)
	}
	if len(broker.spent) >= broker.spentCap {
		return controlUnusable(controlCallSecret, ErrSecretGrantTrackingFull)
	}
	broker.spent[handle] = struct{}{}
	return nil
}

// requestUsable applies the domain syntax and the contract's own identifier
// limits to one grant request. Both are required: the domain rejects what is not
// a well-formed identifier, and the contract rejects what it would not bind.
func (broker *CoreSecretBroker) requestUsable(request models.SecretGrantRequest) error {
	grant := broker.contract.Core.SecretGrant
	if request.Validate() != nil ||
		len(request.Reference) > grant.ReferenceMaximumLength ||
		len(request.Purpose) > grant.PurposeMaximumLength {
		return controlUnusable(controlCallSecret, models.ErrInvalidSecretGrant)
	}
	return nil
}

// redemptionUsable applies the domain syntax and the contract's own handle limit
// to one redemption. The domain limit is looser than the contract's, so the
// contract limit is checked here rather than assumed to be implied.
func (broker *CoreSecretBroker) redemptionUsable(redemption models.SecretRedemption) error {
	if redemption.Validate() != nil ||
		len(redemption.Handle) > broker.contract.Core.SecretGrant.HandleMaximumLength ||
		!controlSecretHandleSingleSegment(redemption.Handle) {
		return controlUnusable(controlCallSecret, models.ErrInvalidSecretGrant)
	}
	return nil
}

// controlSecretHandleSingleSegment reports whether a handle can occupy exactly one
// path position in the contract's redemption template.
//
// The handle is an opaque bearer value, so neither the domain nor the contract
// constrains its characters. HTTPContract.ControlURL percent-encodes the segment
// and then rebuilds the target from the decoded path, so an encoding-sensitive
// character is decoded straight back out: a "/" would be sent as a real path
// separator and a dot segment would traverse out of the grants path. Both would
// put a one-use grant handle somewhere other than the one position the template
// names. A handle that cannot be confined to one segment is therefore refused
// here rather than escaped and trusted, and it is refused before the wire, so no
// request is ever sent for it.
//
// Only the characters that can change path structure are refused. A space, a
// question mark, a hash or a percent sign are re-escaped correctly when the target
// is serialised, so a handle may still carry them.
func controlSecretHandleSingleSegment(handle string) bool {
	if handle == "" || handle == "." || handle == ".." {
		return false
	}
	// A backslash is a path separator for intermediaries that normalise it, even
	// though a URL path does not treat it as one.
	return !strings.ContainsAny(handle, `/\`)
}

// grantUsable checks a published grant against the domain syntax, the contract's
// handle limit, and the request it answers. Core binds a grant to what was asked
// for, so a grant that names another reference, purpose or generation is not the
// grant this replica requested and is refused instead of being handed to a caller
// that would use it for the wrong thing.
func (broker *CoreSecretBroker) grantUsable(published models.SecretGrant, request models.SecretGrantRequest) error {
	if published.Validate() != nil ||
		len(published.Handle) > broker.contract.Core.SecretGrant.HandleMaximumLength ||
		published.Reference != request.Reference ||
		published.Purpose != request.Purpose ||
		published.Generation != request.Generation {
		return controlUnusable(controlCallSecret, models.ErrInvalidSecretGrant)
	}
	return nil
}

// call performs exactly one plugin-to-Core secret request against an already
// resolved target. It renders the request under the contract's own media type,
// byte limit and required fields, bounds the call with the deadline of its own
// operation, and hands the request to the shared transport. It never retries and
// never follows a redirect, so a handle stays on exactly one origin and is
// offered at most once.
func (broker *CoreSecretBroker) call(ctx context.Context, seconds int, target *url.URL, document SecretEndpointContract, payload any) (*http.Response, error) {
	contents, err := broker.render(document, payload)
	if err != nil {
		return nil, err
	}
	callCtx, cancel, err := controlCallContext(ctx, seconds)
	if err != nil {
		return nil, controlUnusable(controlCallSecret, err)
	}
	defer cancel()
	request, err := http.NewRequestWithContext(callCtx, document.Method, target.String(), bytes.NewReader(contents))
	if err != nil {
		return nil, controlUnusable(controlCallSecret, ErrControlTransportFailed)
	}
	request.Header.Set("content-type", document.RequestMediaType)
	request.Header.Set("accept", document.ResponseMediaType)
	return performControlCall(broker.transport, request, controlCallSecret)
}

// render produces one request document and applies the contract's request rules:
// the exact media type, the byte limit, and every field the contract marks
// required. An oversized or incomplete request is never sent, so the caller learns
// its own input was unusable rather than discovering it from Core.
func (broker *CoreSecretBroker) render(document SecretEndpointContract, payload any) ([]byte, error) {
	if _, err := controlMediaType(document.RequestMediaType); err != nil {
		return nil, controlUnusable(controlCallSecret, err)
	}
	contents, err := json.Marshal(payload)
	if err != nil {
		return nil, controlUnusable(controlCallSecret, ErrControlRequestOversized)
	}
	if err := requireControlRequestSize(contents, document.MaximumRequestBytes); err != nil {
		return nil, controlUnusable(controlCallSecret, err)
	}
	if err := requireControlFields(contents, document.Required); err != nil {
		return nil, controlUnusable(controlCallSecret, err)
	}
	return contents, nil
}

// document applies the contract's rules to one successful response: the exact
// media type and the byte limit. The contract registers no required member for a
// secret response, so none is invented here, and the bytes are returned exactly as
// published.
func (broker *CoreSecretBroker) document(response *http.Response, document DocumentContract) ([]byte, error) {
	expected, err := controlMediaType(document.MediaType)
	if err != nil {
		return nil, controlUnusable(controlCallSecret, err)
	}
	if err := requireControlMediaType(response, expected); err != nil {
		return nil, controlUnusable(controlCallSecret, err)
	}
	contents, err := readControlBody(response.Body, document.MaximumBytes)
	if err != nil {
		return nil, controlBodyFailure(controlCallSecret, err)
	}
	return contents, nil
}

// refusal attributes a non-success answer to the problem Core published. A body the
// SDK cannot read is not a licence to guess: the call stays unattributed and the
// caller's own transport classification decides. A spent grant keeps its own
// sentinel so a caller can recognise a replay without reading a private type.
func (broker *CoreSecretBroker) refusal(response *http.Response, maximum int64) error {
	contents, err := readControlBody(response.Body, maximum)
	if err != nil {
		return controlUnusable(controlCallSecret, ErrControlRequestRefused)
	}
	outcome, attributed := broker.problems.resolve(response.StatusCode, decodeControlProblem(contents))
	if !attributed {
		return controlUnusable(controlCallSecret, ErrControlRequestRefused)
	}
	if outcome == models.OutcomeGrantSpent {
		return controlFailure(controlCallSecret, outcome, ErrSecretGrantSpent)
	}
	return controlFailure(controlCallSecret, outcome, ErrControlRequestRefused)
}

// controlSecretEndpointIncomplete requires one secret route to carry everything a
// safe call needs: a method, both media types, both byte limits and at least one
// required field, so a request can never be sent without the member Core needs.
func controlSecretEndpointIncomplete(endpoint SecretEndpointContract) bool {
	return endpoint.Method == "" || endpoint.PathTemplate == "" ||
		endpoint.RequestMediaType == "" || endpoint.ResponseMediaType == "" ||
		endpoint.MaximumRequestBytes <= 0 || endpoint.MaximumResponseBytes <= 0 ||
		len(endpoint.Required) == 0
}

// controlSecretTemplateUsable requires a contract path template to be a rooted path
// that expands exactly the named segment and no other, so a handle can only ever
// occupy one path position and can never escape it.
func controlSecretTemplateUsable(template string, segments ...string) bool {
	if !strings.HasPrefix(template, "/") {
		return false
	}
	for _, segment := range segments {
		placeholder := "{" + segment + "}"
		if strings.Count(template, placeholder) != 1 {
			return false
		}
	}
	return true
}
