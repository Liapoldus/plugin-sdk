package application

import (
	"context"
	"errors"
	"sync"

	"github.com/Liapoldus/plugin-sdk/domain/interfaces"
	"github.com/Liapoldus/plugin-sdk/domain/models"
)

// ErrMissingLifecycleDependency is returned by NewLifecycle when a required port
// or the lifecycle observer is absent. It names no port, endpoint, address,
// document or configuration value.
var ErrMissingLifecycleDependency = errors.New("plugin lifecycle dependency is required")

// LifecycleConfiguration carries the injected ports and the per-replica
// identity of one manually started plugin process. It is product agnostic: no
// field describes a setting, a capability or a public route.
type LifecycleConfiguration struct {
	// Source reads one exact immutable generation from Core over the private
	// mutual-TLS control API.
	Source interfaces.ConfigurationSource
	// Applier is the plugin-owned validator and atomic applier. The SDK never
	// inspects a product field and never applies a document itself.
	Applier interfaces.ConfigurationApplier
	// Identity is the unique per-replica identity this process publishes.
	Identity models.ReplicaIdentity
	// Observer receives exactly one bounded lifecycle event per Reload outcome.
	// When it also implements ReadinessObserver or PullFailureReporter the SDK
	// keeps those signals in step with the applied generation automatically.
	Observer interfaces.LifecycleObserver
}

// Lifecycle is the generic Core-to-plugin Reload use case of one plugin
// replica: pull the exact immutable generation, verify it, hand it to the
// plugin-owned applier and acknowledge only what was actually activated.
//
// The SDK holds no configuration document after an apply. It retains only the
// descriptor of the generation it applied so that a repeat notification is
// answered idempotently and a contradiction is refused without a pull.
//
// Lifecycle is safe for concurrent use. Reloads are serialized so that exactly
// one document is being applied at a time, while readiness and registration
// stay answerable during an apply.
type Lifecycle struct {
	source     interfaces.ConfigurationSource
	applier    interfaces.ConfigurationApplier
	observer   interfaces.LifecycleObserver
	readiness  ReadinessObserver
	pulls      PullFailureReporter
	classifier OutcomeClassifier
	identity   models.ReplicaIdentity

	applyMu  sync.Mutex
	stateMu  sync.RWMutex
	active   *models.Reload
	pending  string
	applying *models.Reload
}

// NewLifecycle builds the Reload use case from the injected ports. It fails
// closed when a port, the replica identity or the observer is missing, because
// a lifecycle that cannot report its own outcomes is not safe to expose.
func NewLifecycle(configuration LifecycleConfiguration) (*Lifecycle, error) {
	if configuration.Source == nil || configuration.Applier == nil || configuration.Observer == nil {
		return nil, ErrMissingLifecycleDependency
	}
	if !configuration.Identity.Valid() {
		return nil, models.ErrInvalidIdentity
	}
	lifecycle := &Lifecycle{
		source:   configuration.Source,
		applier:  configuration.Applier,
		observer: configuration.Observer,
		identity: configuration.Identity,
	}
	lifecycle.classifier, _ = configuration.Source.(OutcomeClassifier)
	lifecycle.readiness, _ = configuration.Observer.(ReadinessObserver)
	lifecycle.pulls, _ = configuration.Observer.(PullFailureReporter)
	return lifecycle, nil
}

