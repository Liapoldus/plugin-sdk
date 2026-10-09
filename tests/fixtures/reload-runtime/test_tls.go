package main

// Fixture-only PKI for the reload runtime.
//
// This file is a test fixture and is never part of a production package. It
// generates one throwaway authority, one Core replica keypair, one plugin
// replica keypair and one empty certificate revocation list on every start, so
// the fixture never reads key material from a file, an environment variable or a
// process argument. Nothing here is trusted outside this process and nothing
// here is persisted.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"time"

	"github.com/Liapoldus/plugin-sdk/domain/interfaces"
	"github.com/Liapoldus/plugin-sdk/domain/models"
	"github.com/Liapoldus/plugin-sdk/infrastructure"
)

var errTestPKI = errors.New("test PKI generation failed")

const (
	// testCoreCommonName and testPluginCommonName are the per-replica identities
	// the two roles of this fixture present. They are fixture inputs to the
	// production peer authorizer, not contract values.
	testCoreCommonName   = "liapoldus-core-replica"
	testPluginCommonName = "liapoldus-plugin-replica"
	// testServerName is the name the fixture's own leaf certificates carry, so a
	// dial address of 127.0.0.1 still passes the contract hostname check.
	testServerName = "localhost"

	testCertificateValidity = 24 * time.Hour
	testRevocationBackdate  = time.Hour
)

// testClock is the fixture's system clock. The SDK reads time only through the
// domain Clock port and never calls a wall clock itself, so the fixture supplies
// one rather than reaching for a time source the domain does not expose.
type testClock struct{}

func (testClock) Now() time.Time { return time.Now() }

// testIdentities holds the two roles' credential material, the two pinned peer
// identities the mutual-TLS adapters are configured with, the credentials the
// startup probes offer to them, and the authority that issues every leaf here.
type testIdentities struct {
	corePeer     models.PeerIdentity
	pluginPeer   models.PeerIdentity
	core         infrastructure.CredentialsMaterial
	plugin       infrastructure.CredentialsMaterial
	revoked      infrastructure.CredentialsMaterial
	impostor     infrastructure.CredentialsMaterial
	expired      infrastructure.CredentialsMaterial
	revocation   []byte
	authority    *x509.Certificate
	authorityKey *ecdsa.PrivateKey
}

const (
	// testRevokedSerial is the serial number of the one certificate this fixture
	// revokes. It is fixed rather than allocated, so a harness never has to read
	// the revocation list to learn which credential is the revoked one.
	testRevokedSerial = 6
	// testImpostorSerial and testExpiredSerial are the serials of the two
	// credentials that are neither revoked nor untrusted. One is in date and
	// issued by the authority the listener trusts, but presents a different
	// replica identity; the other is the registered identity with a validity
	// window that has already closed. Neither serial appears in the revocation
	// list, so a refusal of either one is evidence about identity and about
	// validity specifically.
	testImpostorSerial = 7
	testExpiredSerial  = 8
	// testRotationInitialSerial and testRotationNextSerial are the serials the
	// credential-rotation surface serves before and after the operator writes new
	// material to disk. They are fixed so a harness can see which certificate the
	// listener actually presented rather than believing its own account of itself.
	testRotationInitialSerial = 9
	testRotationNextSerial    = 10
	// testImpostorCommonName and testImpostorURISuffix are the wrong identity the
	// impostor presents. The suffix is appended to the contract's own trust-domain
	// prefix, so the impostor's URI satisfies the contract's prefix rule and can
	// only ever be refused by exact comparison. Both the common name and the URI
	// are wrong on purpose: the SDK accepts a peer that matches either one.
	testImpostorCommonName = testCoreCommonName + "-impostor"
	testImpostorURISuffix  = "replica-impostor"
)

