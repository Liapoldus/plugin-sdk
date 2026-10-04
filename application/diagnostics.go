package application

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/Liapoldus/plugin-sdk/domain/interfaces"
	"github.com/Liapoldus/plugin-sdk/domain/models"
)

// Kind is the closed vocabulary of generic lifecycle events the SDK itself
// emits. It is the only source of a metric kind label inside this layer, so the
// presentation layer publishes the same values instead of spelling them out
// again. A caller that invents a kind is refused by Recorder.
type Kind string

const (
	// KindReload labels one generic Reload notification outcome.
	KindReload Kind = "reload"
	// KindSecretGrant labels one generic secret grant issue outcome.
	KindSecretGrant Kind = "secretGrant"
	// KindSecretRedemption labels one generic secret redemption outcome.
	KindSecretRedemption Kind = "secretRedemption"
)

// ErrMissingMetricsSink is returned by NewRecorder when no metrics sink is
// configured. A recorder without a sink is a no-op that only hides the absence
// of observability, so it is refused instead.
var ErrMissingMetricsSink = errors.New("plugin metrics sink is required")

// ErrInvalidRecorderConfiguration is returned by NewRecorder when the kind
// allowlist or the kind length bound is unusable, which would leave label
// cardinality unbounded.
var ErrInvalidRecorderConfiguration = errors.New("invalid plugin lifecycle recorder configuration")

// MetricsSink is the narrow, dependency-free metrics port of the SDK. The SDK
// defines no metric name, no help text and no exposition format: those belong
// to the versioned contract asset, so a sink stays a plain Go interface and
// needs no third-party dependency.
type MetricsSink interface {
	// Lifecycle counts one lifecycle operation by kind and outcome.
	Lifecycle(kind string, outcome models.Outcome)
	// PullFailure counts one exact-generation pull attempt that did not yield
	// an applicable document.
	PullFailure(outcome models.Outcome)
	// SetReady publishes whether this replica has an active generation.
	SetReady(ready bool)
}

// ReadinessObserver is the optional port an observer implements to also publish
// the replica readiness gauge. The SDK updates it when an active generation
// becomes eligible, when a reload refusal fences the replica, and when an
// idempotent announcement clears that pending refusal.
type ReadinessObserver interface {
	// SetReady publishes the readiness of this replica.
	SetReady(ready bool)
}

// PullFailureReporter is the optional port an observer implements to also count
// exact-generation pull attempts that did not yield an applicable document.
type PullFailureReporter interface {
	// PullFailure records the outcome of a pull attempt that failed.
	PullFailure(outcome models.Outcome)
}

// RecorderConfiguration configures the lifecycle metrics recorder.
type RecorderConfiguration struct {
	// Sink receives the bounded counters and the readiness gauge.
	Sink MetricsSink
	// AllowedKinds is the closed set of kind labels this replica may publish.
	// It is supplied by the caller from its contract asset or from the Kind
	// constants of this package; an empty set is refused so that no
	// unbounded label can ever reach a metric.
	AllowedKinds []Kind
	// MaximumKindLength bounds a published kind label. It is a caller supplied
	// bound, not a contract default.
	MaximumKindLength int
}

// Recorder implements interfaces.LifecycleObserver and forwards bounded counters
// to a metrics sink. It enforces the closed outcome vocabulary and the kind
// allowlist, and drops anything else instead of forwarding a caller string, so
// no unknown outcome and no unbounded label can reach a metric.
//
// A recorder never accepts a configuration document, a secret value, a grant
// handle, a certificate or an address: the only data it can publish is a kind
// from the allowlist and an outcome from the domain vocabulary.
type Recorder struct {
	sink              MetricsSink
	kinds             map[string]struct{}
	maximumKindLength int
	denied            atomic.Uint64
}

// NewRecorder builds the metrics recorder. It fails closed when the sink, the
// kind allowlist or the kind length bound is missing, and when an allowed kind
// is empty or longer than the bound.
func NewRecorder(configuration RecorderConfiguration) (*Recorder, error) {
	if configuration.Sink == nil {
		return nil, ErrMissingMetricsSink
	}
	if len(configuration.AllowedKinds) == 0 || configuration.MaximumKindLength <= 0 {
		return nil, ErrInvalidRecorderConfiguration
	}
	kinds := make(map[string]struct{}, len(configuration.AllowedKinds))
	for _, kind := range configuration.AllowedKinds {
		kind := string(kind)
		if kind == "" || len(kind) > configuration.MaximumKindLength {
			return nil, ErrInvalidRecorderConfiguration
		}
		kinds[kind] = struct{}{}
	}
	return &Recorder{
		sink:              configuration.Sink,
		kinds:             kinds,
		maximumKindLength: configuration.MaximumKindLength,
	}, nil
}

// Observe implements interfaces.LifecycleObserver. It counts the event only when
// the outcome is in the closed vocabulary and the kind is in the allowlist;
// anything else is denied and counted as denied.
func (recorder *Recorder) Observe(_ context.Context, kind string, outcome models.Outcome) {
	if !recorder.permitted(kind, outcome) {
		recorder.denied.Add(1)
		return
	}
	recorder.sink.Lifecycle(kind, outcome)
}

// PullFailure implements PullFailureReporter. It counts only outcomes that mean
// a pull attempt did not yield an applicable document, so a refused apply or a
// refused request never inflates the pull counter.
func (recorder *Recorder) PullFailure(outcome models.Outcome) {
	if !outcome.Valid() || !countsAsPullFailure(outcome) {
		recorder.denied.Add(1)
		return
	}
	recorder.sink.PullFailure(outcome)
}

