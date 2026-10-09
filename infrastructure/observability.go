package infrastructure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Liapoldus/plugin-sdk/domain/interfaces"
	"github.com/Liapoldus/plugin-sdk/domain/models"
)

var (
	// ErrInvalidLogger rejects a logger configuration that cannot meet the
	// contract's structured-output rules, for example an absent sink or a level
	// outside the contract vocabulary.
	ErrInvalidLogger = errors.New("invalid Plugin SDK logger configuration")
	// ErrInvalidObserver rejects an observer configuration that cannot produce
	// contract-conformant metrics.
	ErrInvalidObserver = errors.New("invalid Plugin SDK observer configuration")
	// ErrObserverContractIncomplete reports that the contract lacks a metric name
	// or label the collector is required to emit. Metrics are never invented, so a
	// missing name is an error rather than a fallback string.
	ErrObserverContractIncomplete = errors.New("incomplete Plugin SDK observer contract")
)

// JSONLogger writes newline-delimited JSON log records to an operator-owned
// sink. It enforces the contract's level vocabulary, field-count bound, key and
// value truncation, and redacted-key policy at the transport edge, so a caller
// that forgot to redact a sensitive key still cannot leak its value.
type JSONLogger struct {
	contract HTTPContract
	writer   io.Writer
	levels   map[string]int
	minimum  int
	clock    interfaces.Clock
	mutex    sync.Mutex
}

// NewJSONLogger returns a logger bound to the contract's logging rules. The
// default level is the contract default, not a level chosen in Go.
func NewJSONLogger(contract HTTPContract, writer io.Writer) (*JSONLogger, error) {
	if writer == nil || contract.Logging.DefaultLevel == "" ||
		contract.Logging.MaximumFields <= 0 || contract.Logging.MaximumKeyLength <= 0 ||
		contract.Logging.MaximumValueLength <= 0 || contract.Logging.RedactedPlaceholder == "" {
		return nil, ErrInvalidLogger
	}
	levels := make(map[string]int, len(contract.Logging.Levels))
	for index, level := range contract.Logging.Levels {
		levels[level] = index
	}
	minimum, ok := levels[contract.Logging.DefaultLevel]
	if !ok {
		return nil, ErrInvalidLogger
	}
	return &JSONLogger{contract: contract, writer: writer, levels: levels, minimum: minimum}, nil
}

// WithJSONLoggerClock replaces the timestamp source. It is the seam a test or a
// deterministic host uses; production callers use the system clock.
func (logger *JSONLogger) WithJSONLoggerClock(clock interfaces.Clock) *JSONLogger {
	if logger == nil {
		return nil
	}
	return &JSONLogger{
		contract: logger.contract,
		writer:   logger.writer,
		levels:   logger.levels,
		minimum:  logger.minimum,
		clock:    clock,
		mutex:    sync.Mutex{},
	}
}

// WithJSONLoggerLevel returns a logger that accepts this level and everything
// more severe. A level outside the contract vocabulary is refused, so a
// misconfigured host cannot silently disable every record.
func (logger *JSONLogger) WithJSONLoggerLevel(level string) (*JSONLogger, error) {
	if logger == nil {
		return nil, ErrInvalidLogger
	}
	minimum, ok := logger.levels[level]
	if !ok {
		return nil, ErrInvalidLogger
	}
	return &JSONLogger{
		contract: logger.contract,
		writer:   logger.writer,
		levels:   logger.levels,
		minimum:  minimum,
		clock:    logger.clock,
		mutex:    sync.Mutex{},
	}, nil
}

// Enabled reports whether a level at or above the configured minimum would be
// written.
func (logger *JSONLogger) Enabled(level string) bool {
	if logger == nil {
		return false
	}
	rank, ok := logger.levels[level]
	return ok && rank >= logger.minimum
}

// Debug writes one debug record.
func (logger *JSONLogger) Debug(ctx context.Context, message string, fields ...interfaces.Field) {
	logger.write(ctx, "debug", message, fields)
}

// Info writes one info record.
func (logger *JSONLogger) Info(ctx context.Context, message string, fields ...interfaces.Field) {
	logger.write(ctx, "info", message, fields)
}

// Warn writes one warning record.
func (logger *JSONLogger) Warn(ctx context.Context, message string, fields ...interfaces.Field) {
	logger.write(ctx, "warn", message, fields)
}

