package presentation

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/Liapoldus/plugin-sdk/domain/models"
)

// Endpoint is one route of the versioned contract: the method Core must use and
// the path it must use it on. The value is injected, so no route string is ever
// spelled in this layer and no second copy of a path can exist.
type Endpoint struct {
	Method string
	Path   string
}

// DocumentContract is one document the contract describes: the media type it is
// served as, the largest number of bytes it may occupy and the fields it must
// carry. A document whose fields this layer owns is checked against the fields of
// the model it serialises, so the two can never drift apart.
type DocumentContract struct {
	MediaType       string
	MaximumBytes    int64
	Required        []string
	DigestAlgorithm string
}

// ContentTypes are the two media types the plugin REST surface answers with.
type ContentTypes struct {
	JSON    string
	Metrics string
}

// ArtifactStreamContract defines the generic multipart envelope the SDK reads.
// Product metadata and artifact contents remain opaque to this transport.
type ArtifactStreamContract struct {
	MediaType                     string
	MetadataMediaType             string
	Parts                         []string
	PartOrder                     []string
	MaximumArtifactBytes          int64
	MinimumArtifactBytes          int64
	MaximumMetadataBytes          int64
	MaximumMultipartOverheadBytes int64
	MaximumRequestBytes           int64
	MaximumReceiptBytes           int64
	AcceptedStatus                int
	FilenameForwarded             bool
	Deadline                      time.Duration
	InvocationContext             ArtifactInvocationContract
}

type ArtifactInvocationContract struct {
	MaximumBytes int
	Required     []string
	Optional     []string
	Headers      map[string]string
}

type AdminActionContract struct {
	MediaType            string
	MaximumRequestBytes  int64
	MaximumResponseBytes int64
	MaximumPageIDBytes   int
	MaximumActionIDBytes int
	PathSegmentPattern   string
	ResponseStatus       StatusRangeContract
	Deadline             time.Duration
	InvocationContext    AdminInvocationContract
}

type StatusRangeContract struct {
	Minimum int
	Maximum int
}

type AdminInvocationContract struct {
	MaximumBytes        int
	UnknownHeaderPrefix string
	Required            []string
	Optional            []string
	Headers             map[string]string
}

// Problem is the public-safe status and code of one contract refusal. It carries
// no cause, path, address, document or secret, so it can be written straight to
// a response or a log line.
type Problem struct {
	Status int
	Code   string
}

// Contracts is the injected view of the versioned HTTP contract that the handler
// layer needs. It is a value, not a source: a plugin author's composition root
// obtains it from the contract loader of the infrastructure layer and passes it
// in, and this package never parses contract bytes, embeds an asset or names a
// status, code, path, media type or limit of its own.
//
// Every field mirrors a part of the contract asset. The four documents this layer
// serialises from a domain model additionally carry the required field list, and
// Validate refuses any combination in which that list is not exactly the field
// set of the model.
type Contracts struct {
	// ContractVersion is the contract this handler set answers for. It is
	// published verbatim in the identity registration document.
	ContractVersion string

	IdentityEndpoint       Endpoint
	ManifestEndpoint       Endpoint
	ConfigSchemaEndpoint   Endpoint
	HealthEndpoint         Endpoint
	ReadyEndpoint          Endpoint
	ReloadEndpoint         Endpoint
	ArtifactStreamEndpoint Endpoint
	AdminSurfaceEndpoint   Endpoint
	AdminActionEndpoint    Endpoint
	MetricsEndpoint        Endpoint

	HealthStatus int
	HealthBody   map[string]string

	ContentTypes ContentTypes

	// ReloadRequest describes the notification Core sends to the only write
	// path, ReloadAcknowledgement the document that path returns, and Readiness
	// the document the readiness endpoint returns.
	ReloadRequest         DocumentContract
	ReloadAcknowledgement DocumentContract
	Readiness             DocumentContract
	// Manifest and ConfigurationSchema describe the two plugin-owned documents
	// this layer publishes verbatim. Their fields stay opaque: no required list
	// is honoured for them because the plugin, not the SDK, owns their shape.
	Manifest            DocumentContract
	ConfigurationSchema DocumentContract
	ArtifactStream      ArtifactStreamContract
	AdminSurface        DocumentContract
	AdminAction         AdminActionContract
	// Registration describes the identity document this replica publishes.
	Registration DocumentContract

	// MaximumMetadataBytes caps any document the plugin itself produces.
	MaximumMetadataBytes int64
	// ReadinessDeadline bounds one readiness read.
	ReadinessDeadline time.Duration

	// Problems holds the refusals the transport itself raises, keyed by the
	// logical names the contract registers them under.
	Problems map[string]Problem
	// Errors holds every contract problem key with the status and code it owns.
	Errors map[string]Problem
	// OutcomeProblems maps a contract outcome to the problem key that owns it,
	// and SuccessOutcomes lists the outcomes that acknowledge success.
	OutcomeProblems map[string]string
	SuccessOutcomes []string
}

