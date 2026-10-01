package main

// Fixture-only credential rotation, on a real file-backed provider.
//
// This file is a test fixture and is never part of a production package. It
// builds a second mutual-TLS listener from the production file credentials
// provider, so the rotation it performs is the rotation a plugin performs: the
// operator writes new material to disk, the provider re-reads it, and the
// listener serves the replacement certificate on the next handshake. The
// material lives in an owner-only directory that is removed when the scenario is
// torn down, and no key material is ever returned to a harness or written to a
// log.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Liapoldus/plugin-sdk/infrastructure"
)

// testRotation answers the two contract durations a rotation has to respect: how
// long a shutdown may wait for the listener to drain, and how long the fixture's
// own control listener may wait for one. They are the contract's own numbers
// rather than numbers chosen for a test, so a scenario that only works because it
// was given extra time cannot pass.
const (
	testRotationListenHost   = "127.0.0.1"
	testRotationFileMaterial = 0o600
	testRotationDirectory    = 0o700
)

// testRotationStopTimeout is how long the drain of a rotating listener may take.
// It is the contract's own shutdown grace and nothing more, so a listener that only
// closes by running out its grace fails instead of passing slowly.
func testRotationStopTimeout(contract infrastructure.HTTPContract) time.Duration {
	return testSeconds(contract.Deadlines.PluginShutdownGraceSeconds)
}

// testControlOperation is the trivial document the fixture's own scenario surfaces
// answer with. The rotation and the drain are proved by the certificate on the wire
// and by the bytes a client received, not by this body, so it exists only to give
// those surfaces something to answer.
const testControlOperation = `{"operation":"scenario"}`

var errTestRotation = errors.New("test credential rotation failed")

// testRotation owns the lifetime of the rotating listener and of the directory
// its material is written to.
type testRotation struct {
	contract   infrastructure.HTTPContract
	identities *testIdentities

	mutex     sync.Mutex
	started   bool
	closed    bool
	removed   bool
	directory string
	server    *infrastructure.MutualTLSServer
	provider  *infrastructure.FileCredentialsProvider
	address   string
	next      int64
}

// start writes the initial material, builds a production mutual-TLS server over
// the file credentials provider, and starts serving. It is deliberately lazy: the
// surface exists only while a harness is using it, so the rest of the fixture
// runs with exactly the listeners it always had.
func (rotation *testRotation) start() (string, error) {
	rotation.mutex.Lock()
	defer rotation.mutex.Unlock()
	if rotation.started {
		return rotation.address, nil
	}
	directory, err := os.MkdirTemp("", "plugin-sdk-rotation-")
	if err != nil {
		return "", fmt.Errorf("%w: %v", errTestRotation, err)
	}
	rotation.directory = directory
	if err := os.Chmod(directory, testRotationDirectory); err != nil {
		rotation.discard()
		return "", fmt.Errorf("%w: %v", errTestRotation, err)
	}
	if err := rotation.writeMaterial(testRotationInitialSerial); err != nil {
		rotation.discard()
		return "", err
	}
	provider, err := infrastructure.NewFileCredentialsProvider(rotation.contract,
		infrastructure.CredentialsMaterial{
			CAFile:                filepath.Join(directory, "ca.pem"),
			ServerCertificateFile: filepath.Join(directory, "server.pem"),
			ServerKeyFile:         filepath.Join(directory, "server.key"),
			ClientCertificateFile: filepath.Join(directory, "client.pem"),
			ClientKeyFile:         filepath.Join(directory, "client.key"),
		})
	if err != nil {
		rotation.discard()
		return "", fmt.Errorf("%w: %v", errTestRotation, err)
	}
	rotation.provider = provider
	// The provider is loaded once here so the revocation source is built from the
	// same authorities the provider trusts, rather than from a second, possibly
	// different, copy of the material.
	credentials, err := provider.Credentials()
	if err != nil {
		rotation.discard()
		return "", fmt.Errorf("%w: %v", errTestRotation, err)
	}
	revocation, err := newTestRevocation(credentials, rotation.identities.revocation, testClock{})
	if err != nil {
		rotation.discard()
		return "", fmt.Errorf("%w: %v", errTestRotation, err)
	}
	server, err := infrastructure.NewMutualTLSServer(rotation.contract, infrastructure.MutualTLSServerConfig{
		Handler:    http.HandlerFunc(testRotationHandler),
		Provider:   provider,
		Peer:       rotation.identities.pluginPeer,
		Revocation: revocation,
		Clock:      testClock{},
	})
	if err != nil {
		rotation.discard()
		return "", fmt.Errorf("%w: %v", errTestRotation, err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(testRotationListenHost, "0"))
	if err != nil {
		rotation.discard()
		return "", fmt.Errorf("%w: %v", errTestRotation, err)
	}
	rotation.server = server
	rotation.address = "https://" + listener.Addr().String()
	rotation.started = true
	rotation.next = testRotationNextSerial
	go func() {
		_ = server.Serve(listener)
	}()
	return rotation.address, nil
}

// rotate writes new material to disk and asks the listener to re-read it. It
// reports the serial the provider held and the serial the listener was serving,
// on both sides of the write, read back through the production accessors rather
// than from anything the fixture remembers about what it issued.
func (rotation *testRotation) rotate() (providerBefore, providerAfter, servedBefore, servedAfter string, err error) {
	rotation.mutex.Lock()
	defer rotation.mutex.Unlock()
	if !rotation.started || rotation.server == nil {
		return "", "", "", "", errTestRotation
	}
	providerBefore = testRotationProviderSerial(rotation.provider)
	servedBefore = testRotationServedSerial(rotation.server)
	if err := rotation.writeMaterial(rotation.next); err != nil {
		return providerBefore, providerBefore, servedBefore, servedBefore, err
	}
	if err := rotation.server.RefreshCredentials(); err != nil {
		return providerBefore, providerBefore, servedBefore, servedBefore,
			fmt.Errorf("%w: %v", errTestRotation, err)
	}
	rotation.next++
	providerAfter = testRotationProviderSerial(rotation.provider)
	servedAfter = testRotationServedSerial(rotation.server)
	return providerBefore, providerAfter, servedBefore, servedAfter, nil
}

// stop drains the listener under the contract's own grace, zeroizes the provider
// and removes the directory the material was written to. It reports what it
// managed to do, so a harness can tell a clean teardown from one that left
// material or a listening socket behind.
func (rotation *testRotation) stop() (closed bool, removed bool, err error) {
	rotation.mutex.Lock()
	defer rotation.mutex.Unlock()
	if !rotation.started {
		return rotation.closed, rotation.removed, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), testRotationStopTimeout(rotation.contract))
	defer cancel()
	if rotation.server != nil {
		if shutdownErr := rotation.server.GracefulShutdown(ctx); shutdownErr != nil {
			rotation.closed = false
			rotation.discardMaterial()
			return false, rotation.removed, fmt.Errorf("%w: %v", errTestRotation, shutdownErr)
		}
		rotation.closed = true
	}
	rotation.discardMaterial()
	rotation.started = false
	rotation.server = nil
	rotation.provider = nil
	rotation.address = ""
	return rotation.closed, rotation.removed, nil
}

