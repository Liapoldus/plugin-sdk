package main

// Fixture-only plugin-owned boundary and contract composition.
//
// This file is a test fixture and is never part of a production package. The
// applier, the metadata provider and the metrics sink are exactly the plugin
// owned pieces a real plugin author writes: the SDK owns the transport, the
// lifecycle and the REST surface, and the plugin owns what its document means.
// The contract mapping below is the composition step a plugin author also has to
// write, because the handler layer takes the contract as an injected value and
// never parses the asset itself.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"liapoldus.local/plugin-sdk/domain/models"
	"liapoldus.local/plugin-sdk/infrastructure"
	"liapoldus.local/plugin-sdk/presentation"
)

var (
	errTestApplyRefused = errors.New("test applier refused the generation")
	errTestAsset        = errors.New("test fixture could not reach the contract asset")
	errTestRoute        = errors.New("test fixture does not serve every contract route")
)

// testApplier is the plugin-owned validator and atomic applier.
//
// It inspects the plugin's own document and nothing else, and it activates a
// generation only after the whole document has been accepted, so a refusal
// leaves the previously active configuration fully usable.
type testApplier struct {
	mutex                     sync.Mutex
	active                    models.Configuration
	applied                   uint64
	refused                   uint64
	rejects                   map[string]struct{}
	lastError                 string
	beforeApply               func(context.Context, models.Configuration) (string, error)
	candidateSecretGeneration string
}

func newTestApplier(rejects ...string) *testApplier {
	applier := &testApplier{rejects: make(map[string]struct{}, len(rejects))}
	for _, generation := range rejects {
		applier.rejects[generation] = struct{}{}
	}
	return applier
}

// Apply implements the plugin-owned applier port.
func (applier *testApplier) Apply(ctx context.Context, configuration models.Configuration) error {
	if err := ctx.Err(); err != nil {
		// A cancelled apply must activate nothing.
		return err
	}
	secretGeneration := ""
	if applier.beforeApply != nil {
		var err error
		secretGeneration, err = applier.beforeApply(ctx, configuration)
		if err != nil {
			return err
		}
	}
	applier.mutex.Lock()
	defer applier.mutex.Unlock()
	if _, refuse := applier.rejects[configuration.Generation]; refuse {
		applier.refused++
		applier.lastError = errTestApplyRefused.Error()
		return errTestApplyRefused
	}
	applier.active = configuration
	applier.candidateSecretGeneration = secretGeneration
	applier.applied++
	applier.lastError = ""
	return nil
}

func (applier *testApplier) lastCandidateSecretGeneration() string {
	applier.mutex.Lock()
	defer applier.mutex.Unlock()
	return applier.candidateSecretGeneration
}

// active reports the configuration the fixture is currently serving.
func (applier *testApplier) activeConfiguration() (models.Configuration, bool) {
	applier.mutex.Lock()
	defer applier.mutex.Unlock()
	if applier.active.Generation == "" {
		return models.Configuration{}, false
	}
	return applier.active, true
}

// counters reports how many generations were activated and how many were refused.
func (applier *testApplier) counters() (applied, refused uint64) {
	applier.mutex.Lock()
	defer applier.mutex.Unlock()
	return applier.applied, applier.refused
}

// testMetadata is the plugin-owned producer of the two opaque documents the SDK
// publishes verbatim. The SDK never decodes them, so their shape is entirely the
// plugin's business.
type testMetadata struct{}

func (testMetadata) Manifest(context.Context) ([]byte, error) {
	return []byte(testManifestDocument), nil
}

func (testMetadata) ConfigurationSchema(context.Context) ([]byte, error) {
	return []byte(testSchemaDocument), nil
}

// testMetricsSink is the plugin author's bridge from the application recorder to
// the infrastructure exposition. The application layer counts a bounded event;
// the infrastructure collector renders it in the contract exposition format. The
// SDK ships the two ends of that bridge but not the join, because which
// instrumentation a plugin runs is the plugin's decision.
type testMetricsSink struct {
	collector *infrastructure.ObserverPrometheusCollector
}

func (sink testMetricsSink) Lifecycle(kind string, outcome models.Outcome) {
	sink.collector.Observe(context.Background(), kind, outcome)
}

// PullFailure implements the application metrics port. The contract exposes one
// counter for exact-generation pull failures, so a refusal of any outcome counts
// once against it.
func (sink testMetricsSink) PullFailure(models.Outcome) {
	sink.collector.RecordConfigPullFailure()
}

func (sink testMetricsSink) SetReady(ready bool) {
	sink.collector.SetReady(ready)
}