// Reload activates exactly the generation Core announced. It returns a
// non-success acknowledgement carrying the contract outcome for every refusal,
// and a public-safe error that carries the same outcome and no cause, address,
// document or transport detail.
//
// The policy is, in order: reject a request that violates the descriptor
// syntax; answer a byte-for-byte repeat of the active descriptor without
// pulling again; refuse a descriptor that contradicts the active generation
// without pulling; pull the exact generation; refuse a generation Core no
// longer desires; verify generation, digest and schema version against both
// the exact bytes and the announcement; and only then call the plugin applier.
//
// The SDK never retries or replays a pull or an apply of its own accord, never
// applies a document whose digest was not verified, and never replaces the
// previously active configuration because of a refusal.
func (lifecycle *Lifecycle) Reload(ctx context.Context, request models.Reload) (models.ReloadAcknowledgement, error) {
	if err := request.Validate(); err != nil {
		lifecycle.observe(ctx, KindReload, models.OutcomeInvalidRequest)
		return acknowledgement(models.Reload{}, false, models.OutcomeInvalidRequest),
			refusal(models.OutcomeInvalidRequest, models.ErrInvalidReload)
	}

	lifecycle.applyMu.Lock()
	defer lifecycle.applyMu.Unlock()

	if settled, outcome, answered := lifecycle.settled(request); answered {
		if outcome == models.OutcomeAlreadyActive {
			if lifecycle.readiness != nil {
				lifecycle.readiness.SetReady(true)
			}
			lifecycle.observe(ctx, KindReload, outcome)
			return acknowledgement(settled, true, outcome), nil
		}
		return lifecycle.refuse(ctx, request, outcome, models.ErrInvalidConfiguration)
	}

	if outcome, expired := contextOutcome(ctx); expired {
		return lifecycle.refusePull(ctx, request, outcome, nil)
	}

	result, err := lifecycle.source.PullExact(ctx, request.Generation)
	if err != nil {
		return lifecycle.refusePull(ctx, request, classifyFailure(err, lifecycle.classifier, permitsAdapterOutcome), nil)
	}
	if outcome, refused := pullStateOutcome(result.State); refused {
		return lifecycle.refusePull(ctx, request, outcome, nil)
	}

	configuration := result.Configuration
	if outcome, reason := verifyDocument(configuration, request); outcome.Valid() {
		return lifecycle.refusePull(ctx, request, outcome, reason)
	}

	exact, err := models.NewConfiguration(
		configuration.Generation, configuration.SchemaVersion, configuration.SHA256, configuration.Bytes())
	if err != nil {
		return lifecycle.refusePull(ctx, request, configurationOutcome(err), models.ErrInvalidConfiguration)
	}

	lifecycle.stateMu.Lock()
	applying := request
	lifecycle.applying = &applying
	lifecycle.stateMu.Unlock()
	applyErr := lifecycle.applier.Apply(ctx, exact)
	lifecycle.stateMu.Lock()
	lifecycle.applying = nil
	lifecycle.stateMu.Unlock()
	if applyErr != nil {
		// The applier contract guarantees the previously active configuration
		// stays fully usable. The SDK cannot know how far the rejected document
		// reached the runtime, so it reports the refusal once and never replays
		// the apply; Core re-announces the generation and idempotency decides.
		return lifecycle.refuse(ctx, request, models.OutcomeApplyRejected, nil)
	}

	lifecycle.stateMu.Lock()
	lifecycle.active = &request
	lifecycle.pending = ""
	lifecycle.stateMu.Unlock()

	lifecycle.observe(ctx, KindReload, models.OutcomeApplied)
	if lifecycle.readiness != nil {
		lifecycle.readiness.SetReady(true)
	}
	return acknowledgement(request, true, models.OutcomeApplied), nil
}

// SecretGrantGeneration returns the generation to which a plugin-owned secret
// request may be bound. While Reload is applying a candidate, that exact
// candidate is returned; otherwise the locally active generation is returned.
// The candidate is visible only for the duration of the serialized Apply call,
// so a caller cannot ask the SDK to mint a grant for an arbitrary generation.
func (lifecycle *Lifecycle) SecretGrantGeneration() (string, bool) {
	if lifecycle == nil {
		return "", false
	}
	lifecycle.stateMu.RLock()
	defer lifecycle.stateMu.RUnlock()
	if lifecycle.applying != nil {
		return lifecycle.applying.Generation, true
	}
	if lifecycle.active != nil {
		return lifecycle.active.Generation, true
	}
	return "", false
}

// ActiveGeneration returns the descriptor of the generation this replica has
// applied, and false while no generation has been acknowledged.
func (lifecycle *Lifecycle) ActiveGeneration() (models.Reload, bool) {
	lifecycle.stateMu.RLock()
	defer lifecycle.stateMu.RUnlock()
	if lifecycle.active == nil {
		return models.Reload{}, false
	}
	return *lifecycle.active, true
}