// SetReady implements ReadinessObserver.
func (recorder *Recorder) SetReady(ready bool) {
	recorder.sink.SetReady(ready)
}

// Denied reports how many events were refused because their outcome or kind was
// outside the allowed vocabulary. It holds no labels and no payload, so a caller
// can notice a misconfigured recorder without any cardinality.
func (recorder *Recorder) Denied() uint64 {
	return recorder.denied.Load()
}

func (recorder *Recorder) permitted(kind string, outcome models.Outcome) bool {
	if !outcome.Valid() || kind == "" || len(kind) > recorder.maximumKindLength {
		return false
	}
	_, allowed := recorder.kinds[kind]
	return allowed
}

// LoggingObserverConfiguration carries the logger and the redaction policy of
// the structured lifecycle log. Every bound and every key list is supplied by
// the caller from its versioned contract asset; this layer spells none of them.
type LoggingObserverConfiguration struct {
	// Logger is the operator-owned structured sink.
	Logger interfaces.Logger
	// RedactedKeys are the key fragments whose value is replaced by the
	// placeholder. Matching is case insensitive and substring based.
	RedactedKeys []string
	// AlwaysRedactedKeys are the key fragments that are never emitted at all,
	// not even with a placeholder.
	AlwaysRedactedKeys []string
	// RedactedPlaceholder replaces a redacted value. When it is empty a
	// redacted field is dropped rather than emitted with an empty value.
	RedactedPlaceholder string
	// MaximumFields bounds the fields of one record.
	MaximumFields int
	// MaximumKeyLength bounds a key. A longer key drops the field.
	MaximumKeyLength int
	// MaximumValueLength bounds a value in bytes. A longer value is truncated
	// at a rune boundary.
	MaximumValueLength int
}

// LoggingObserver writes one bounded, redacted record per lifecycle event
// through interfaces.Logger.
//
// It exposes no API that accepts a configuration document, a grant handle, a
// secret or an address: the only data it can emit is a bounded kind and a
// closed-vocabulary outcome, and both pass through RedactFields again on the way
// out. A refused or invalid event produces no line at all, because there is
// nothing safe left to say.
type LoggingObserver struct {
	logger             interfaces.Logger
	redactedKeys       []string
	alwaysRedactedKeys []string
	placeholder        string
	maximumFields      int
	maximumKeyLength   int
	maximumValueLength int
}

// NewLoggingObserver builds the structured lifecycle log observer. It fails
// closed when the logger is missing or when a bound is not positive.
func NewLoggingObserver(configuration LoggingObserverConfiguration) (*LoggingObserver, error) {
	if configuration.Logger == nil || configuration.MaximumFields <= 0 ||
		configuration.MaximumKeyLength <= 0 || configuration.MaximumValueLength <= 0 {
		return nil, ErrMissingLifecycleDependency
	}
	return &LoggingObserver{
		logger:             configuration.Logger,
		redactedKeys:       configuration.RedactedKeys,
		alwaysRedactedKeys: configuration.AlwaysRedactedKeys,
		placeholder:        configuration.RedactedPlaceholder,
		maximumFields:      configuration.MaximumFields,
		maximumKeyLength:   configuration.MaximumKeyLength,
		maximumValueLength: configuration.MaximumValueLength,
	}, nil
}

// Observe implements interfaces.LifecycleObserver.
func (observer *LoggingObserver) Observe(ctx context.Context, kind string, outcome models.Outcome) {
	if !outcome.Valid() || kind == "" {
		return
	}
	fields := RedactFields(observer.redactedKeys, observer.alwaysRedactedKeys, observer.placeholder,
		observer.maximumFields, observer.maximumKeyLength, observer.maximumValueLength,
		[]interfaces.Field{
			{Key: "kind", Value: kind},
			{Key: "outcome", Value: string(outcome)},
		})
	if len(fields) == 0 {
		return
	}
	if isSuccessOutcome(outcome) {
		observer.logger.Debug(ctx, "plugin lifecycle event", fields...)
		return
	}
	observer.logger.Warn(ctx, "plugin lifecycle event refused", fields...)
}

// isSuccessOutcome reports whether an outcome is one of the success outcomes of
// the lifecycle vocabulary. Everything else is a refusal and is logged as one.
func isSuccessOutcome(outcome models.Outcome) bool {
	switch outcome {
	case models.OutcomeApplied, models.OutcomeAlreadyActive, models.OutcomeSucceeded, models.OutcomeGranted:
		return true
	}
	return false
}

// Observers fans one bounded lifecycle event out to several observers and
// forwards the optional readiness and pull-failure signals to whichever observer
// implements them. It lets a replica keep metrics and logs in step with a single
// injected observer, and it never widens what any observer may emit.
type Observers []interfaces.LifecycleObserver

var (
	_ interfaces.LifecycleObserver = Observers(nil)
	_ ReadinessObserver            = Observers(nil)
	_ PullFailureReporter          = Observers(nil)
)

// Observe implements interfaces.LifecycleObserver.
func (observers Observers) Observe(ctx context.Context, kind string, outcome models.Outcome) {
	for _, observer := range observers {
		if observer != nil {
			observer.Observe(ctx, kind, outcome)
		}
	}
}

// SetReady implements ReadinessObserver.
func (observers Observers) SetReady(ready bool) {
	for _, observer := range observers {
		if reporter, ok := observer.(ReadinessObserver); ok {
			reporter.SetReady(ready)
		}
	}
}

// PullFailure implements PullFailureReporter.
func (observers Observers) PullFailure(outcome models.Outcome) {
	for _, observer := range observers {
		if reporter, ok := observer.(PullFailureReporter); ok {
			reporter.PullFailure(outcome)
		}
	}
}