// registeredEndpoints lists every endpoint this layer serves, in a stable order
// so a conflicting-path check is deterministic.
func (contracts Contracts) registeredEndpoints() []Endpoint {
	return []Endpoint{
		contracts.IdentityEndpoint,
		contracts.ManifestEndpoint,
		contracts.ConfigSchemaEndpoint,
		contracts.HealthEndpoint,
		contracts.ReadyEndpoint,
		contracts.ReloadEndpoint,
		contracts.ArtifactStreamEndpoint,
		contracts.AdminSurfaceEndpoint,
		contracts.AdminActionEndpoint,
		contracts.MetricsEndpoint,
	}
}

// problem returns the status and code registered under one error key.
func (contracts Contracts) problem(key string) (Problem, bool) {
	problem, ok := contracts.Errors[key]
	return problem, ok
}

// transportProblem returns the status and code registered under one of the five
// transport refusal keys.
func (contracts Contracts) transportProblem(key string) (Problem, bool) {
	problem, ok := contracts.Problems[key]
	return problem, ok
}

// isSuccessOutcome reports whether an outcome acknowledges success. The
// vocabulary is closed in the contract, so a string invented anywhere else can
// never be classified as a success.
func (contracts Contracts) isSuccessOutcome(outcome models.Outcome) bool {
	for _, candidate := range contracts.SuccessOutcomes {
		if candidate != "" && candidate == string(outcome) {
			return true
		}
	}
	return false
}

// outcomeProblem resolves one contract outcome to the status and code that own
// it. A success outcome has no problem and reports false.
//
// Every other outcome is resolved through the contract's own maps. The contract
// itself maps some outcomes to a problem key it registers no entry for, so an
// outcome with no specific entry degrades to the contract's own internalError
// problem rather than to a status or code invented here. ok is false only for a
// success outcome, or if even the internalError problem is absent, which
// Validate rules out.
func (contracts Contracts) outcomeProblem(outcome models.Outcome) (Problem, bool) {
	if contracts.isSuccessOutcome(outcome) {
		return Problem{}, false
	}
	if outcome.Valid() {
		if problem, ok := contracts.problem(contracts.OutcomeProblems[string(outcome)]); ok {
			return problem, true
		}
	}
	return contracts.transportProblem(internalErrorKey)
}

