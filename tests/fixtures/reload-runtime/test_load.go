package main

// Fixture-only load and drain, on a real mutual-TLS listener.
//
// This file is a test fixture and is never part of a production package. It
// answers a question the rest of the fixture never asks: what happens to a request
// that is already in flight when the operator asks the process to stop. The
// listener is a production mutual-TLS server and the request is read by a
// production mutual-TLS client, so the response has to survive a real drain of a
// real connection.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Liapoldus/plugin-sdk/domain/interfaces"
	"github.com/Liapoldus/plugin-sdk/domain/models"
	"github.com/Liapoldus/plugin-sdk/infrastructure"
)

// testLoadDocument is the fixed document the load surface serves to a request
// that is in flight during a drain. A fixed body of a known length is what makes a
// truncated or interleaved response detectable: the client reports the exact byte
// count and digest it received, and a harness can check that digest itself.
const testLoadDocument = "{\"operation\":\"load\",\"status\":\"drained\"}\n"

// testLoadDrainBound is the fraction of the contract's own shutdown grace within
// which a drain has to finish. It is deliberately a fraction rather than the whole
// grace, so a drain that only completes by waiting out its deadline fails here
// instead of passing slowly, and it is derived from the contract rather than
// chosen, so the two can never disagree.
const testLoadDrainGraceDivisor = 4

// testLoadEnteredTolerance bounds how long a drain waits for its own request to
// reach the handler, and how long the post-drain probe waits for a refusal. The
// handler signals the moment it is entered, so this only has to cover scheduling.
const testLoadEnteredTolerance = 2 * time.Second

// testLoadReadLimit bounds the document a drain will read, so a surface that
// answers with something enormous is reported rather than buffered.
const testLoadReadLimit = 1 << 20

var (
	errTestLoad    = errors.New("test load drain failed")
	errTestLoadBig = errors.New("test load response exceeded the fixture's read bound")
)

// testLoad owns the lifetime of the loaded surface. The surface plays the plugin
// side of the contract, so it is served with the plugin's credentials and pins the
// Core replica, and it is dialled with the Core's credentials pinning the plugin.
// Those are the credentials the running fixture already uses, so the drain is
// observed through the same material a real exchange would use.
type testLoad struct {
	contract         infrastructure.HTTPContract
	clock            interfaces.Clock
	serverProvider   infrastructure.CredentialsProvider
	serverPeer       models.PeerIdentity
	serverRevocation interfaces.RevocationSource
	clientProvider   infrastructure.CredentialsProvider
	clientPeer       models.PeerIdentity
	clientRevocation interfaces.RevocationSource

	mutex       sync.Mutex
	started     bool
	address     string
	server      *infrastructure.MutualTLSServer
	client      *infrastructure.MutualTLSClient
	entered     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
	inFlight    int
	served      int
}

// testDrainReport is what a harness learns about one drain. It carries no part of
// the served document, only its length and digest, so a harness can verify the
// response it did not read for itself.
type testDrainReport struct {
	inFlight            int
	served              int
	elapsedMilliseconds int64
	afterShutdown       string
	bodyBytes           int
	bodySHA256          string
	status              int
	closed              bool
	failure             string
}

// drainBound is the wall clock a drain is given. It comes from the contract's own
// shutdown grace rather than from a number chosen for a test.
func (load *testLoad) drainBound() time.Duration {
	return testSeconds(load.contract.Deadlines.PluginShutdownGraceSeconds) / testLoadDrainGraceDivisor
}

// start builds the loaded surface: a production mutual-TLS listener served by a
// handler that holds each request until the drain releases it, plus a production
// mutual-TLS client that reads what the listener answers.
func (load *testLoad) start() (string, error) {
	load.mutex.Lock()
	defer load.mutex.Unlock()
	if load.started {
		return load.address, nil
	}
	load.entered = make(chan struct{}, 8)
	load.release = make(chan struct{})
	load.releaseOnce = sync.Once{}
	server, err := infrastructure.NewMutualTLSServer(load.contract, infrastructure.MutualTLSServerConfig{
		Handler:    http.HandlerFunc(load.handle),
		Provider:   load.serverProvider,
		Peer:       load.serverPeer,
		Revocation: load.serverRevocation,
		Clock:      load.clock,
	})
	if err != nil {
		return "", fmt.Errorf("%w: %v", errTestLoad, err)
	}
	// A shutdown releases the request being drained. Registering it with the server
	// that serves the request is what makes the ordering deterministic: the handler
	// cannot finish before the drain has begun, and the drain cannot finish before
	// the handler has.
	server.Server().RegisterOnShutdown(load.releaseRequests)
	listener, err := net.Listen("tcp", net.JoinHostPort(testRotationListenHost, "0"))
	if err != nil {
		return "", fmt.Errorf("%w: %v", errTestLoad, err)
	}
	client, err := load.dialer()
	if err != nil {
		_ = listener.Close()
		return "", fmt.Errorf("%w: %v", errTestLoad, err)
	}
	load.server = server
	load.client = client
	load.address = "https://" + listener.Addr().String()
	load.started = true
	go func() {
		_ = server.Serve(listener)
	}()
	return load.address, nil
}

