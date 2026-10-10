package application

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Liapoldus/plugin-sdk/v2/domain/interfaces"
	"github.com/Liapoldus/plugin-sdk/v2/domain/models"
)

// ErrMissingSecretDependency is returned by NewSecretManager when a required
// port, the lifecycle, the clock or the observer is absent. It names no
// reference, handle, purpose or secret.
var ErrMissingSecretDependency = errors.New("plugin secret dependency is required")

// SecretManagerConfiguration carries the injected ports of the generic scoped
// secret use case. The manager is a client of Core's broker only: it holds no
// credential store, caches no secret material and never writes secret bytes,
// handles or references to any sink.
type SecretManagerConfiguration struct {
	// Broker issues and redeems scoped, expiring, one-use grants in Core.
	Broker interfaces.SecretBroker
	// Clock supplies the current time used for the local expiry decision. The
	// SDK never reads a wall clock directly.
	Clock interfaces.Clock
	// Lifecycle supplies the generation a grant is bound to.
	Lifecycle *Lifecycle
	// Observer receives one bounded event per secret outcome.
	Observer interfaces.LifecycleObserver
	// MaximumTrackedGrants bounds how many issued grants this replica tracks in
	// memory for the local expiry and one-use decision. It is a caller supplied
	// resource bound, not a contract default. When the bound is reached the
	// oldest tracked grant is forgotten, after which Core is again the only
	// authority for that handle.
	MaximumTrackedGrants int
}

// SecretManager issues and redeems scoped secret grants for the generation this
// replica has actually applied, or the exact candidate being atomically applied
// from inside the ConfigurationApplier callback.
//
// A grant is bound to the applied generation, checked for expiry against the
// injected clock before and after the broker call, and usable once: a second
// redemption of the same issued grant is refused locally without a call to
// Core, and a failed redemption closes the grant locally so that a call whose
// outcome is unknown is never replayed. Handles are never returned in an error,
// in an acknowledgement or in a log field.
type SecretManager struct {
	broker     interfaces.SecretBroker
	clock      interfaces.Clock
	lifecycle  *Lifecycle
	observer   interfaces.LifecycleObserver
	classifier OutcomeClassifier
	maximum    int

	stateMu sync.Mutex
	grants  map[string]trackedGrant
	order   []string
}

type trackedGrant struct {
	expiresAt time.Time
	spent     bool
}

// NewSecretManager builds the scoped secret use case. It fails closed when a
// port, the lifecycle, the clock or the observer is missing, or when the tracking
// bound is not positive, so no unbounded handle set can be created.
func NewSecretManager(configuration SecretManagerConfiguration) (*SecretManager, error) {
	if configuration.Broker == nil || configuration.Clock == nil || configuration.Lifecycle == nil ||
		configuration.Observer == nil || configuration.MaximumTrackedGrants <= 0 {
		return nil, ErrMissingSecretDependency
	}
	manager := &SecretManager{
		broker:    configuration.Broker,
		clock:     configuration.Clock,
		lifecycle: configuration.Lifecycle,
		observer:  configuration.Observer,
		maximum:   configuration.MaximumTrackedGrants,
		grants:    make(map[string]trackedGrant),
		order:     make([]string, 0, configuration.MaximumTrackedGrants),
	}
	if optional, ok := configuration.Broker.(OutcomeClassifier); ok {
		manager.classifier = optional
	}
	return manager, nil
}

// IssueGrant asks Core for one scoped, expiring, one-use grant bound to the
// generation this replica has applied.
//
// It fails with models.ErrNotReady while neither an active nor an applying
// generation exists, and with
// models.ErrInvalidSecretGrant when the reference or purpose is unusable or when
// the request names a generation this replica did not apply. An empty generation
// is bound to the applied one; the SDK never issues a grant for a generation it
// has not activated.
func (manager *SecretManager) IssueGrant(ctx context.Context, request models.SecretGrantRequest) (models.SecretGrant, error) {
	generation, ready := manager.lifecycle.SecretGrantGeneration()
	if !ready {
		return models.SecretGrant{}, models.ErrNotReady
	}
	bound := request
	if bound.Generation != "" && bound.Generation != generation {
		return models.SecretGrant{}, models.ErrInvalidSecretGrant
	}
	bound.Generation = generation
	if err := bound.Validate(); err != nil {
		return models.SecretGrant{}, err
	}

	grant, err := manager.broker.IssueGrant(ctx, bound)
	if err != nil {
		outcome := classifyFailure(err, manager.classifier, permitsSecretIssueOutcome)
		manager.observe(ctx, KindSecretGrant, outcome)
		return models.SecretGrant{}, refusal(outcome, nil)
	}
	if err := grant.Validate(); err != nil || grant.Generation != bound.Generation {
		// A grant the broker did not bind to the generation this replica applied
		// would hand the plugin a secret for a configuration that never ran, so it
		// is refused on the same footing as a malformed grant.
		manager.observe(ctx, KindSecretGrant, models.OutcomeGrantDenied)
		return models.SecretGrant{}, models.ErrInvalidSecretGrant
	}

	manager.track(grant)
	manager.observe(ctx, KindSecretGrant, models.OutcomeGranted)
	return grant, nil
}

