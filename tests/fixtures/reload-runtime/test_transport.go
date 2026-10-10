package main

// Fixture-only transport faults a harness can ask the running plugin surface for.
//
// This file is a test fixture and is never part of a production package. It can
// take a request apart after the listener has already accepted it, and it counts
// what the listener's own connection bookkeeping does with a connection that was
// taken away from it. Nothing here relaxes a TLS or peer check: the faults
// replace a connection after the handshake has already succeeded, so everything
// that is being measured is still measured through the production adapters.

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Liapoldus/plugin-sdk/v2/domain/interfaces"
	"github.com/Liapoldus/plugin-sdk/v2/infrastructure"
	"github.com/Liapoldus/plugin-sdk/v2/tests/support/process"
)

// testScenarioInputs is the read-only state the on-demand scenarios need in order
// to build their own mutual-TLS surfaces. The two providers and the two
// revocation sources are the ones the running fixture already uses, so a scenario
// is observed through the same credentials a real peer would present rather than
// through a second, easier set.
type testScenarioInputs struct {
	contract         infrastructure.HTTPContract
	clock            interfaces.Clock
	identities       *testIdentities
	coreProvider     infrastructure.CredentialsProvider
	pluginProvider   infrastructure.CredentialsProvider
	coreRevocation   interfaces.RevocationSource
	pluginRevocation interfaces.RevocationSource
	drops            *testDrops
	connections      *testConnections
	// pluginAddress and readyPath are the live plugin mutual-TLS listener and one
	// route on it. The close race is measured against that listener rather than
	// against a surface of its own, so what it reports is the state of the surface
	// a Core actually talks to.
	pluginAddress string
	readyPath     string
}

// testScenarios owns the surfaces a harness starts on demand: the rotating
// credential surface, the loaded surface that is drained and the close-race churn
// against the running plugin listener. The surfaces are created the first time they
// are asked for and exist only while a harness is using them.
type testScenarios struct {
	inputs testScenarioInputs

	mutex    sync.Mutex
	rotation *testRotation
	load     *testLoad
	churn    *testChurn
}

func newTestScenarios(inputs testScenarioInputs) *testScenarios {
	return &testScenarios{inputs: inputs}
}

// rotationSurface returns the rotating surface, starting it if this is the first
// time it has been asked for.
func (scenarios *testScenarios) rotationSurface() (*testRotation, error) {
	scenarios.mutex.Lock()
	defer scenarios.mutex.Unlock()
	if scenarios.rotation == nil {
		scenarios.rotation = &testRotation{
			contract:   scenarios.inputs.contract,
			identities: scenarios.inputs.identities,
		}
	}
	return scenarios.rotation, nil
}

// loadSurface returns the loaded surface, starting it if this is the first time it
// has been asked for.
func (scenarios *testScenarios) loadSurface() (*testLoad, error) {
	scenarios.mutex.Lock()
	defer scenarios.mutex.Unlock()
	if scenarios.load == nil {
		scenarios.load = &testLoad{
			contract:         scenarios.inputs.contract,
			clock:            scenarios.inputs.clock,
			serverProvider:   scenarios.inputs.pluginProvider,
			serverPeer:       scenarios.inputs.identities.corePeer,
			serverRevocation: scenarios.inputs.pluginRevocation,
			clientProvider:   scenarios.inputs.coreProvider,
			clientPeer:       scenarios.inputs.identities.pluginPeer,
			clientRevocation: scenarios.inputs.coreRevocation,
		}
	}
	return scenarios.load, nil
}

// churnScenario returns the close-race churn, which needs no listener of its own:
// it is measured against the plugin surface the fixture is already serving.
func (scenarios *testScenarios) churnScenario() (*testChurn, error) {
	scenarios.mutex.Lock()
	defer scenarios.mutex.Unlock()
	if scenarios.churn == nil {
		client, err := infrastructure.NewMutualTLSClient(scenarios.inputs.contract,
			scenarios.inputs.coreProvider,
			infrastructure.MutualTLSClientConfig{
				// The churn plays the Core replica, because the surface it is
				// measured against pins the Core replica as its only peer.
				Peer:       scenarios.inputs.identities.pluginPeer,
				ServerName: testServerName,
				Revocation: scenarios.inputs.coreRevocation,
				Clock:      scenarios.inputs.clock,
			})
		if err != nil {
			return nil, err
		}
		scenarios.churn = &testChurn{
			contract:    scenarios.inputs.contract,
			clock:       scenarios.inputs.clock,
			address:     scenarios.inputs.pluginAddress,
			readyPath:   scenarios.inputs.readyPath,
			client:      client,
			connections: scenarios.inputs.connections,
		}
	}
	return scenarios.churn, nil
}