// newTestIdentities builds the whole PKI for one fixture run.
//
// The Core replica identity carries exactly one URI, taken from the contract's
// own trust-domain prefix, so the production peer authorizer can apply its URI
// rule without the fixture spelling a trust domain of its own. The plugin
// identity deliberately carries no URI: the contract registers a Core replica
// prefix, and a plugin URI would have to either claim that prefix or fail the
// authorizer, so the plugin is identified by its unique common name alone.
func newTestIdentities(contract infrastructure.HTTPContract) (*testIdentities, error) {
	clock := testClock{}
	now := clock.Now()

	authorityKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errTestPKI, err)
	}
	authorityTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "liapoldus-plugin-sdk-fixture-authority"},
		NotBefore:             now.Add(-testRevocationBackdate),
		NotAfter:              now.Add(testCertificateValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
		SubjectKeyId:          []byte{0x4c, 0x50, 0x53, 0x44, 0x4b, 0x01},
	}
	authorityDER, err := x509.CreateCertificate(rand.Reader, authorityTemplate, authorityTemplate, &authorityKey.PublicKey, authorityKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errTestPKI, err)
	}
	authority, err := x509.ParseCertificate(authorityDER)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errTestPKI, err)
	}
	authorityPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: authorityDER})

	coreURI := contract.TransportSecurity.PeerIdentity.UniformResourceIdentifierPrefix + "replica-1"
	if _, err := url.Parse(coreURI); err != nil {
		return nil, fmt.Errorf("%w: core replica URI: %w", errTestPKI, err)
	}
	identities := &testIdentities{
		corePeer: models.PeerIdentity{
			CommonName:                testCoreCommonName,
			UniformResourceIdentifier: coreURI,
		},
		pluginPeer: models.PeerIdentity{CommonName: testPluginCommonName},
	}

	coreCoreURI, err := url.Parse(coreURI)
	if err != nil {
		return nil, fmt.Errorf("%w: core replica URI: %w", errTestPKI, err)
	}
	coreServer, err := testIssueLeaf(2, testCoreCommonName, []*url.URL{coreCoreURI},
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, now, authority, authorityKey)
	if err != nil {
		return nil, err
	}
	coreClient, err := testIssueLeaf(3, testCoreCommonName, []*url.URL{coreCoreURI},
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, now, authority, authorityKey)
	if err != nil {
		return nil, err
	}
	// The revoked credential is issued from the same authority, with the same
	// Core replica name and the same client-auth usage as the live one, and is
	// then listed in the revocation list below. Nothing about it is malformed or
	// untrusted, which is the point: revocation is the single reason it is
	// refused.
	revokedClient, err := testIssueLeaf(testRevokedSerial, testCoreCommonName, []*url.URL{coreCoreURI},
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, now, authority, authorityKey)
	if err != nil {
		return nil, err
	}
	// The impostor is issued by the same authority, with the same client-auth
	// usage and the same validity window as the live Core replica credential, and
	// its URI still starts with the contract's trust-domain prefix. Only its
	// replica identity is different, so a refusal can only be about identity.
	impostorURI, err := url.Parse(
		contract.TransportSecurity.PeerIdentity.UniformResourceIdentifierPrefix + testImpostorURISuffix)
	if err != nil {
		return nil, fmt.Errorf("%w: impostor URI: %w", errTestPKI, err)
	}
	impostorClient, err := testIssueLeaf(testImpostorSerial, testImpostorCommonName,
		[]*url.URL{impostorURI}, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, now,
		authority, authorityKey)
	if err != nil {
		return nil, err
	}
	// The expired peer is the registered Core replica identity, issued by the
	// trusted authority, with the same client-auth usage and a serial the
	// revocation list does not name. Its validity window closed a day ago, so the
	// only thing wrong with it is that it is no longer in date.
	expiredClient, err := testIssueLeafWindow(testExpiredSerial, testCoreCommonName,
		[]*url.URL{coreCoreURI}, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		now.Add(-2*testCertificateValidity), now.Add(-testCertificateValidity),
		authority, authorityKey)
	if err != nil {
		return nil, err
	}
	pluginServer, err := testIssueLeaf(4, testPluginCommonName, nil,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, now, authority, authorityKey)
	if err != nil {
		return nil, err
	}
	pluginClient, err := testIssueLeaf(5, testPluginCommonName, nil,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, now, authority, authorityKey)
	if err != nil {
		return nil, err
	}

	identities.core = infrastructure.CredentialsMaterial{
		CABundle:             authorityPEM,
		ServerCertificatePEM: coreServer.certificate,
		ServerKeyPEM:         coreServer.key,
		ClientCertificatePEM: coreClient.certificate,
		ClientKeyPEM:         coreClient.key,
	}
	identities.plugin = infrastructure.CredentialsMaterial{
		CABundle:             authorityPEM,
		ServerCertificatePEM: pluginServer.certificate,
		ServerKeyPEM:         pluginServer.key,
		ClientCertificatePEM: pluginClient.certificate,
		ClientKeyPEM:         pluginClient.key,
	}
	identities.revoked = infrastructure.CredentialsMaterial{
		CABundle:             authorityPEM,
		ClientCertificatePEM: revokedClient.certificate,
		ClientKeyPEM:         revokedClient.key,
	}
	identities.impostor = infrastructure.CredentialsMaterial{
		CABundle:             authorityPEM,
		ClientCertificatePEM: impostorClient.certificate,
		ClientKeyPEM:         impostorClient.key,
	}
	identities.expired = infrastructure.CredentialsMaterial{
		CABundle:             authorityPEM,
		ClientCertificatePEM: expiredClient.certificate,
		ClientKeyPEM:         expiredClient.key,
	}
	identities.authority = authority
	identities.authorityKey = authorityKey

	// The empty list is a real, CA-signed revocation list rather than an absent
	// one: the contract is fail-closed, and a source with no list at all refuses
	// every handshake instead of admitting the fixture's own peers.
	revocationDER, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{
		Number:     big.NewInt(1),
		ThisUpdate: now.Add(-testRevocationBackdate),
		NextUpdate: now.Add(testCertificateValidity),
		RevokedCertificateEntries: []x509.RevocationListEntry{{
			SerialNumber:   big.NewInt(testRevokedSerial),
			RevocationTime: now.Add(-testRevocationBackdate),
		}},
	}, authority, authorityKey)
	if err != nil {
		return nil, fmt.Errorf("%w: revocation list: %w", errTestPKI, err)
	}
	identities.revocation = pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: revocationDER})
	return identities, nil
}