// Error writes one error record.
func (logger *JSONLogger) Error(ctx context.Context, message string, fields ...interfaces.Field) {
	logger.write(ctx, "error", message, fields)
}

func (logger *JSONLogger) write(_ context.Context, level, message string, fields []interfaces.Field) {
	if logger == nil || !logger.Enabled(level) {
		return
	}
	// Every key written here is either a contract stream name or a fixed record
	// envelope. Nothing is read back out of the context, so a caller cannot smuggle
	// a value into a record through a context key, and this package never invents
	// a field name the contract does not describe.
	record := map[string]string{
		"time":    clockNow(logger.clock).UTC().Format(time.RFC3339Nano),
		"level":   level,
		"stream":  logger.contract.Logging.Stream,
		"message": observerTruncate(message, logger.contract.Logging.MaximumValueLength),
	}
	for index, field := range fields {
		if index >= logger.contract.Logging.MaximumFields {
			break
		}
		key := observerTruncate(field.Key, logger.contract.Logging.MaximumKeyLength)
		if key == "" {
			continue
		}
		if logger.redacts(key) {
			record[key] = logger.contract.Logging.RedactedPlaceholder
			continue
		}
		record[key] = observerTruncate(field.Value, logger.contract.Logging.MaximumValueLength)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return
	}
	logger.mutex.Lock()
	defer logger.mutex.Unlock()
	if _, err := logger.writer.Write(append(encoded, '\n')); err != nil {
		// Logging cannot fail a lifecycle operation, and retrying a partial write
		// would duplicate an event. Never echo the writer's cause or record.
		return
	}
}

// redacts reports whether a key is contract-marked sensitive. Both the
// configurable key set and the always-redacted set apply, so an operator cannot
// remove a key from configuration and expose it.
func (logger *JSONLogger) redacts(key string) bool {
	lowered := strings.ToLower(key)
	for _, candidate := range logger.contract.Logging.AlwaysRedactedKeys {
		if strings.ToLower(candidate) == lowered {
			return true
		}
	}
	for _, candidate := range logger.contract.Logging.RedactedKeys {
		if strings.ToLower(candidate) == lowered {
			return true
		}
	}
	return false
}

// ObserverCollector extends the domain lifecycle observer with the readiness
// gauge and the configuration-pull failure counter the contract publishes, so
// the presentation layer has a single port to satisfy.
type ObserverCollector interface {
	interfaces.LifecycleObserver

	// SetReady publishes whether the plugin is currently able to serve.
	SetReady(ready bool)
	// RecordConfigPullFailure counts one failure to pull a configuration
	// generation from Core. The reason is deliberately not a label: a label
	// vocabulary of failure causes would leak response detail into metrics.
	RecordConfigPullFailure()
	// ContentType is the media type WriteText produces, taken from the contract.
	ContentType() string
	// WriteText renders the current metrics in the contract exposition format.
	WriteText(writer io.Writer) error
	// Snapshot returns a copy of the current sample values.
	Snapshot() ObserverSnapshot
}

// ObserverSnapshot is a point-in-time copy of the collector's counters.
type ObserverSnapshot struct {
	// Ready is the last published readiness value.
	Ready bool
	// ReadySet reports whether readiness was ever evaluated. A false value means
	// the gauge is still the contract's default 0 rather than an observed 0.
	ReadySet bool
	// PullFailures counts failed configuration pulls since start.
	PullFailures uint64
	// LifecycleSamples is the number of distinct counted lifecycle series.
	LifecycleSamples int
}

// ObserverLifecycleSample is one counted lifecycle observation. The pair is a
// key, not two independent labels, so the collector can never emit a series that
// the contract vocabulary does not define.
type ObserverLifecycleSample struct {
	Kind    string
	Outcome models.Outcome
}

// ObserverPrometheusCollector publishes the three contract metrics in the
// Prometheus text exposition format. Metric names, help strings, label names and
// the media type all come from the contract, so this package holds no second
// copy of any of them.
type ObserverPrometheusCollector struct {
	contract  HTTPContract
	permitted map[string]map[models.Outcome]struct{}
	ready     atomic.Bool
	readySet  atomic.Bool
	pullCount atomic.Uint64
	mutex     sync.Mutex
	samples   map[ObserverLifecycleSample]uint64
}