// Redeem exchanges one grant for the referenced value.
//
// The expiry of a tracked grant is checked locally before the call and again
// after it, so a grant that expires during the round trip destroys the value
// instead of returning it. A grant this replica already redeemed or already
// failed to redeem is refused locally without a call to Core. A handle the SDK
// does not track, for example after a restart, is still offered to Core, which
// remains the authority for a single-use grant.
func (manager *SecretManager) Redeem(ctx context.Context, redemption models.SecretRedemption) (models.SecretValue, error) {
	if err := redemption.Validate(); err != nil {
		return models.SecretValue{}, err
	}

	tracked, known := manager.lookup(redemption.Handle)
	switch {
	case known && tracked.spent:
		manager.observe(ctx, KindSecretRedemption, models.OutcomeGrantSpent)
		return models.SecretValue{}, refusal(models.OutcomeGrantSpent, models.ErrInvalidSecretGrant)
	case known && !tracked.expiresAt.After(manager.clock.Now()):
		manager.markSpent(redemption.Handle)
		manager.observe(ctx, KindSecretRedemption, models.OutcomeGrantExpired)
		return models.SecretValue{}, refusal(models.OutcomeGrantExpired, nil)
	}
	if outcome, expired := contextOutcome(ctx); expired {
		manager.markSpent(redemption.Handle)
		manager.observe(ctx, KindSecretRedemption, outcome)
		return models.SecretValue{}, refusal(outcome, nil)
	}

	value, err := manager.broker.Redeem(ctx, redemption)
	if err != nil {
		// A redemption is single use, so a failed attempt closes the grant
		// locally: the SDK never replays a call whose outcome it does not know.
		value.Destroy()
		manager.markSpent(redemption.Handle)
		outcome := classifyFailure(err, manager.classifier, permitsRedemptionOutcome)
		manager.observe(ctx, KindSecretRedemption, outcome)
		return models.SecretValue{}, refusal(outcome, nil)
	}

	if known && !tracked.expiresAt.After(manager.clock.Now()) {
		value.Destroy()
		manager.markSpent(redemption.Handle)
		manager.observe(ctx, KindSecretRedemption, models.OutcomeGrantExpired)
		return models.SecretValue{}, refusal(models.OutcomeGrantExpired, nil)
	}

	manager.markSpent(redemption.Handle)
	manager.observe(ctx, KindSecretRedemption, models.OutcomeSucceeded)
	return value, nil
}

// SecretProvider is the one-call convenience for a plugin that needs a single
// operational secret: it issues a grant for the reference and purpose, redeems
// it immediately and returns the value. The intermediate grant is never
// retained, logged or returned, and a value obtained on a failing path is
// destroyed before the error is returned. The caller owns the returned value and
// destroys it when the generation it belongs to is replaced or the process
// shuts down.
func (manager *SecretManager) SecretProvider(ctx context.Context, reference, purpose string) (models.SecretValue, error) {
	grant, err := manager.IssueGrant(ctx, models.SecretGrantRequest{Reference: reference, Purpose: purpose})
	if err != nil {
		return models.SecretValue{}, err
	}
	return manager.Redeem(ctx, models.SecretRedemption{Handle: grant.Handle})
}

func (manager *SecretManager) observe(ctx context.Context, kind Kind, outcome models.Outcome) {
	if !outcome.Valid() {
		return
	}
	manager.observer.Observe(ctx, string(kind), outcome)
}

func (manager *SecretManager) track(grant models.SecretGrant) {
	manager.stateMu.Lock()
	defer manager.stateMu.Unlock()
	if _, exists := manager.grants[grant.Handle]; !exists {
		manager.order = append(manager.order, grant.Handle)
	}
	manager.grants[grant.Handle] = trackedGrant{expiresAt: grant.ExpiresAt}
	for len(manager.order) > manager.maximum {
		delete(manager.grants, manager.order[0])
		manager.order = manager.order[1:]
	}
}

func (manager *SecretManager) lookup(handle string) (trackedGrant, bool) {
	manager.stateMu.Lock()
	defer manager.stateMu.Unlock()
	tracked, known := manager.grants[handle]
	return tracked, known
}

func (manager *SecretManager) markSpent(handle string) {
	manager.stateMu.Lock()
	defer manager.stateMu.Unlock()
	tracked, known := manager.grants[handle]
	if !known {
		return
	}
	tracked.spent = true
	manager.grants[handle] = tracked
}

// permitsSecretIssueOutcome is the set of outcomes a transport adapter may
// claim for a failed grant issue. The local readiness and syntax refusals stay
// with the SDK.
func permitsSecretIssueOutcome(outcome models.Outcome) bool {
	switch outcome {
	case models.OutcomeNotPermitted, models.OutcomeGrantDenied, models.OutcomeCoreUnavailable,
		models.OutcomeCancelled, models.OutcomeDeadlineExceeded:
		return true
	}
	return false
}

// permitsRedemptionOutcome is the set of outcomes a transport adapter may claim
// for a failed redemption. The local expiry, replay and syntax refusals stay
// with the SDK.
func permitsRedemptionOutcome(outcome models.Outcome) bool {
	switch outcome {
	case models.OutcomeNotPermitted, models.OutcomeGrantUnknown, models.OutcomeGrantDenied,
		models.OutcomeGrantExpired, models.OutcomeGrantSpent, models.OutcomeCoreUnavailable,
		models.OutcomeCancelled, models.OutcomeDeadlineExceeded:
		return true
	}
	return false
}