// OutcomeError is the public-safe error of a refused lifecycle operation. It
// carries the closed contract outcome and, where one applies, a domain
// sentinel. It never carries a cause, a path, an address, a document, a
// certificate or a secret.
type OutcomeError struct {
	// Outcome is the contract outcome reported to Core and to the operator.
	Outcome models.Outcome
	reason  error
}

// Error implements error with the outcome only.
func (failure *OutcomeError) Error() string {
	return "plugin lifecycle operation refused: " + string(failure.Outcome)
}

// Unwrap exposes the domain sentinel reason of the refusal, if any, so a caller
// can match it with errors.Is.
func (failure *OutcomeError) Unwrap() error {
	return failure.reason
}

// OutcomeOf reports the contract outcome carried by a refused operation error.
// It is how the presentation layer maps a refusal to a status and a code
// without parsing a message.
func OutcomeOf(err error) (models.Outcome, bool) {
	var failure *OutcomeError
	if errors.As(err, &failure) {
		return failure.Outcome, true
	}
	return "", false
}

func refusal(outcome models.Outcome, reason error) error {
	if !outcome.Valid() {
		outcome = models.OutcomeCoreUnavailable
	}
	return &OutcomeError{Outcome: outcome, reason: reason}
}

// settled reports the outcome of a notification that needs no pull at all: a
// byte-for-byte repeat of the active descriptor, or a descriptor that
// contradicts the active generation under the same identifier. It is the whole
// idempotency contract: a duplicate is acknowledged, a contradiction is refused.
func (lifecycle *Lifecycle) settled(request models.Reload) (models.Reload, models.Outcome, bool) {
	lifecycle.stateMu.Lock()
	defer lifecycle.stateMu.Unlock()
	if lifecycle.active == nil {
		return models.Reload{}, "", false
	}
	if lifecycle.active.SameDescriptor(request) {
		lifecycle.pending = ""
		return *lifecycle.active, models.OutcomeAlreadyActive, true
	}
	if lifecycle.active.Generation == request.Generation {
		return *lifecycle.active, models.OutcomeGenerationConflict, true
	}
	return models.Reload{}, "", false
}

func (lifecycle *Lifecycle) refuse(ctx context.Context, request models.Reload, outcome models.Outcome, reason error) (models.ReloadAcknowledgement, error) {
	lifecycle.stateMu.Lock()
	lifecycle.pending = request.Generation
	lifecycle.stateMu.Unlock()
	if lifecycle.readiness != nil {
		lifecycle.readiness.SetReady(false)
	}
	lifecycle.observe(ctx, KindReload, outcome)
	return acknowledgement(request, false, outcome), refusal(outcome, reason)
}

// refusePull is the refusal path of an exact-generation pull. The refused
// generation is also counted as a pull attempt that did not yield an applicable
// document, which is a different fact from the lifecycle outcome.
func (lifecycle *Lifecycle) refusePull(ctx context.Context, request models.Reload, outcome models.Outcome, reason error) (models.ReloadAcknowledgement, error) {
	if lifecycle.pulls != nil && countsAsPullFailure(outcome) {
		lifecycle.pulls.PullFailure(outcome)
	}
	return lifecycle.refuse(ctx, request, outcome, reason)
}

func (lifecycle *Lifecycle) observe(ctx context.Context, kind Kind, outcome models.Outcome) {
	if !outcome.Valid() {
		return
	}
	lifecycle.observer.Observe(ctx, string(kind), outcome)
}

// verifyDocument checks the exact pulled bytes against the document rules the
// SDK owns and against the descriptor Core announced in the notification. It
// returns the outcome of the first violated rule, or an empty outcome when the
// document may be applied.
func verifyDocument(configuration models.Configuration, request models.Reload) (models.Outcome, error) {
	if err := configuration.Validate(); err != nil && errors.Is(err, models.ErrInvalidDocument) {
		return models.OutcomeDocumentMalformed, models.ErrInvalidDocument
	}
	if configuration.Generation != request.Generation {
		return models.OutcomeUnknownGeneration, models.ErrInvalidConfiguration
	}
	if configuration.SHA256 != request.SHA256 || models.Digest(configuration.Bytes()) != configuration.SHA256 {
		return models.OutcomeDigestMismatch, models.ErrInvalidConfiguration
	}
	if configuration.SchemaVersion != request.SchemaVersion {
		return models.OutcomeSchemaVersionMismatch, models.ErrInvalidConfiguration
	}
	return "", nil
}