// Validate reports whether the injected contract can produce a conformant
// handler set. It refuses a missing or malformed route, a missing media type, a
// non-positive limit, an absent readiness deadline, an unusable problem entry and
// a required field list that is not exactly the field set of the model this
// layer serialises for that document.
//
// It never invents a value, so a wiring mistake fails closed at composition time
// instead of becoming a wrong status or a missing field on the wire.
func (contracts Contracts) Validate() error {
	if contracts.ContractVersion == "" || contracts.ReadinessDeadline <= 0 ||
		contracts.MaximumMetadataBytes <= 0 {
		return ErrInvalidContracts
	}
	seen := make(map[string]struct{}, 10)
	for _, endpoint := range contracts.registeredEndpoints() {
		if !validEndpoint(endpoint) {
			return ErrInvalidContracts
		}
		if _, repeated := seen[endpoint.Path]; repeated {
			return ErrInvalidContracts
		}
		seen[endpoint.Path] = struct{}{}
	}
	artifact := contracts.ArtifactStream
	if artifact.MediaType == "" || artifact.MetadataMediaType == "" ||
		artifact.MaximumArtifactBytes <= 0 || artifact.MinimumArtifactBytes <= 0 ||
		artifact.MinimumArtifactBytes > artifact.MaximumArtifactBytes ||
		artifact.MaximumMetadataBytes <= 0 || artifact.MaximumMultipartOverheadBytes <= 0 ||
		artifact.MaximumReceiptBytes <= 0 || artifact.AcceptedStatus < 200 || artifact.AcceptedStatus >= 300 ||
		artifact.Deadline <= 0 ||
		artifact.FilenameForwarded || len(artifact.Parts) != 2 || len(artifact.PartOrder) != 2 ||
		artifact.Parts[0] == "" || artifact.Parts[1] == "" || artifact.Parts[0] == artifact.Parts[1] ||
		artifact.PartOrder[0] != artifact.Parts[0] || artifact.PartOrder[1] != artifact.Parts[1] ||
		artifact.MaximumRequestBytes != artifact.MaximumArtifactBytes+artifact.MaximumMetadataBytes+artifact.MaximumMultipartOverheadBytes ||
		artifact.InvocationContext.MaximumBytes <= 0 || len(artifact.InvocationContext.Required) == 0 ||
		len(artifact.InvocationContext.Headers) != len(artifact.InvocationContext.Required)+len(artifact.InvocationContext.Optional) {
		return ErrInvalidContracts
	}
	if contracts.HealthStatus < 100 || contracts.HealthStatus > 599 || len(contracts.HealthBody) == 0 {
		return ErrInvalidContracts
	}
	for field, value := range contracts.HealthBody {
		if strings.TrimSpace(field) == "" || value == "" {
			return ErrInvalidContracts
		}
	}
	if contracts.ContentTypes.JSON == "" || contracts.ContentTypes.Metrics == "" {
		return ErrInvalidContracts
	}
	// The four documents this layer serialises from a domain model must publish
	// exactly the fields that model serialises. A contract that renames, adds or
	// drops one of them is refused instead of producing a partial response.
	if err := contracts.ReloadRequest.checkModel(models.Reload{}); err != nil {
		return err
	}
	if err := contracts.ReloadAcknowledgement.checkModel(models.ReloadAcknowledgement{}); err != nil {
		return err
	}
	if err := contracts.Readiness.checkModel(models.Readiness{}); err != nil {
		return err
	}
	if err := contracts.Registration.checkModel(models.Registration{}); err != nil {
		return err
	}
	// The two plugin-owned metadata documents stay opaque, so only their own
	// declared limits are checked. The SDK never requires a field of a document
	// it does not own.
	if err := contracts.Manifest.check(); err != nil {
		return err
	}
	if err := contracts.ConfigurationSchema.check(); err != nil {
		return err
	}
	if err := contracts.AdminSurface.check(); err != nil {
		return fmt.Errorf("%w: admin surface document", err)
	}
	if contracts.AdminSurface.DigestAlgorithm == "" {
		return fmt.Errorf("%w: admin surface digest algorithm", ErrInvalidContracts)
	}
	if !strings.Contains(contracts.AdminActionEndpoint.Path, "{page}") ||
		!strings.Contains(contracts.AdminActionEndpoint.Path, "{action}") ||
		contracts.AdminActionEndpoint.Method == "" {
		return ErrInvalidContracts
	}
	admin := contracts.AdminAction
	if admin.MediaType == "" || admin.MaximumRequestBytes <= 0 || admin.MaximumResponseBytes <= 0 ||
		admin.MaximumPageIDBytes <= 0 || admin.MaximumActionIDBytes <= 0 || admin.PathSegmentPattern == "" ||
		admin.Deadline <= 0 || admin.ResponseStatus.Minimum < 200 || admin.ResponseStatus.Maximum > 599 ||
		admin.ResponseStatus.Minimum > admin.ResponseStatus.Maximum || admin.InvocationContext.MaximumBytes <= 0 ||
		admin.InvocationContext.UnknownHeaderPrefix == "" || !validAdminInvocation(admin.InvocationContext) {
		return ErrInvalidContracts
	}
	if _, err := regexp.Compile(admin.PathSegmentPattern); err != nil {
		return ErrInvalidContracts
	}
	for _, key := range transportProblemKeys {
		if _, ok := contracts.transportProblem(key); !ok {
			return ErrInvalidContracts
		}
	}
	for _, key := range errorProblemKeys {
		problem, ok := contracts.problem(key)
		if !ok || problem.Status < 100 || problem.Status > 599 || problem.Code == "" {
			return ErrInvalidContracts
		}
	}
	if len(contracts.SuccessOutcomes) == 0 {
		return ErrInvalidContracts
	}
	for _, outcome := range contracts.SuccessOutcomes {
		if outcome == "" || !models.Outcome(outcome).Valid() {
			return ErrInvalidContracts
		}
	}
	// Every published outcome mapping must name an outcome of the closed
	// vocabulary that is not itself a success. Whether a specific status exists
	// for it is decided at request time, because the contract publishes outcome
	// keys that carry no entry of their own.
	for outcome := range contracts.OutcomeProblems {
		candidate := models.Outcome(outcome)
		if !candidate.Valid() || contracts.isSuccessOutcome(candidate) {
			return ErrInvalidContracts
		}
	}
	return nil
}