// shutdown tears down whatever a harness left running, so the process never exits
// with a scenario listener or a scenario directory still open.
func (scenarios *testScenarios) shutdown() {
	scenarios.mutex.Lock()
	rotation, load := scenarios.rotation, scenarios.load
	scenarios.mutex.Unlock()
	if rotation != nil {
		_, _, err := rotation.stop(context.Background())
		process.Must(err)
	}
	if load != nil {
		load.discard()
	}
}

// testDrops is the switch behind the control operation that disconnects a
// request mid-flight. It wraps the production handler chain, so a dropped
// request is one the listener really accepted, on a connection that really
// completed a mutual-TLS handshake.
type testDrops struct {
	inner http.Handler

	mutex     sync.Mutex
	remaining int
	dropped   int
}

func newTestDrops(inner http.Handler) *testDrops {
	return &testDrops{inner: inner}
}

// arm asks for exactly count further drops. A count of zero disarms the switch
// and reports what actually landed, so a caller can tell an injection that
// happened from one it merely asked for.
func (drops *testDrops) arm(count int) (armed int, dropped int) {
	drops.mutex.Lock()
	defer drops.mutex.Unlock()
	if count > 0 {
		drops.remaining = count
	}
	armed = drops.remaining
	dropped = drops.dropped
	return armed, dropped
}

func (drops *testDrops) remainingDrops() int {
	drops.mutex.Lock()
	defer drops.mutex.Unlock()
	return drops.remaining
}

func (drops *testDrops) countDrop() {
	drops.mutex.Lock()
	defer drops.mutex.Unlock()
	if drops.remaining > 0 {
		drops.remaining--
	}
	drops.dropped++
}

// serve drops the next armed request, and otherwise passes the request to the
// production handler untouched.
func (drops *testDrops) serve(w http.ResponseWriter, r *http.Request) {
	if drops.remainingDrops() == 0 {
		drops.inner.ServeHTTP(w, r)
		return
	}
	drops.countDrop()
	if !testDropConnection(w) {
		// A hijack the platform refused would leave the request to be answered
		// normally, which would make this drop a lie. Answering the real handler
		// keeps the surface working; the reported count of drops still falls short
		// of what was asked for, so a harness can see it.
		drops.inner.ServeHTTP(w, r)
	}
}

// ServeHTTP is how the switch becomes part of the plugin's handler chain. It is
// the production handler underneath, so a request the switch passes is handled by
// exactly the code a surface without a switch would have used.
func (drops *testDrops) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	drops.serve(w, r)
}

// testDropConnection takes the connection the request arrived on out of the
// server's hands and closes it, without writing a response. Hijacking is what a
// real abrupt disconnect looks like to net/http: the connection leaves the
// server's active set and is closed by the hijacker, and the client sees a
// transport failure rather than a status code.
func testDropConnection(w http.ResponseWriter) bool {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return false
	}
	connection, _, err := hijacker.Hijack()
	if err != nil {
		return false
	}
	if connection != nil {
		process.Close(connection)
	}
	return true
}

// testConnections counts the connections of one listener through the state the
// production server already tracks. An injected drop removes its connection from
// that bookkeeping, so a connection that is still counted as open after a drop
// would be a connection the listener believes it still owns.
type testConnections struct {
	mutex    sync.Mutex
	opened   int64
	open     int64
	hijacked int64
}

// state is the snapshot a harness reads. It is only stable once the listener has
// gone quiet, which is why every read settles first.
type testConnectionState struct {
	opened   int64
	open     int64
	hijacked int64
}

// hook reports the net/http connection states that matter here. A connection is
// counted as opened when the listener accepts it, and stops being counted as open
// both when it is closed and when it is hijacked: net/http hands a hijacked
// connection to the caller and takes it out of its own bookkeeping at that point,
// so treating hijack as still-open would report a connection the server has
// already let go of.
func (connections *testConnections) hook(_ net.Conn, state http.ConnState) {
	connections.mutex.Lock()
	defer connections.mutex.Unlock()
	switch state {
	case http.StateNew:
		connections.opened++
		connections.open++
	case http.StateHijacked:
		connections.hijacked++
		connections.open--
	case http.StateClosed:
		connections.open--
	}
}

func (connections *testConnections) snapshot() testConnectionState {
	connections.mutex.Lock()
	defer connections.mutex.Unlock()
	return testConnectionState{
		opened:   connections.opened,
		open:     connections.open,
		hijacked: connections.hijacked,
	}
}

// settle waits until the open count has stopped moving, or the limit passes. It
// polls on a short interval rather than sleeping for a fixed time, so a listener
// that is already quiet costs almost nothing and a listener that is still
// settling is given a bounded chance to finish.
func (connections *testConnections) settle(limit time.Duration) testConnectionState {
	deadline := time.Now().Add(limit)
	previous := connections.snapshot().open
	unchanged := 0
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
		current := connections.snapshot().open
		if current != previous {
			previous = current
			unchanged = 0
			continue
		}
		unchanged++
		if unchanged >= 5 {
			break
		}
	}
	return connections.snapshot()
}