func configurationOutcome(err error) models.Outcome {
	if errors.Is(err, models.ErrInvalidDocument) {
		return models.OutcomeDocumentMalformed
	}
	return models.OutcomeDigestMismatch
}

// pullStateOutcome reports whether a pulled generation may be applied. Only a
// state Core marks as currently desired may be applied. A generation that is no
// longer desired is refused, and a state the SDK cannot interpret is treated as
// an unusable response instead of being guessed.
func pullStateOutcome(state interfaces.GenerationState) (models.Outcome, bool) {
	switch state {
	case interfaces.GenerationStateActive, "":
		return "", false
	case interfaces.GenerationStatePrevious:
		return models.OutcomeStaleGeneration, true
	default:
		return models.OutcomeCoreUnavailable, true
	}
}

func acknowledgement(descriptor models.Reload, applied bool, outcome models.Outcome) models.ReloadAcknowledgement {
	return models.ReloadAcknowledgement{
		Generation:    descriptor.Generation,
		SHA256:        descriptor.SHA256,
		SchemaVersion: descriptor.SchemaVersion,
		Applied:       applied,
		Outcome:       outcome,
	}
}

// OutcomeClassifier is the optional port a transport adapter implements to
// attribute a failed call to a typed contract outcome. It lets an adapter report
// what it actually saw on the wire instead of collapsing every failure into the
// SDK's own transport classification. An outcome outside the set the SDK permits
// for that call, or outside the closed vocabulary, is ignored.
type OutcomeClassifier interface {
	// ClassifyOutcome maps a failed call to a contract outcome. It must return
	// an empty outcome when it cannot attribute the failure.
	ClassifyOutcome(err error) models.Outcome
}

func classifyFailure(err error, classifier OutcomeClassifier, permitted func(models.Outcome) bool) models.Outcome {
	if classifier != nil {
		if outcome := classifier.ClassifyOutcome(err); outcome.Valid() && permitted(outcome) {
			return outcome
		}
	}
	return transportOutcome(err)
}

// transportOutcome is the SDK's own classification of a call that produced no
// usable result. A cancelled or expired context is always visible; any other
// cause is an opaque failure of the private control API and is never described.
func transportOutcome(err error) models.Outcome {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return models.OutcomeDeadlineExceeded
	case errors.Is(err, context.Canceled):
		return models.OutcomeCancelled
	default:
		return models.OutcomeCoreUnavailable
	}
}

// contextOutcome reports the outcome of a call whose context is already done.
// The SDK starts no control-plane work under a dead context, so Core is told the
// exact reason instead of a generic transport failure.
func contextOutcome(ctx context.Context) (models.Outcome, bool) {
	switch err := ctx.Err(); err {
	case nil:
		return "", false
	case context.DeadlineExceeded:
		return models.OutcomeDeadlineExceeded, true
	default:
		return models.OutcomeCancelled, true
	}
}

// permitsAdapterOutcome is the set of pull outcomes a transport adapter may
// claim. The outcomes the SDK decides from the pulled bytes itself, and the
// outcomes of a request that never reached the control API, stay with the SDK,
// so exactly one owner decides each outcome.
func permitsAdapterOutcome(outcome models.Outcome) bool {
	switch outcome {
	case models.OutcomeNotPermitted, models.OutcomeUnknownGeneration, models.OutcomeStaleGeneration,
		models.OutcomeDocumentOversized, models.OutcomeCoreUnavailable, models.OutcomeCancelled,
		models.OutcomeDeadlineExceeded:
		return true
	}
	return false
}

// countsAsPullFailure reports whether an exact-generation pull did not yield an
// applicable document. A refused pull is a different fact from the lifecycle
// outcome, and it is counted separately.
func countsAsPullFailure(outcome models.Outcome) bool {
	if permitsAdapterOutcome(outcome) {
		return true
	}
	switch outcome {
	case models.OutcomeDigestMismatch, models.OutcomeSchemaVersionMismatch, models.OutcomeDocumentMalformed:
		return true
	}
	return false
}