// handle holds the request until the drain releases it, then answers with the
// fixed document.
func (load *testLoad) handle(w http.ResponseWriter, _ *http.Request) {
	load.mutex.Lock()
	load.inFlight++
	load.mutex.Unlock()
	select {
	case load.entered <- struct{}{}:
	default:
	}
	<-load.release
	load.mutex.Lock()
	load.inFlight--
	load.served++
	load.mutex.Unlock()
	w.Header().Set("Content-Type", testControlMediaType)
	w.Header().Set("Content-Length", fmt.Sprint(len(testLoadDocument)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(testLoadDocument))
}

// dialer builds the production client that stands in for the Core replica on the
// load surface. The pinned peer is the plugin's, because the load surface plays
// the plugin side of the contract.
func (load *testLoad) dialer() (*infrastructure.MutualTLSClient, error) {
	return infrastructure.NewMutualTLSClient(load.contract, load.clientProvider,
		infrastructure.MutualTLSClientConfig{
			Peer:       load.clientPeer,
			ServerName: testServerName,
			Revocation: load.clientRevocation,
			Clock:      load.clock,
		})
}

// shutdown puts a request in flight, drains the listener within the contract's own
// grace, and reports what the production client actually received. It reports the
// in-flight count it observed as well, so a drain that answered nothing because
// nothing was in flight cannot be mistaken for a drain that delivered.
func (load *testLoad) shutdown() testDrainReport {
	load.mutex.Lock()
	if !load.started {
		load.mutex.Unlock()
		return testDrainReport{failure: errTestLoad.Error()}
	}
	// A pooled connection from an earlier request would hide the drain, so the
	// drain's own request is guaranteed a fresh connection.
	load.client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, load.address, nil)
	if err != nil {
		load.mutex.Unlock()
		return testDrainReport{failure: errTestLoad.Error()}
	}
	request.Header.Set("Connection", "close")
	answers := make(chan testLoadAnswer, 1)
	go func() {
		answers <- load.read(request)
	}()
	load.mutex.Unlock()

	// The request is in flight before the drain starts, so what the drain has to
	// finish is real work rather than an idle connection.
	report := testDrainReport{}
	entered := false
	select {
	case <-load.entered:
		entered = true
	case <-time.After(testLoadEnteredTolerance):
	}
	load.mutex.Lock()
	report.inFlight = load.inFlight
	load.mutex.Unlock()
	if !entered {
		report.failure = "the request never reached the handler"
	}

	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), load.drainBound())
	if report.failure == "" && load.server.GracefulShutdown(ctx) != nil {
		// The production error deliberately carries no transport text.
		report.failure = "the drain did not finish within the grace the contract allows"
	}
	cancel()
	report.elapsedMilliseconds = time.Since(started).Milliseconds()
	report.closed = report.failure == ""

	answer := <-answers
	load.mutex.Lock()
	report.served = load.served
	load.mutex.Unlock()
	if report.failure == "" && answer.err != nil {
		report.failure = "the drained response was not delivered whole"
	}
	report.status = answer.status
	report.bodyBytes = len(answer.body)
	digest := sha256.Sum256(answer.body)
	report.bodySHA256 = hex.EncodeToString(digest[:])

	// A drained surface has to stop answering on the address it was serving, so one
	// more request is made against it. Anything other than a refusal means the
	// listener is still there.
	report.afterShutdown = load.probe()

	load.mutex.Lock()
	load.client.CloseIdleConnections()
	load.started = false
	load.server = nil
	load.client = nil
	load.address = ""
	load.mutex.Unlock()
	return report
}

// testLoadAnswer is what one request on the load surface produced.
type testLoadAnswer struct {
	status int
	body   []byte
	err    error
}

// read makes one request through the production client and reads the response
// within the fixture's read bound.
func (load *testLoad) read(request *http.Request) testLoadAnswer {
	response, err := load.client.Do(request)
	if err != nil {
		return testLoadAnswer{err: err}
	}
	defer response.Body.Close()
	body, readErr := readTestAnswer(response.Body, testLoadReadLimit)
	return testLoadAnswer{status: response.StatusCode, body: body, err: readErr}
}

// probe asks a drained surface for a document and reports whether it was refused
// or answered.
func (load *testLoad) probe() string {
	load.mutex.Lock()
	address := load.address
	load.mutex.Unlock()
	if address == "" {
		return "refused"
	}
	client, err := load.dialer()
	if err != nil {
		return "refused"
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), testLoadEnteredTolerance)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "refused"
	}
	if _, probeErr := client.Do(request); probeErr != nil {
		return "refused"
	}
	return "answered"
}

// discard tears the loaded surface down without reporting a drain. A harness that
// asked for one and the fixture shutting down anyway both end here, so a load
// listener is never left bound after the scenario that started it is over.
func (load *testLoad) discard() {
	load.mutex.Lock()
	server := load.server
	client := load.client
	load.started = false
	load.server = nil
	load.client = nil
	load.address = ""
	load.mutex.Unlock()
	// A request the handler is still holding is released first, because a drain
	// that can never finish would hold the shutdown path for the whole grace.
	load.releaseRequests()
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), load.drainBound())
	defer cancel()
	_ = server.GracefulShutdown(ctx)
	if client != nil {
		client.CloseIdleConnections()
	}
}

// releaseRequests lets every held request answer. The drain and the teardown can
// both reach it, so it happens exactly once: a second close would panic the
// fixture and take the test with it.
func (load *testLoad) releaseRequests() {
	load.releaseOnce.Do(load.releaseHeld)
}

func (load *testLoad) releaseHeld() {
	load.mutex.Lock()
	release := load.release
	load.release = nil
	load.mutex.Unlock()
	if release != nil {
		close(release)
	}
}

// readTestAnswer reads a response within a bound, reporting an oversized read
// instead of treating it as a complete document. A short read is not detectable
// here, which is why the caller also compares the length and the digest the served
// document is known to have.
func readTestAnswer(body io.Reader, limit int) ([]byte, error) {
	contents, err := io.ReadAll(io.LimitReader(body, int64(limit)+1))
	if err != nil {
		return contents, err
	}
	if len(contents) > limit {
		return contents, errTestLoadBig
	}
	return contents, nil
}