// testLeaf is one generated keypair in PEM form.
type testLeaf struct {
	certificate []byte
	key         []byte
}

func testIssueLeaf(serial int64, commonName string, uris []*url.URL,
	usages []x509.ExtKeyUsage, now time.Time, authority *x509.Certificate,
	authorityKey *ecdsa.PrivateKey) (testLeaf, error) {
	return testIssueLeafWindow(serial, commonName, uris, usages,
		now.Add(-testRevocationBackdate), now.Add(testCertificateValidity), authority, authorityKey)
}

// testIssueLeafWindow issues one leaf with an explicit validity window, so a
// credential can be generated that is expired rather than merely unusual. Every
// other leaf in this fixture is issued through testIssueLeaf and gets the same
// in-date window.
func testIssueLeafWindow(serial int64, commonName string, uris []*url.URL,
	usages []x509.ExtKeyUsage, notBefore, notAfter time.Time, authority *x509.Certificate,
	authorityKey *ecdsa.PrivateKey) (testLeaf, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return testLeaf{}, fmt.Errorf("%w: %w", errTestPKI, err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  usages,
		URIs:         uris,
	}
	for _, usage := range usages {
		if usage == x509.ExtKeyUsageServerAuth {
			template.DNSNames = []string{testServerName}
			template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, authority, &key.PublicKey, authorityKey)
	if err != nil {
		return testLeaf{}, fmt.Errorf("%w: %w", errTestPKI, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return testLeaf{}, fmt.Errorf("%w: %w", errTestPKI, err)
	}
	return testLeaf{
		certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		key:         pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}, nil
}

// newTestRevocation builds the fail-closed revocation source both roles use. The
// authorities come from the loaded credentials, so the trust decision that
// admits a chain and the revocation decision that gates it are one decision.
func newTestRevocation(credentials *infrastructure.Credentials, list []byte,
	clock interfaces.Clock) (*infrastructure.Revocation, error) {
	authorities := credentials.TrustAuthorities()
	if len(authorities) == 0 {
		return nil, errTestPKI
	}
	return infrastructure.NewRevocation(infrastructure.RevocationConfiguration{
		Authorities: authorities,
		Bundles:     [][]byte{list},
		Policy:      infrastructure.RevocationFailClosed,
		Clock:       clock,
	})
}