// presentationContracts maps the loaded contract onto the injected view the
// handler layer validates at construction time.
//
// Three parts come straight off the loaded contract: the seven routes, the
// content types and the error table. The fourth, the transport refusal table,
// lives in a top-level block of the same asset that the loader does not expose,
// so the fixture reads that one block from the same file instead of restating
// it. The version of the file read is checked against the loaded contract, so a
// fixture pointed at a different asset version fails closed at composition time
// rather than serving a mixture of two contracts.
func presentationContracts(contract infrastructure.HTTPContract) (presentation.Contracts, error) {
	problems, err := testTransportProblems(contract)
	if err != nil {
		return presentation.Contracts{}, err
	}
	routes := make(map[string]presentation.Endpoint, len(contract.Plugin.Endpoints))
	for _, name := range contract.EndpointNames() {
		endpoint, err := contract.Endpoint(name)
		if err != nil {
			return presentation.Contracts{}, err
		}
		routes[name] = presentation.Endpoint{Method: endpoint.Method, Path: endpoint.Path}
	}
	// Every registered route must be served by exactly one handler field, so a
	// contract that grows a route this fixture does not serve is a startup error
	// rather than a silent 404.
	served := make(map[string]presentation.Endpoint, 7)
	for name, endpoint := range map[string]presentation.Endpoint{
		testRouteIdentity:     routes[testRouteIdentity],
		testRouteManifest:     routes[testRouteManifest],
		testRouteConfigSchema: routes[testRouteConfigSchema],
		testRouteHealth:       routes[testRouteHealth],
		testRouteReady:        routes[testRouteReady],
		testRouteReload:       routes[testRouteReload],
		testRouteMetrics:      routes[testRouteMetrics],
	} {
		if endpoint.Path == "" {
			return presentation.Contracts{}, fmt.Errorf("%w: %s", errTestRoute, name)
		}
		served[name] = endpoint
	}
	if len(served) != len(routes) {
		return presentation.Contracts{}, errTestRoute
	}

	// The contract already registers every error key, so this is a straight copy
	// of the table rather than a re-keyed view of it.
	errorTable := make(map[string]presentation.Problem, len(contract.Errors))
	for key, problem := range contract.Errors {
		errorTable[key] = presentation.Problem{Status: problem.Status, Code: problem.Code}
	}

	plugin := contract.Plugin
	contracts := presentation.Contracts{
		ContractVersion:       contract.ContractVersion,
		IdentityEndpoint:      served[testRouteIdentity],
		ManifestEndpoint:      served[testRouteManifest],
		ConfigSchemaEndpoint:  served[testRouteConfigSchema],
		HealthEndpoint:        served[testRouteHealth],
		ReadyEndpoint:         served[testRouteReady],
		ReloadEndpoint:        served[testRouteReload],
		MetricsEndpoint:       served[testRouteMetrics],
		HealthStatus:          plugin.Responses.Health.Status,
		HealthBody:            plugin.Responses.Health.Body,
		ContentTypes:          presentation.ContentTypes{JSON: plugin.Responses.ContentTypes.JSON, Metrics: plugin.Responses.ContentTypes.Metrics},
		ReloadRequest:         testDocument(plugin.ReloadRequest),
		ReloadAcknowledgement: testDocument(plugin.ReloadAcknowledgement),
		Readiness:             testDocument(plugin.Readiness),
		Manifest:              testDocument(plugin.Manifest),
		ConfigurationSchema:   testDocument(plugin.ConfigurationSchema),
		Registration:          testDocument(infrastructure.DocumentContract{MediaType: contract.Identity.Registration.MediaType, MaximumBytes: contract.Identity.Registration.MaximumBytes, Required: contract.Identity.Registration.Required}),
		MaximumMetadataBytes:  plugin.MaximumMetadataBytes,
		ReadinessDeadline:     testSeconds(contract.Deadlines.PluginReadinessSeconds),
		Problems:              problems,
		Errors:                errorTable,
		OutcomeProblems:       contract.OutcomeProblems,
		SuccessOutcomes:       contract.SuccessOutcomes,
	}
	if err := contracts.Validate(); err != nil {
		return presentation.Contracts{}, err
	}
	return contracts, nil
}

// testDocument converts one contract document description to the injected form.
func testDocument(document infrastructure.DocumentContract) presentation.DocumentContract {
	return presentation.DocumentContract{
		MediaType:    document.MediaType,
		MaximumBytes: document.MaximumBytes,
		Required:     document.Required,
	}
}

// testAssetDocument is the one part of the asset the loader does not expose: the
// top-level block of refusals the transport itself raises.
type testAssetDocument struct {
	ContractVersion string `json:"contractVersion"`
	Problems        map[string]struct {
		Status int    `json:"status"`
		Code   string `json:"code"`
	} `json:"problems"`
}

// testTransportProblems reads the transport refusal block from the same asset
// file the SDK embeds and checks that it describes the contract the fixture just
// loaded.
func testTransportProblems(contract infrastructure.HTTPContract) (map[string]presentation.Problem, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("%w: caller", errTestAsset)
	}
	// The asset lives at the module root, three directories above this file, so
	// the path is resolved from the source location rather than from the working
	// directory the harness happened to start the fixture in.
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "..",
		"infrastructure", "assets", "plugin-sdk", "v1", "http-contract.json")
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errTestAsset, err)
	}
	var asset testAssetDocument
	if err := json.Unmarshal(contents, &asset); err != nil {
		return nil, fmt.Errorf("%w: %v", errTestAsset, err)
	}
	if asset.ContractVersion != contract.ContractVersion {
		// The fixture would otherwise be serving the refusal table of a contract
		// it is not otherwise speaking.
		return nil, fmt.Errorf("%w: asset version %q, loaded %q",
			errTestAsset, asset.ContractVersion, contract.ContractVersion)
	}
	problems := make(map[string]presentation.Problem, len(asset.Problems))
	for key, problem := range asset.Problems {
		problems[key] = presentation.Problem{Status: problem.Status, Code: problem.Code}
	}
	return problems, nil
}
