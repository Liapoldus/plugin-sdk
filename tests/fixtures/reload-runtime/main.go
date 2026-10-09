package main

// Fixture-only composition root for the reload runtime.
//
// This file is a test fixture and is never part of a production package. It
// wires the SDK's four production layers into one process that plays both roles
// at once: the plugin replica, which serves the contract REST surface and pulls
// its configuration from Core, and the Core control plane, which publishes
// immutable generations and brokers one-use secret grants.
//
// The fixture adds no lifecycle of its own. Every call a Core would make goes
// through the same production adapters in both directions, over mutual TLS with
// per-replica identity, so a scenario exercises the shipped transport, the
// shipped policy and the shipped contract rather than a re-implementation. The
// only listener that is not mutual TLS is the harness control surface, which
// exists so a test can drive scenarios the SDK never initiates on its own.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Liapoldus/plugin-sdk/tests/support/process"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Liapoldus/plugin-sdk/application"
	"github.com/Liapoldus/plugin-sdk/domain/interfaces"
	"github.com/Liapoldus/plugin-sdk/domain/models"
	"github.com/Liapoldus/plugin-sdk/infrastructure"
	"github.com/Liapoldus/plugin-sdk/presentation"
)

var errTestStart = errors.New("test fixture could not start")

// The logical names the contract registers its routes under. These are
// identifiers the asset uses, not contract values, and the presentation mapping
// checks that the asset registers exactly this set.
const (
	testRouteIdentity       = "identity"
	testRouteManifest       = "manifest"
	testRouteConfigSchema   = "configSchema"
	testRouteHealth         = "health"
	testRouteReady          = "ready"
	testRouteReload         = "reload"
	testRouteArtifactStream = "artifactStream"
	testRouteAdminSurface   = "adminSurface"
	testRouteAdminAction    = "adminAction"
	testRouteMetrics        = "metrics"
)

// The harness paths. They belong to the fixture alone and are named here once.
// The control surface speaks no contract, so it names its own media type.
const (
	testRouteControlReload             = "/control/reload"
	testRouteControlPublish            = "/control/publish"
	testRouteControlFault              = "/control/fault"
	testRouteControlSecretFault        = "/control/secret-fault"
	testRouteControlSecret             = "/control/secret"
	testRouteControlState              = "/control/state"
	testRouteControlDocument           = "/control/document"
	testRouteControlDrops              = "/control/drops"
	testRouteControlConnections        = "/control/connections"
	testRouteControlRotation           = "/control/rotation"
	testRouteControlReconnect          = "/control/reconnect"
	testRouteControlLoad               = "/control/load"
	testRouteControlArtifactStream     = "/control/artifact-stream"
	testRouteControlArtifactProbes     = "/control/artifact-probes"
	testRouteControlContractValidation = "/control/contract-validation"
	testRouteControlAdminSurface       = "/control/admin-surface"
	testRouteControlAdminAction        = "/control/admin-action"
	testRouteControlAdminProbes        = "/control/admin-action-probes"
	testControlMediaType               = "application/json"
)

// Fixture inputs. These are a plugin author's test data, not contract values: one
// replica identity, one schema version, two plugin-owned documents, the four
// seeded generations, and the secret a scenario provisions.
const (
	testInstanceID    = "fixture-instance"
	testReplicaID     = "fixture-replica-1"
	testSchemaVersion = "fixture/v1"

	// The seeded generation names are the fixture's own vocabulary, and they are
	// the only place a harness learns which refusal a generation is there to
	// provoke. The active one is reported on the ready line; the other three are
	// named so a scenario can reload exactly the document that produces one
	// specific contract outcome.
	testDefaultBase   = "generation-active"
	testDuplicateBase = "generation-duplicate-keys"
	testApplyFailBase = "generation-apply-failure"
	testCorruptBase   = "generation-corrupt"

	testLoopbackAddress = "127.0.0.1:0"
	// testTrackedGrants bounds the plugin's local grant tracking and the Core
	// broker's tracked redemptions. They are fixture resource bounds, chosen
	// small because the fixture issues one grant at a time.
	testTrackedGrants = 8
	// testMaximumKindLength bounds a published metric kind label.
	testMaximumKindLength = 64
	testSecretReference   = "fixture-secret-reference"
	testSecretPurpose     = "fixture-purpose"
)

// The two plugin-owned documents. The SDK publishes them verbatim and never
// interprets a field, so their exact shape is the plugin's business.
const (
	testManifestDocument = `{"schemaVersion":"fixture/v1","capabilities":[]}`
	testSchemaDocument   = `{"type":"object","additionalProperties":true}`
)

