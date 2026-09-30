package main

// Fixture-only close-race churn against the plugin's own live mutual-TLS listener.
//
// This file is a test fixture and is never part of a production package. It asks a
// question the rest of the fixture never asks: what does a serving listener do with
// a client that completes a handshake, writes a request and then goes away without
// reading the answer? A harness cannot ask it from the outside, because the peer
// credentials on that surface belong to the Core replica and are never published,
// so the fixture asks it from the inside with the same credentials a real Core
// would present.
//
// Nothing here is a shortcut past a check. Every abandoned connection is dialled
// with the production client configuration, so it completes the same handshake, the
// same revocation check and the same pinned-peer check as any other call, and the
// request written onto it is a real contract request. The point of closing early is
// to race the listener's read of a request that will never be answered.

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"liapoldus.local/plugin-sdk/domain/interfaces"
	"liapoldus.local/plugin-sdk/infrastructure"
)

// testChurnAttempts is how many connections are abandoned mid-request. It is large
// enough that a close landing inside the listener's read is a near-certainty
// rather than a coincidence, and small enough that the scenario stays a test rather
// than a load test.
const testChurnAttempts = 64

// testChurnSettleDivisor divides the contract's own shutdown grace to bound how
// long the scenario waits for the listener's connection bookkeeping to come to
// rest. A listener still holding connections after a churn of this size fails
// instead of being reported clean late.
const testChurnSettleDivisor = 4

// testChurnReadLimit bounds the readiness document read back around the churn, so a
// surface answering with something enormous is reported rather than buffered.
const testChurnReadLimit = 1 << 20

var errTestChurn = errors.New("test close-race churn failed")

// testChurnReport is what a harness learns about one churn. It reports the answer
// the listener gave before the churn and the answer it gave after, so a harness can
// compare two real responses without having read either of them itself, and the
// listener's own connection bookkeeping, so a connection the churn left behind is
// visible.
type testChurnReport struct {
	requested  int
	handshakes int
	refused    int

	statusBefore int
	bytesBefore  int
	sha256Before string
	statusAfter  int
	bytesAfter   int
	sha256After  string

	openedBefore  int64
	openedAfter   int64
	open          int64
	hijackedAfter int64

	failure string
}

// testChurn drives one close race against the running plugin surface. It is not
// stateful between runs: everything it reports is read from the listener.
type testChurn struct {
	contract    infrastructure.HTTPContract
	clock       interfaces.Clock
	address     string
	readyPath   string
	client      *infrastructure.MutualTLSClient
	connections *testConnections
}

// testChurnAnswer is one response read through the production client.
type testChurnAnswer struct {
	status int
	body   []byte
}

// run makes the abandoned connections, then reads the listener twice, once before
// and once after, through the production client. Two real answers and not one is
// the point: a listener that recovers into a state which answers differently, or
// not at all, has to show it here.
func (churn *testChurn) run() testChurnReport {
	report := testChurnReport{requested: testChurnAttempts}
	report.openedBefore = churn.connections.snapshot().opened

	before, err := churn.readiness()
	if err != nil {
		report.failure = "the listener did not answer before the churn"
		return report
	}
	report.statusBefore, report.bytesBefore = before.status, len(before.body)
	report.sha256Before = testChurnDigest(before.body)

	for attempt := 0; attempt < testChurnAttempts; attempt++ {
		if abandonErr := churn.abandon(); abandonErr != nil {
			report.refused++
			continue
		}
		report.handshakes++
	}

	// A pooled connection would hide a listener that had stopped accepting, so the
	// request after the churn is guaranteed a connection of its own.
	churn.client.CloseIdleConnections()
	after, err := churn.readiness()
	if err != nil {
		report.failure = "the listener did not answer after the churn"
		return report
	}
	report.statusAfter, report.bytesAfter = after.status, len(after.body)
	report.sha256After = testChurnDigest(after.body)

	settled := churn.connections.settle(churn.settleBound())
	report.openedAfter, report.open, report.hijackedAfter = settled.opened, settled.open, settled.hijacked
	return report
}

func (churn *testChurn) settleBound() time.Duration {
	return testSeconds(churn.contract.Deadlines.PluginShutdownGraceSeconds) / testChurnSettleDivisor
}

// abandon completes one handshake, writes one real request onto the connection and
// closes without reading the answer. The bytes are already written when the close
// happens, so the listener runs its whole request path against a client that has
// gone away, which is the race being measured.
func (churn *testChurn) abandon() error {
	target, err := churn.target()
	if err != nil {
		return err
	}
	dialer := &net.Dialer{Timeout: testSeconds(churn.contract.Deadlines.ClientDialSeconds)}
	connection, err := tls.DialWithDialer(dialer, "tcp", target, churn.client.TLSConfig())
	if err != nil {
		return err
	}
	// The close is the point of the exercise, so it is deferred rather than
	// conditional: a failed write still leaves a connection that went away.
	defer connection.Close()
	if _, err := connection.Write([]byte(churn.rawRequest())); err != nil {
		return err
	}
	_ = connection.SetDeadline(time.Now().Add(testSeconds(churn.contract.Deadlines.PluginWriteSeconds)))
	return nil
}

// readiness makes one real call through the production client and reads the answer
// within the fixture's bound.
func (churn *testChurn) readiness() (testChurnAnswer, error) {
	ctx, cancel := context.WithTimeout(context.Background(),
		testSeconds(churn.contract.Deadlines.PluginReadSeconds))
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, churn.address+churn.readyPath, nil)
	if err != nil {
		return testChurnAnswer{}, err
	}
	// Its own connection, so the answer cannot come from a pooled one.
	request.Close = true
	response, err := churn.client.Do(request)
	if err != nil {
		return testChurnAnswer{}, err
	}
	defer response.Body.Close()
	body, err := readTestAnswer(response.Body, testChurnReadLimit)
	if err != nil {
		return testChurnAnswer{}, err
	}
	return testChurnAnswer{status: response.StatusCode, body: body}, nil
}

// rawRequest is the request an abandoned connection carries. It is a real request
// for a contract path with real headers, not a bare write, so the listener parses
// and routes it exactly as it would any other.
func (churn *testChurn) rawRequest() string {
	return strings.Join([]string{
		http.MethodGet + " " + churn.readyPath + " HTTP/1.1",
		"Host: " + testServerName,
		"Connection: close",
		"", "",
	}, "\r\n")
}

// target is the host:port the abandoned connections dial, read from the published
// address so the fixture and the harness are looking at the same listener.
func (churn *testChurn) target() (string, error) {
	parsed, err := url.Parse(churn.address)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errTestChurn, err)
	}
	return net.JoinHostPort(parsed.Hostname(), parsed.Port()), nil
}

func testChurnDigest(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}