// Lifecycle operation names are the keys of the contract outcomes object. They are
// spelled here only to bind each typed group to its key; the accepted set and the
// outcomes allowed under each kind are still read from the contract, so a change
// to the asset is what changes what is counted.
const (
	observerKindReload           = "reload"
	observerKindConfigPull       = "configPull"
	observerKindSecretRedemption = "secretRedemption"
)

// NewObserverPrometheusCollector returns a collector for one contract.
//
// The accepted lifecycle vocabulary is the contract's own outcome groups: a kind
// is a key of the outcomes object, and the outcomes counted for that kind are
// exactly the ones that group lists. A kind the contract does not describe, an
// outcome that fails models.Outcome.Valid, and an outcome listed for a different
// kind are all refused. Nothing is defaulted and nothing is invented, so a
// published series always corresponds to a documented lifecycle operation.
func NewObserverPrometheusCollector(contract HTTPContract) (*ObserverPrometheusCollector, error) {
	metrics := contract.Plugin.Responses.Metrics
	if metrics.ReadyMetricName == "" || metrics.LifecycleCounterName == "" ||
		metrics.PullFailureCounterName == "" || metrics.LifecycleCounterKindLabel == "" ||
		metrics.LifecycleCounterOutcomeLabel == "" || contract.Plugin.Responses.ContentTypes.Metrics == "" {
		return nil, ErrObserverContractIncomplete
	}
	permitted := make(map[string]map[models.Outcome]struct{}, 3)
	for kind, group := range map[string][]string{
		observerKindReload:           contract.Outcomes.Reload,
		observerKindConfigPull:       contract.Outcomes.ConfigPull,
		observerKindSecretRedemption: contract.Outcomes.SecretRedemption,
	} {
		if len(group) == 0 {
			return nil, ErrObserverContractIncomplete
		}
		outcomes := make(map[models.Outcome]struct{}, len(group))
		for _, outcome := range group {
			candidate := models.Outcome(outcome)
			if !candidate.Valid() {
				return nil, ErrObserverContractIncomplete
			}
			outcomes[candidate] = struct{}{}
		}
		permitted[kind] = outcomes
	}
	return &ObserverPrometheusCollector{
		contract:  contract,
		permitted: permitted,
		samples:   make(map[ObserverLifecycleSample]uint64),
	}, nil
}

// SetReady publishes the readiness gauge. A plugin that has never evaluated its
// own readiness is exposed as 0 rather than omitted, so a scrape can never read
// the absence of a sample as readiness.
func (collector *ObserverPrometheusCollector) SetReady(ready bool) {
	if collector == nil {
		return
	}
	collector.ready.Store(ready)
	collector.readySet.Store(true)
}

// RecordConfigPullFailure counts one failed configuration pull.
func (collector *ObserverPrometheusCollector) RecordConfigPullFailure() {
	if collector == nil {
		return
	}
	collector.pullCount.Add(1)
}

// Observe counts one lifecycle observation.
//
// A kind the contract does not describe, an outcome that fails
// models.Outcome.Valid, and an outcome the contract does not list for that kind
// are all refused. The event is dropped rather than counted under a fallback
// value, because an invented series would be a metric the published contract does
// not describe.
func (collector *ObserverPrometheusCollector) Observe(_ context.Context, kind string, outcome models.Outcome) {
	if collector == nil || !outcome.Valid() {
		return
	}
	permitted, ok := collector.permitted[kind]
	if !ok {
		return
	}
	if _, ok := permitted[outcome]; !ok {
		return
	}
	collector.mutex.Lock()
	defer collector.mutex.Unlock()
	collector.samples[ObserverLifecycleSample{Kind: kind, Outcome: outcome}]++
}

// ContentType is the contract's metrics media type.
func (collector *ObserverPrometheusCollector) ContentType() string {
	if collector == nil {
		return ""
	}
	return collector.contract.Plugin.Responses.ContentTypes.Metrics
}