func validEndpoint(endpoint Endpoint) bool {
	if endpoint.Method == "" || endpoint.Path == "" ||
		!strings.HasPrefix(endpoint.Path, "/") || strings.HasSuffix(endpoint.Path, "/") {
		return false
	}
	if strings.ContainsAny(endpoint.Method+endpoint.Path, " \t\r\n?#") {
		return false
	}
	for _, character := range endpoint.Method + endpoint.Path {
		if character < ' ' || character == 0x7f {
			return false
		}
	}
	return true
}

func validAdminInvocation(invocation AdminInvocationContract) bool {
	required := []string{"callerId", "instanceId", "pageId", "actionId", "surfaceDigest", "requestId"}
	optional := []string{"idempotencyKey", "ifMatch"}
	if len(invocation.Headers) != len(required)+len(optional) || !sameSet(invocation.Required, required) || !sameSet(invocation.Optional, optional) {
		return false
	}
	seen := map[string]struct{}{}
	for _, key := range append(append([]string{}, required...), optional...) {
		name := invocation.Headers[key]
		if name == "" || strings.ContainsAny(name, "\r\n :") {
			return false
		}
		folded := strings.ToLower(name)
		if _, ok := seen[folded]; ok {
			return false
		}
		seen[folded] = struct{}{}
	}
	return true
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, item := range a {
		set[item] = struct{}{}
	}
	for _, item := range b {
		if _, ok := set[item]; !ok {
			return false
		}
		delete(set, item)
	}
	return len(set) == 0
}

// check validates the limits of a document this layer serves but does not own.
func (document DocumentContract) check() error {
	if document.MediaType == "" || document.MaximumBytes <= 0 {
		return ErrInvalidContracts
	}
	return nil
}

// checkModel validates a document this layer serialises from a domain model and
// proves that the required field list is exactly the model field set. The
// comparison is derived from the injected list and the model's own JSON tags, so
// a response can neither omit a required field nor add an unpublished one.
func (document DocumentContract) checkModel(sample any) error {
	if err := document.check(); err != nil {
		return err
	}
	fields, err := jsonFieldNames(sample)
	if err != nil || len(document.Required) == 0 {
		return ErrInvalidContracts
	}
	required := make(map[string]struct{}, len(document.Required))
	for _, field := range document.Required {
		if field == "" {
			return ErrInvalidContracts
		}
		if _, repeated := required[field]; repeated {
			return ErrInvalidContracts
		}
		required[field] = struct{}{}
	}
	if len(required) != len(fields) {
		return ErrInvalidContracts
	}
	for _, field := range fields {
		if _, present := required[field]; !present {
			return ErrInvalidContracts
		}
	}
	return nil
}

// jsonFieldNames lists the wire field names of one domain model. A field this
// layer would not serialise is refused rather than skipped, because a required
// list that silently omits a model field would let the contract and the response
// disagree.
func jsonFieldNames(sample any) ([]string, error) {
	value := reflect.ValueOf(sample)
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil, ErrInvalidContracts
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return nil, ErrInvalidContracts
	}
	model := value.Type()
	fields := make([]string, 0, model.NumField())
	for index := range model.NumField() {
		field := model.Field(index)
		if !field.IsExported() {
			return nil, ErrInvalidContracts
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			return nil, ErrInvalidContracts
		}
		fields = append(fields, name)
	}
	return fields, nil
}