// writeMaterial replaces the whole set of files the provider watches, so a
// rotation is a rotation of every input rather than of one of them.
func (rotation *testRotation) writeMaterial(serial int64) error {
	leaf, err := testIssueLeaf(serial, testPluginCommonName, nil,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, testClock{}.Now(),
		rotation.identities.authority, rotation.identities.authorityKey)
	if err != nil {
		return err
	}
	files := []struct {
		name    string
		content []byte
	}{
		{"ca.pem", rotation.identities.plugin.CABundle},
		{"server.pem", leaf.certificate},
		{"server.key", leaf.key},
		// The rotating surface is only ever dialled by this fixture and by a
		// harness that already holds the live plugin client keypair, so the client
		// leg of the material is a copy of that live pair rather than a third
		// keypair nobody uses.
		{"client.pem", rotation.identities.plugin.ClientCertificatePEM},
		{"client.key", rotation.identities.plugin.ClientKeyPEM},
	}
	for _, file := range files {
		if err := os.WriteFile(filepath.Join(rotation.directory, file.name), file.content,
			testRotationFileMaterial); err != nil {
			return fmt.Errorf("%w: %v", errTestRotation, err)
		}
	}
	return nil
}

func (rotation *testRotation) discardMaterial() {
	if rotation.provider != nil {
		rotation.provider.Zeroize()
	}
	if rotation.directory == "" {
		return
	}
	if err := os.RemoveAll(rotation.directory); err == nil {
		rotation.removed = true
	}
	rotation.directory = ""
}

func (rotation *testRotation) discard() {
	rotation.discardMaterial()
	rotation.started = false
	rotation.removed = false
}

// testRotationProviderSerial reports what the file credentials provider currently
// holds after re-reading its inputs.
func testRotationProviderSerial(provider *infrastructure.FileCredentialsProvider) string {
	if provider == nil {
		return ""
	}
	credentials, err := provider.Credentials()
	if err != nil || credentials == nil {
		return ""
	}
	serial, _ := credentials.ServerCertificate()
	return testRotationSerialText(serial)
}

// testRotationServedSerial reports what the listener's published configuration
// would offer the next handshake.
func testRotationServedSerial(server *infrastructure.MutualTLSServer) string {
	if server == nil {
		return ""
	}
	configuration := server.TLSConfig()
	if configuration == nil {
		return ""
	}
	for _, certificate := range configuration.Certificates {
		if certificate.Leaf != nil {
			return testRotationSerialText(certificate)
		}
	}
	return ""
}

// testRotationSerialText renders a serial the way a TLS peer reports it on the
// wire: uppercase hex with no leading zeros. The harness compares this against the
// serial it reads off a real handshake, so the fixture and the observation have to
// speak one notation. Decimal would be just as accurate and just as impossible to
// compare, because a serial is an ASN.1 INTEGER and every reader prints it in hex.
func testRotationSerialText(certificate tls.Certificate) string {
	if certificate.Leaf == nil {
		return ""
	}
	return strings.ToUpper(certificate.Leaf.SerialNumber.Text(16))
}

// testRotationHandler is the trivial response the rotating surface serves. What
// the rotation is proved by is the certificate the handshake presented, not what
// this says, so the body exists only to give the surface something to answer.
func testRotationHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", testControlMediaType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(testControlOperation))
}