// WriteText renders the metrics. Output is sorted by kind then outcome, so two
// scrapes of the same state are byte-identical and a diff of consecutive scrapes
// shows only real changes.
func (collector *ObserverPrometheusCollector) WriteText(writer io.Writer) error {
	if collector == nil || writer == nil {
		return ErrInvalidObserver
	}
	snapshot := collector.snapshot()
	metrics := collector.contract.Plugin.Responses.Metrics
	var buffer bytes.Buffer

	buffer.WriteString("# HELP " + metrics.ReadyMetricName + " " + observerSingleLine(metrics.ReadyMetricHelp) + "\n")
	buffer.WriteString("# TYPE " + metrics.ReadyMetricName + " gauge\n")
	buffer.WriteString(metrics.ReadyMetricName + " " + observerGaugeValue(snapshot.Ready) + "\n")

	buffer.WriteString("# HELP " + metrics.LifecycleCounterName + " " + observerSingleLine(metrics.LifecycleCounterHelp) + "\n")
	buffer.WriteString("# TYPE " + metrics.LifecycleCounterName + " counter\n")
	for _, sample := range snapshot.Samples {
		buffer.WriteString(metrics.LifecycleCounterName +
			"{" + metrics.LifecycleCounterKindLabel + `="` + observerEscapeLabel(sample.Kind) + `",` +
			metrics.LifecycleCounterOutcomeLabel + `="` + observerEscapeLabel(string(sample.Outcome)) + `"} ` +
			strconv.FormatUint(sample.Count, 10) + "\n")
	}

	buffer.WriteString("# HELP " + metrics.PullFailureCounterName + " " + observerSingleLine(metrics.PullFailureCounterHelp) + "\n")
	buffer.WriteString("# TYPE " + metrics.PullFailureCounterName + " counter\n")
	buffer.WriteString(metrics.PullFailureCounterName + " " + strconv.FormatUint(snapshot.PullFailures, 10) + "\n")

	_, err := writer.Write(buffer.Bytes())
	return err
}

// Snapshot returns a copy of the current counters with lifecycle samples sorted
// for deterministic consumption.
func (collector *ObserverPrometheusCollector) Snapshot() ObserverSnapshot {
	if collector == nil {
		return ObserverSnapshot{}
	}
	state := collector.snapshot()
	return ObserverSnapshot{
		Ready:            state.Ready,
		ReadySet:         state.ReadySet,
		PullFailures:     state.PullFailures,
		LifecycleSamples: len(state.Samples),
	}
}

type observerCountedSample struct {
	ObserverLifecycleSample
	Count uint64
}

// observerState is the collector's internal point-in-time state. It is separate
// from ObserverSnapshot so the public surface stays a flat scalar summary while
// the exposition format can iterate ordered samples.
type observerState struct {
	Ready        bool
	ReadySet     bool
	PullFailures uint64
	Samples      []observerCountedSample
}

func (collector *ObserverPrometheusCollector) snapshot() observerState {
	if collector == nil {
		return observerState{}
	}
	collector.mutex.Lock()
	counted := make([]observerCountedSample, 0, len(collector.samples))
	for sample, count := range collector.samples {
		counted = append(counted, observerCountedSample{ObserverLifecycleSample: sample, Count: count})
	}
	collector.mutex.Unlock()
	sort.Slice(counted, func(first, second int) bool {
		if counted[first].Kind != counted[second].Kind {
			return counted[first].Kind < counted[second].Kind
		}
		return counted[first].Outcome < counted[second].Outcome
	})
	return observerState{
		Ready:        collector.ready.Load(),
		ReadySet:     collector.readySet.Load(),
		PullFailures: collector.pullCount.Load(),
		Samples:      counted,
	}
}

// observerGaugeValue renders a boolean as a Prometheus sample value. The exposition
// format accepts only a number here, so a boolean word would make the scrape
// unparseable.
func observerGaugeValue(ready bool) string {
	if ready {
		return "1"
	}
	return "0"
}

// observerTruncate bounds a value to the contract maximum. A value is cut rather
// than dropped, so a record always shows that something was present.
func observerTruncate(value string, maximum int) string {
	if maximum <= 0 || len(value) <= maximum {
		return value
	}
	return value[:maximum]
}

// observerSingleLine flattens a contract help string to one line, so a multi-line
// asset value cannot break the exposition format.
func observerSingleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// observerEscapeLabel escapes a label value per the exposition format.
func observerEscapeLabel(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)
	return replacer.Replace(value)
}