// testReadyLine is the one line this fixture writes to stdout. A harness reads it
// to learn the addresses, the seeded generation, and the two checks that have
// already been performed. Everything else the process produces goes to stderr.
type testReadyLine struct {
	PluginURL                string `json:"pluginURL"`
	ControlURL               string `json:"controlURL"`
	CoreURL                  string `json:"coreURL"`
	PluginHTTPSURL           string `json:"pluginHTTPSURL"`
	Generation               string `json:"generation"`
	Digest                   string `json:"digest"`
	DuplicateDigest          string `json:"duplicateDigest"`
	ExpectedRawJSON          string `json:"expectedRawJSON"`
	MTLSRejectsAnonymous     string `json:"mTLSRejectsAnonymous"`
	MTLSRejectsRevoked       string `json:"mTLSRejectsRevoked"`
	MTLSRejectsWrongIdentity string `json:"mTLSRejectsWrongIdentity"`
	MTLSRejectsExpired       string `json:"mTLSRejectsExpired"`
	RejectsUnsafeControlURLs string `json:"rejectsUnsafeControlURLs"`
	// The authority PEM and the PLUGIN client keypair are published so a harness
	// written in a language that owns its own TLS stack can verify the Core
	// stand-in and authenticate to it, exactly as the SDK does. The plugin
	// identity is the one published because the Core control server pins the
	// plugin peer: a harness standing in for the Core replica must present the
	// credential Core would accept, or it can never observe Core's own response
	// headers or its refusals. They belong to the throwaway authority this
	// fixture generates in memory for one run and exist for no other purpose, so
	// handing them to the harness that started this process reveals nothing that
	// is not already inside this test tree.
	CABundlePEM                   string `json:"caBundlePEM"`
	CorePlaneClientCertificatePEM string `json:"corePlaneClientCertificatePEM"`
	CorePlaneClientKeyPEM         string `json:"corePlaneClientKeyPEM"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "reload-runtime fixture: %v\n", err)
		os.Exit(1)
	}
}

// run builds the whole fixture, publishes its first line, and serves until the
// process is asked to stop.
func run() error {
	contract, err := infrastructure.LoadHTTPContract()
	if err != nil {
		return fmt.Errorf("%w: %w", errTestStart, err)
	}
	contracts, err := presentationContracts(contract)
	if err != nil {
		return fmt.Errorf("%w: %w", errTestStart, err)
	}
	identities, err := newTestIdentities(contract)
	if err != nil {
		return fmt.Errorf("%w: %w", errTestStart, err)
	}
	core, err := newTestCore(contract)
	if err != nil {
		return fmt.Errorf("%w: %w", errTestStart, err)
	}
	clock := testClock{}

	// Every listener is bound before anything is announced, so a harness that
	// reads the ready line can never race a port that does not exist yet.
	pluginListener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", testLoopbackAddress)
	if err != nil {
		return fmt.Errorf("%w: plugin plaintext listener: %w", errTestStart, err)
	}
	defer process.Close(pluginListener)
	pluginTLSListener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", testLoopbackAddress)
	if err != nil {
		return fmt.Errorf("%w: plugin mutual-TLS listener: %w", errTestStart, err)
	}
	defer process.Close(pluginTLSListener)
	coreListener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", testLoopbackAddress)
	if err != nil {
		return fmt.Errorf("%w: Core listener: %w", errTestStart, err)
	}
	defer process.Close(coreListener)
	controlListener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", testLoopbackAddress)
	if err != nil {
		return fmt.Errorf("%w: control listener: %w", errTestStart, err)
	}
	defer process.Close(controlListener)

	pluginURL := "http://" + pluginListener.Addr().String()
	pluginTLSURL := "https://" + pluginTLSListener.Addr().String()
	coreURL := "https://" + coreListener.Addr().String()
	controlURL := "http://" + controlListener.Addr().String()

	// One credential set per role. The plugin's own material is what its server
	// presents and what its client to Core presents, so the whole credential
	// surface of the fixture is these two loaded sets and nothing else.
	pluginCredentials, err := infrastructure.LoadCredentials(contract, identities.plugin)
	if err != nil {
		return fmt.Errorf("%w: plugin credentials: %w", errTestStart, err)
	}
	pluginProvider, err := infrastructure.NewStaticCredentialsProvider(pluginCredentials)
	if err != nil {
		return fmt.Errorf("%w: plugin credentials: %w", errTestStart, err)
	}
	coreCredentials, err := infrastructure.LoadCredentials(contract, identities.core)
	if err != nil {
		return fmt.Errorf("%w: Core credentials: %w", errTestStart, err)
	}
	coreProvider, err := infrastructure.NewStaticCredentialsProvider(coreCredentials)
	if err != nil {
		return fmt.Errorf("%w: Core credentials: %w", errTestStart, err)
	}
	// The revocation decision that gates a handshake comes from the same loaded
	// authorities that admitted the chain, so trust and revocation are one
	// decision rather than two that can disagree.
	pluginRevocation, err := newTestRevocation(pluginCredentials, identities.revocation, clock)
	if err != nil {
		return fmt.Errorf("%w: plugin revocation: %w", errTestStart, err)
	}
	coreRevocation, err := newTestRevocation(coreCredentials, identities.revocation, clock)
	if err != nil {
		return fmt.Errorf("%w: Core revocation: %w", errTestStart, err)
	}

	// Instrumentation is wired before the use cases, because both the lifecycle
	// and the secret manager require an observer and refuse to build without one.
	collector, err := infrastructure.NewObserverPrometheusCollector(contract)
	if err != nil {
		return fmt.Errorf("%w: metrics collector: %w", errTestStart, err)
	}
	// The contract sends production logs to stdout. This fixture reserves stdout
	// for its single ready line, so the SDK's structured stream is written to
	// stderr instead. That is a decision about where one process's output goes; it
	// changes neither the contract nor the logger.
	logger, err := infrastructure.NewJSONLogger(contract, os.Stderr)
	if err != nil {
		return fmt.Errorf("%w: logger: %w", errTestStart, err)
	}
	recorder, err := application.NewRecorder(application.RecorderConfiguration{
		Sink: testMetricsSink{collector: collector},
		AllowedKinds: []application.Kind{
			application.KindReload,
			application.KindSecretGrant,
			application.KindSecretRedemption,
		},
		MaximumKindLength: testMaximumKindLength,
	})
	if err != nil {
		return fmt.Errorf("%w: recorder: %w", errTestStart, err)
	}
	observer, err := application.NewLoggingObserver(application.LoggingObserverConfiguration{
		Logger:              logger,
		RedactedPlaceholder: contract.Logging.RedactedPlaceholder,
		MaximumFields:       contract.Logging.MaximumFields,
		MaximumKeyLength:    contract.Logging.MaximumKeyLength,
		MaximumValueLength:  contract.Logging.MaximumValueLength,
	})
	if err != nil {
		return fmt.Errorf("%w: logging observer: %w", errTestStart, err)
	}
	fanout := application.Observers{recorder, observer}

	// The plugin's client to Core. It dials the Core replica's own identity and
	// presents the plugin's client certificate, and the Core side is built the
	// same way below with the roles exchanged.
	pluginToCore, err := infrastructure.NewMutualTLSClient(contract, pluginProvider,
		infrastructure.MutualTLSClientConfig{
			Peer:       identities.corePeer,
			ServerName: testServerName,
			Revocation: pluginRevocation,
			Clock:      clock,
		})
	if err != nil {
		return fmt.Errorf("%w: plugin to Core client: %w", errTestStart, err)
	}
	source, err := infrastructure.NewCoreConfigurationSource(contract, coreURL, pluginToCore)
	if err != nil {
		return fmt.Errorf("%w: configuration source: %w", errTestStart, err)
	}
	broker, err := infrastructure.NewCoreSecretBroker(contract, coreURL, pluginToCore,
		testTrackedGrants)
	if err != nil {
		return fmt.Errorf("%w: secret broker: %w", errTestStart, err)
	}

	applier := newTestApplier(testApplyFailBase)
	lifecycle, err := application.NewLifecycle(application.LifecycleConfiguration{
		Source:   source,
		Applier:  applier,
		Identity: models.ReplicaIdentity{InstanceID: testInstanceID, ReplicaID: testReplicaID},
		Observer: fanout,
	})
	if err != nil {
		return fmt.Errorf("%w: lifecycle: %w", errTestStart, err)
	}
	secrets, err := application.NewSecretManager(application.SecretManagerConfiguration{
		Broker:               broker,
		Clock:                clock,
		Lifecycle:            lifecycle,
		Observer:             fanout,
		MaximumTrackedGrants: testTrackedGrants,
	})
	if err != nil {
		return fmt.Errorf("%w: secret manager: %w", errTestStart, err)
	}
	applier.beforeApply = func(ctx context.Context, configuration models.Configuration) (string, error) {
		grant, issueErr := secrets.IssueGrant(ctx, models.SecretGrantRequest{
			Reference: testSecretReference,
			Purpose:   testSecretPurpose,
		})
		if issueErr != nil {
			return "", issueErr
		}
		value, redeemErr := secrets.Redeem(ctx, models.SecretRedemption{Handle: grant.Handle})
		if redeemErr != nil {
			return "", redeemErr
		}
		value.Destroy()
		return grant.Generation, nil
	}

	// The plugin's REST surface. Readiness is the lifecycle's own answer adapted to
	// the port the handler set declares, and registration is the lifecycle's
	// document, which always advertises the contract this handler set answers for.
	artifactReceiver := &testArtifactReceiver{}
	artifactReceiver.started = make(chan struct{}, 1)
	adminActions := &testAdminActions{started: make(chan struct{})}
	handlers, err := presentation.NewHandlerSet(presentation.HandlerConfiguration{
		Contracts:    contracts,
		Lifecycle:    lifecycle,
		Readiness:    presentation.WithoutContext(lifecycle.Readiness),
		Registration: testRegistrationOf(lifecycle),
		Metadata:     testMetadata{},
		Metrics:      collector,
		Artifacts:    artifactReceiver,
		AdminSurface: testAdminMetadata{},
		AdminActions: adminActions,
	})
	if err != nil {
		return fmt.Errorf("%w: handler set: %w", errTestStart, err)
	}

	// The Core replica. The production plugin-side server is reused symmetrically
	// here, configured with the plugin's pinned identity instead of the Core's, so
	// the Core control API is served by the same mutual-TLS policy the plugin
	// surface is rather than by a second hand-written TLS path.
	coreServer, err := infrastructure.NewMutualTLSServer(contract,
		infrastructure.MutualTLSServerConfig{
			Handler:    core.handler,
			Provider:   coreProvider,
			Peer:       identities.pluginPeer,
			Revocation: coreRevocation,
			ErrorLog:   log.New(io.Discard, "", 0),
			Clock:      clock,
		})
	if err != nil {
		return fmt.Errorf("%w: Core server: %w", errTestStart, err)
	}
	// The connection-drop switch and the connection count both hang off the
	// plugin's mutual-TLS listener only. The switch wraps the production handler
	// chain, so a dropped request is one the listener really accepted; the
	// plaintext mirror keeps serving the same handler without the fault, so the
	// two surfaces cannot drift.
	drops := newTestDrops(handlers.Handler())
	connections := &testConnections{}
	pluginServer, err := infrastructure.NewMutualTLSServer(contract,
		infrastructure.MutualTLSServerConfig{
			Handler:    drops,
			Provider:   pluginProvider,
			Peer:       identities.corePeer,
			Revocation: pluginRevocation,
			ErrorLog:   log.New(io.Discard, "", 0),
			Clock:      clock,
		})
	if err != nil {
		return fmt.Errorf("%w: plugin server: %w", errTestStart, err)
	}
	pluginServer.Server().ConnState = connections.hook

	// The Core-side client that drives the plugin. It is the same adapter a real
	// Core replica uses, over a client that presents the Core's own certificate
	// and pins the plugin's identity.
	coreToPluginTLS, err := infrastructure.NewMutualTLSClient(contract, coreProvider,
		infrastructure.MutualTLSClientConfig{
			Peer:       identities.pluginPeer,
			ServerName: testServerName,
			Revocation: coreRevocation,
			Clock:      clock,
		})
	if err != nil {
		return fmt.Errorf("%w: Core to plugin transport: %w", errTestStart, err)
	}
	artifactStatus := &testArtifactStatusOverride{transport: coreToPluginTLS}
	coreToPlugin, err := infrastructure.NewPluginClient(contract, pluginTLSURL,
		artifactStatus, identities.pluginPeer)
	if err != nil {
		return fmt.Errorf("%w: Core to plugin client: %w", errTestStart, err)
	}

	generations, err := publishTestGenerations(core)
	if err != nil {
		return fmt.Errorf("%w: %w", errTestStart, err)
	}
	readyEndpoint, err := contract.Endpoint("ready")
	if err != nil {
		return fmt.Errorf("%w: %w", errTestStart, err)
	}

	// The control surface drives the same SDK client and the same secret use case a
	// Core would, so a scenario observes shipped behaviour rather than fixture
	// behaviour. The issued handle is held by the plugin process, as a product
	// would hold it in its own data path, and is never written to an answer.
	// The scenarios a harness can ask for later. They are built here and started
	// only on demand, so the rest of the fixture runs with exactly the listeners it
	// always had.
	scenarios := newTestScenarios(testScenarioInputs{
		contract:         contract,
		clock:            clock,
		identities:       identities,
		coreProvider:     coreProvider,
		pluginProvider:   pluginProvider,
		coreRevocation:   coreRevocation,
		pluginRevocation: pluginRevocation,
		drops:            drops,
		connections:      connections,
		pluginAddress:    pluginTLSURL,
		readyPath:        readyEndpoint.Path,
	})
	control := newTestControl(core, contract, coreToPlugin, newTestSecrets(secrets), applier, scenarios, artifactReceiver, adminActions, artifactStatus, coreCredentials, pluginTLSURL)

	unsafeControlRefused, err := testUnsafeControlRefused(contract)
	if err != nil {
		return fmt.Errorf("%w: %w", errTestStart, err)
	}

	// The plaintext mirror and the control surface are the two fixture-only
	// listeners. The mirror serves the same handler the mutual-TLS listener serves,
	// so the two surfaces can never drift. Both are loopback-only and both write
	// their transport diagnostics nowhere.
	plaintext := &http.Server{
		Handler:           handlers.Handler(),
		ReadHeaderTimeout: testSeconds(contract.Deadlines.PluginReadHeaderSeconds),
		ReadTimeout:       testSeconds(contract.Deadlines.PluginReadSeconds),
		WriteTimeout:      testSeconds(contract.Deadlines.PluginWriteSeconds),
		IdleTimeout:       testSeconds(contract.Deadlines.PluginIdleSeconds),
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	controlServer := &http.Server{
		Handler:           control.handler,
		ReadHeaderTimeout: testSeconds(contract.Deadlines.PluginReadHeaderSeconds),
		ErrorLog:          log.New(io.Discard, "", 0),
	}

	failures := make(chan error, 4)
	go func() { failures <- ignoreClosed(pluginServer.Serve(pluginTLSListener)) }()
	go func() { failures <- ignoreClosed(coreServer.Serve(coreListener)) }()
	go func() { failures <- ignoreClosed(plaintext.Serve(pluginListener)) }()
	go func() { failures <- ignoreClosed(controlServer.Serve(controlListener)) }()

	// The check the ready line reports is performed after the listeners serve and
	// before the line is written, so a harness never has to take the fixture's word
	// for it. It runs after Serve on purpose: a probe against a bound but unserved
	// listener would see a refused connection and could not tell that apart from a
	// refused handshake.
	anonymousRejected, err := testMutualTLSRejectsAnonymous(coreToPlugin,
		pluginTLSListener.Addr().String(), readyEndpoint.Path,
		contract.Plugin.Readiness.MaximumBytes, coreCredentials,
		testSeconds(contract.Deadlines.PluginReadSeconds))
	if err != nil {
		return fmt.Errorf("%w: %w", errTestStart, err)
	}

	revokedRejected, err := testMutualTLSRejectsRevoked(coreToPlugin,
		pluginTLSListener.Addr().String(), readyEndpoint.Path,
		contract.Plugin.Readiness.MaximumBytes, coreCredentials, identities.revoked,
		testSeconds(contract.Deadlines.PluginReadSeconds))
	if err != nil {
		return fmt.Errorf("%w: %w", errTestStart, err)
	}

	wrongIdentityRejected, err := testMutualTLSRejectsWrongIdentity(coreToPlugin,
		pluginTLSListener.Addr().String(), readyEndpoint.Path,
		contract.Plugin.Readiness.MaximumBytes, coreCredentials, identities.impostor,
		testSeconds(contract.Deadlines.PluginReadSeconds))
	if err != nil {
		return fmt.Errorf("%w: %w", errTestStart, err)
	}

	expiredRejected, err := testMutualTLSRejectsExpired(coreToPlugin,
		pluginTLSListener.Addr().String(), readyEndpoint.Path,
		contract.Plugin.Readiness.MaximumBytes, coreCredentials, identities.expired,
		testSeconds(contract.Deadlines.PluginReadSeconds))
	if err != nil {
		return fmt.Errorf("%w: %w", errTestStart, err)
	}

	ready, err := json.Marshal(testReadyLine{
		PluginURL:                     pluginURL,
		ControlURL:                    controlURL,
		CoreURL:                       coreURL,
		PluginHTTPSURL:                pluginTLSURL,
		Generation:                    generations.active,
		Digest:                        generations.digest,
		DuplicateDigest:               generations.duplicateDigest,
		ExpectedRawJSON:               generations.rawJSON,
		MTLSRejectsAnonymous:          testReadyFlag(anonymousRejected),
		MTLSRejectsRevoked:            testReadyFlag(revokedRejected),
		MTLSRejectsWrongIdentity:      testReadyFlag(wrongIdentityRejected),
		MTLSRejectsExpired:            testReadyFlag(expiredRejected),
		RejectsUnsafeControlURLs:      testReadyFlag(unsafeControlRefused),
		CABundlePEM:                   string(identities.core.CABundle),
		CorePlaneClientCertificatePEM: string(identities.plugin.ClientCertificatePEM),
		CorePlaneClientKeyPEM:         string(identities.plugin.ClientKeyPEM),
	})
	if err != nil {
		return fmt.Errorf("%w: ready line: %w", errTestStart, err)
	}
	if _, err := fmt.Fprintf(os.Stdout, "%s\n", ready); err != nil {
		return fmt.Errorf("%w: write ready line: %w", errTestStart, err)
	}
	// stdout is closed after the single ready line, so a stray write from any
	// later code path cannot add a second line for a harness to misparse.
	if closer, ok := any(os.Stdout).(io.Closer); ok {
		process.Close(closer)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
	case served := <-failures:
		return fmt.Errorf("%w: listener stopped: %w", errTestStart, served)
	}

	// Each mutual-TLS listener is stopped by the adapter that owns its own
	// deadline, so the contract's grace period is applied per surface instead of
	// to one process-wide stop.
	shutdown, cancel := context.WithTimeout(context.Background(),
		testSeconds(contract.Deadlines.PluginShutdownGraceSeconds))
	defer cancel()
	// A scenario listener a harness started and never stopped would otherwise stay
	// bound past the process exit, so the scenarios are torn down with the rest of
	// the surfaces rather than left for the operating system to reclaim.
	scenarios.shutdown()
	process.Must(pluginServer.GracefulShutdown(shutdown))
	process.Must(coreServer.GracefulShutdown(shutdown))
	process.Must(plaintext.Shutdown(shutdown))
	process.Must(controlServer.Shutdown(shutdown))
	coreToPlugin.CloseIdleConnections()
	pluginToCore.CloseIdleConnections()
	return nil
}

// testReadyFlag reports one performed check as the string the frozen harness reads.
func testReadyFlag(ok bool) string {
	if ok {
		return "true"
	}
	return "false"
}

// testSeconds converts a contract deadline in seconds to a duration.
func testSeconds(seconds int) time.Duration {
	return time.Duration(seconds) * time.Second
}

// ignoreClosed reports a server's own close as a clean stop, because every
// shutdown path in this fixture closes a listener deliberately.
func ignoreClosed(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// testMutualTLSRejectsAnonymous proves the mutual-TLS listener admits a Core that
// presents its own certificate and admits nothing else, before any harness request
// depends on it.
//
// The check is two probes, and the first one is what makes the second meaningful.
// The positive probe is the production readiness call over the production Core-side
// client, which proves the listener is live and that its client requirement and
// pinned peer identity are both satisfiable by a real Core. The anonymous probe
// then presents no certificate and no name, so the only thing that can refuse it
// is the server's own requirement. A refused handshake is the expected answer; a
// handshake that somehow succeeded would still not be enough, so the request
// itself has to be refused too.
func testMutualTLSRejectsAnonymous(client *infrastructure.PluginClient,
	address string, readyPath string, readyMaximum int64,
	trust *infrastructure.Credentials, timeout time.Duration) (bool, error) {
	authorities := trust.TrustAuthorities()
	if len(authorities) == 0 {
		return false, errTestStart
	}

	ready, err := testProbeReadiness(client, timeout)
	if err != nil {
		// The listener answered nothing usable, so the anonymous probe below would
		// be indistinguishable from a refused connection. Refuse to report a flag
		// the fixture has not earned.
		return false, err
	}
	if !ready {
		return false, fmt.Errorf("%w: a Core with its own certificate was refused", errTestStart)
	}

	pool := x509.NewCertPool()
	for _, authority := range authorities {
		pool.AddCert(authority)
	}
	anonymous := &http.Client{
		Transport: &http.Transport{
			DialTLSContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&tls.Dialer{NetDialer: &net.Dialer{Timeout: timeout},
					Config: &tls.Config{
						ServerName: testServerName,
						RootCAs:    pool,
						MinVersion: tls.VersionTLS12,
					}}).DialContext(ctx, network, address)
			},
		},
		Timeout: timeout,
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://"+address+readyPath, nil)
	if err != nil {
		return true, nil
	}
	response, err := anonymous.Do(request) //nolint:bodyclose // the response body is closed before this probe returns.
	if err != nil {
		// The listener is already proven live, so a failure here can only be the
		// refused handshake this check exists to observe.
		return true, nil
	}
	defer process.Close(response.Body)
	if _, err := io.Copy(io.Discard, io.LimitReader(response.Body, readyMaximum)); err != nil {
		panic(err)
	}
	return response.StatusCode >= 400, nil
}

// testMutualTLSRejectsRevoked proves the mutual-TLS listener refuses a peer whose
// certificate the revocation list names, before any harness request depends on it.
//
// The credential it presents is a genuine Core replica client certificate, issued
// by the same authority, carrying the same name and the same usage as the live
// one. Nothing about it is untrusted, expired or malformed, so the revocation
// gate is the only thing that can refuse it. A refusal is therefore evidence about
// revocation specifically, which is why this check exists at all: Node never
// consults a CRL, so a harness could not observe this handshake itself.
func testMutualTLSRejectsRevoked(client *infrastructure.PluginClient,
	address string, readyPath string, readyMaximum int64,
	trust *infrastructure.Credentials, revoked infrastructure.CredentialsMaterial,
	timeout time.Duration) (bool, error) {
	return testMutualTLSRefusesCredential(client, address, readyPath, readyMaximum,
		trust, revoked, timeout)
}

// testMutualTLSRejectsWrongIdentity proves the mutual-TLS listener refuses a peer
// that is trusted, in date and unrevoked, but presents a different replica
// identity.
//
// The credential is issued by the very authority the listener trusts, carries the
// same client-auth usage and the same validity window as the live Core replica
// credential, and its URI still begins with the contract's trust-domain prefix, so
// the contract's own URI rule is satisfied. Only the replica identity differs, and
// it differs in both names at once, because the SDK accepts a peer that matches
// either the common name or the URI: an impostor that got only one of them wrong
// would be accepted on the other. A refusal is therefore evidence about the pinned
// peer identity specifically.
func testMutualTLSRejectsWrongIdentity(client *infrastructure.PluginClient,
	address string, readyPath string, readyMaximum int64,
	trust *infrastructure.Credentials, impostor infrastructure.CredentialsMaterial,
	timeout time.Duration) (bool, error) {
	return testMutualTLSRefusesCredential(client, address, readyPath, readyMaximum,
		trust, impostor, timeout)
}

// testMutualTLSRejectsExpired proves the mutual-TLS listener refuses a peer whose
// certificate is no longer in date.
//
// The credential is the registered Core replica identity, issued by the trusted
// authority, with the same client-auth usage and a serial the revocation list does
// not name. The only thing wrong with it is that its validity window closed, so a
// refusal is evidence about validity. Go's own chain verification rejects an
// expired leaf during the handshake, before the SDK's peer gate is consulted, and
// that is a refusal by the live listener all the same: a caller cannot connect
// with an expired peer either way.
func testMutualTLSRejectsExpired(client *infrastructure.PluginClient,
	address string, readyPath string, readyMaximum int64,
	trust *infrastructure.Credentials, expired infrastructure.CredentialsMaterial,
	timeout time.Duration) (bool, error) {
	return testMutualTLSRefusesCredential(client, address, readyPath, readyMaximum,
		trust, expired, timeout)
}

// testMutualTLSRefusesCredential is the check behind all three of the credential
// probes: the listener is first shown to answer a good credential, and is then
// offered exactly one other credential, over the same production dialer shape. The
// credential is otherwise indistinguishable from the live one, so whatever refuses
// it is the only thing about it that is wrong.
//
// The positive probe is what makes the refusal meaningful. A refused connection to
// a listener that was never proven live would prove nothing at all, so the check
// is not allowed to report a flag it has not earned.
func testMutualTLSRefusesCredential(client *infrastructure.PluginClient,
	address string, readyPath string, readyMaximum int64,
	trust *infrastructure.Credentials, presented infrastructure.CredentialsMaterial,
	timeout time.Duration) (bool, error) {
	authorities := trust.TrustAuthorities()
	if len(authorities) == 0 {
		return false, errTestStart
	}
	keyPair, err := tls.X509KeyPair(presented.ClientCertificatePEM, presented.ClientKeyPEM)
	if err != nil {
		return false, fmt.Errorf("%w: probe credential: %w", errTestStart, err)
	}

	ready, err := testProbeReadiness(client, timeout)
	if err != nil {
		return false, err
	}
	if !ready {
		return false, fmt.Errorf("%w: a Core with its own certificate was refused", errTestStart)
	}

	pool := x509.NewCertPool()
	for _, authority := range authorities {
		pool.AddCert(authority)
	}
	offered := &http.Client{
		Transport: &http.Transport{
			DialTLSContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&tls.Dialer{NetDialer: &net.Dialer{Timeout: timeout},
					Config: &tls.Config{
						ServerName:   testServerName,
						RootCAs:      pool,
						Certificates: []tls.Certificate{keyPair},
						MinVersion:   tls.VersionTLS13,
					}}).DialContext(ctx, network, address)
			},
		},
		Timeout: timeout,
	}
	// The verdict is read off whether the connection produced a response at all,
	// not off the status it carried. A response of any status means the handshake
	// completed and the listener accepted the presented credential: the peer gate
	// runs inside the handshake, so it can only have refused before there was
	// anything to answer with. Reading the status instead would let a credential
	// the listener accepted look refused, because the fixture asks this question
	// before a configuration generation has been acknowledged and readiness
	// answers 503 until one is. That would make this check report a refusal for
	// every credential it was ever given.
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://"+address+readyPath, nil)
	if err != nil {
		return true, nil
	}
	response, err := offered.Do(request) //nolint:bodyclose // the response body is closed before this probe returns.
	if err != nil {
		// The listener is already proven live with a good credential, so a
		// failure here can only be the refusal this check exists to observe.
		return true, nil
	}
	defer process.Close(response.Body)
	if _, err := io.Copy(io.Discard, io.LimitReader(response.Body, readyMaximum)); err != nil {
		panic(err)
	}
	return false, nil
}

// testProbeReadiness performs the production readiness call and reports whether
// the served listener answered it.
func testProbeReadiness(client *infrastructure.PluginClient,
	timeout time.Duration) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if _, err := client.Readiness(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// testUnsafeControlRefused proves the production control adapters will not accept
// a plaintext Core address. Every control URL in this fixture is https, and each
// production constructor refuses anything else, so a Core can never be handed a
// downgraded control path.
func testUnsafeControlRefused(contract infrastructure.HTTPContract) (bool, error) {
	plaintext := "http://" + testLoopbackAddress
	if _, err := infrastructure.NewCoreConfigurationSource(contract, plaintext,
		http.DefaultClient); err == nil {
		return false, nil
	}
	if _, err := infrastructure.NewPluginClient(contract, plaintext, http.DefaultClient,
		models.PeerIdentity{CommonName: testCoreCommonName}); err == nil {
		return false, nil
	}
	return true, nil
}

// testRegistration adapts the lifecycle's registration document to the port the
// handler set declares. The port asks for the document by the contract version,
// so the plugin always advertises the contract it is actually answering for.
type testRegistration struct {
	lifecycle *application.Lifecycle
}

func (registration testRegistration) Registration(contractVersion string) (models.Registration, error) {
	return registration.lifecycle.Registration(contractVersion)
}

func testRegistrationOf(lifecycle *application.Lifecycle) presentation.RegistrationProvider {
	return testRegistration{lifecycle: lifecycle}
}

// testSecrets is the plugin-side view the control surface drives.
//
// A real plugin keeps the handle it was issued where its own data path needs it.
// The fixture does the same, because the SDK's one-use decision is keyed by the
// handle and cannot be observed otherwise. The handle is held here and is never
// written to an answer, a log line, an error or the ready line.
type testSecrets struct {
	manager *application.SecretManager

	mutex   sync.Mutex
	handles map[string]string
}

func newTestSecrets(manager *application.SecretManager) *testSecrets {
	return &testSecrets{manager: manager, handles: make(map[string]string)}
}

// classify reports one secret failure the way a plugin would report it to its own
// operator: the contract outcome the use case itself resolved, and nothing else.
//
// The outcome is read through the accessor the presentation layer uses, so the
// harness attributes a refusal exactly the way a Core would, rather than
// re-classifying the error or parsing a message.
func (secrets *testSecrets) classify(err error) string {
	if err == nil {
		return ""
	}
	if outcome, ok := application.OutcomeOf(err); ok {
		return string(outcome)
	}
	return testErrorName(err)
}

// issue asks Core for one grant and remembers the handle for this reference and
// purpose, reporting only the outcome.
func (secrets *testSecrets) issue(ctx context.Context, reference, purpose string) (string, error) {
	grant, err := secrets.manager.IssueGrant(ctx, models.SecretGrantRequest{
		Reference: reference,
		Purpose:   purpose,
	})
	if err != nil {
		return secrets.classify(err), err
	}
	secrets.mutex.Lock()
	secrets.handles[testSecretKey(reference, purpose)] = grant.Handle
	secrets.mutex.Unlock()
	return "granted", nil
}

// provide issues one grant and redeems it once, reporting only bounded proof of
// the value. The value is destroyed before the answer is built, and the answer
// carries a length and a digest rather than the bytes.
func (secrets *testSecrets) provide(ctx context.Context, reference, purpose string) (int, string, error) {
	if _, err := secrets.issue(ctx, reference, purpose); err != nil {
		return 0, "", err
	}
	handle, ok := secrets.take(reference, purpose)
	if !ok {
		return 0, "", models.ErrInvalidSecretGrant
	}
	value, err := secrets.manager.Redeem(ctx, models.SecretRedemption{Handle: handle})
	if err != nil {
		return 0, "", err
	}
	// The bytes are read once, folded into a digest inside the same expression and
	// then destroyed, so no name in this process ever holds the value.
	contents := value.Bytes()
	digest := testDigest(contents)
	length := len(contents)
	value.Destroy()
	return length, digest, nil
}

// redeemTwice issues one grant and redeems it twice, which is how a scenario
// observes the one-use decision: the second redemption is refused locally, without
// a second call to Core, and both outcomes are reported.
func (secrets *testSecrets) redeemTwice(ctx context.Context, reference, purpose string) (string, string, error) {
	if _, err := secrets.issue(ctx, reference, purpose); err != nil {
		return "", "", err
	}
	handle, ok := secrets.take(reference, purpose)
	if !ok {
		return "", "", models.ErrInvalidSecretGrant
	}
	first := "succeeded"
	if _, err := secrets.manager.Redeem(ctx, models.SecretRedemption{Handle: handle}); err != nil {
		first = secrets.classify(err)
	}
	second := "succeeded"
	if _, err := secrets.manager.Redeem(ctx, models.SecretRedemption{Handle: handle}); err != nil {
		second = secrets.classify(err)
		return first, second, err
	}
	return first, second, nil
}

// take removes and returns the handle for one reference and purpose, so a second
// scenario step has to issue a fresh grant rather than reusing one.
func (secrets *testSecrets) take(reference, purpose string) (string, bool) {
	secrets.mutex.Lock()
	defer secrets.mutex.Unlock()
	key := testSecretKey(reference, purpose)
	handle, known := secrets.handles[key]
	delete(secrets.handles, key)
	return handle, known
}

// testSecretKey is the fixture's own map key for a reference and purpose. It
// never reaches an answer.
func testSecretKey(reference, purpose string) string { return reference + "\x00" + purpose }

// publishTestGenerations seeds the store with the generations the frozen harness
// and the manual scenarios address by name.
//
// The four generations exist because each is the only way to reach one distinct
// contract outcome: a valid apply, a refusal that must leave the previous
// generation active, a document whose bytes are announced as a different
// generation's, and a document that is not a well-formed JSON object.
func publishTestGenerations(core *testCore) (testGenerationSet, error) {
	document := testDefaultDocument()
	duplicate := testDuplicateDocument()
	set := testGenerationSet{
		active:          testDefaultBase,
		rawJSON:         document,
		digest:          testDigest([]byte(document)),
		duplicateDigest: testDigest([]byte(duplicate)),
	}
	// The active generation is a well-formed document with a trailing newline, so
	// a harness comparing the exact bytes it published sees them back unchanged.
	if err := core.publish(testGeneration{
		generation:    set.active,
		schemaVersion: testSchemaVersion,
		rawJSON:       []byte(document),
		state:         interfaces.GenerationStateActive,
	}); err != nil {
		return testGenerationSet{}, err
	}
	// The duplicate-key document carries its own truthful digest. It has to: a
	// harness that reloads it announces that digest, and the only way to reach the
	// malformed-document outcome is a document whose announced descriptors are
	// correct and whose bytes are still not a JSON object.
	if err := core.publish(testGeneration{
		generation:      testDuplicateBase,
		schemaVersion:   testSchemaVersion,
		announcedDigest: set.duplicateDigest,
		rawJSON:         []byte(duplicate),
		state:           interfaces.GenerationStateActive,
		malformed:       true,
	}); err != nil {
		return testGenerationSet{}, err
	}
	// The apply-failure generation is byte-identical to the active one. It is a
	// different generation of the same document, which is the honest way to reach a
	// refusal that has to leave the previous generation active: the digest
	// verifies, the plugin's own applier refuses, and nothing changes.
	if err := core.publish(testGeneration{
		generation:      testApplyFailBase,
		schemaVersion:   testSchemaVersion,
		announcedDigest: set.digest,
		rawJSON:         []byte(document),
		state:           interfaces.GenerationStateActive,
	}); err != nil {
		return testGenerationSet{}, err
	}
	// The generation whose bytes are a different document from the one announced,
	// so a reload of it is refused as a digest mismatch before any apply.
	if err := core.publish(testGeneration{
		generation:             testCorruptBase,
		schemaVersion:          testSchemaVersion,
		announcedDigest:        set.digest,
		announcedSchemaVersion: testSchemaVersion,
		rawJSON:                []byte(testCorruptDocument()),
		state:                  interfaces.GenerationStateActive,
	}); err != nil {
		return testGenerationSet{}, err
	}
	return set, nil
}

// testGenerationSet is what the ready line publishes about the seeded store.
type testGenerationSet struct {
	active          string
	rawJSON         string
	digest          string
	duplicateDigest string
}

// testDefaultDocument is the valid plugin configuration document. It is the
// plugin's own payload; the SDK verifies and forwards it and never reads a field.
func testDefaultDocument() string {
	return `{"replica":"fixture","enabled":true,"items":[1,2,3]}` + "\n"
}

// testDuplicateDocument is a document with one key twice. It is the only way to
// reach the malformed-document outcome, so it has to be marked malformed to be
// stored at all.
func testDuplicateDocument() string {
	return `{"replica":"fixture","replica":"duplicate","enabled":true}` + "\n"
}

// testCorruptDocument is a valid document the store announces under another
// document's digest, so a reload of it is refused as a digest mismatch.
func testCorruptDocument() string {
	return `{"replica":"fixture","enabled":false}` + "\n"
}
