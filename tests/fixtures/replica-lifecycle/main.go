package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/Liapoldus/plugin-sdk/tests/support/process"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/Liapoldus/plugin-sdk/domain/interfaces"
	"github.com/Liapoldus/plugin-sdk/domain/models"
	"github.com/Liapoldus/plugin-sdk/infrastructure"
)

var fixtureEpoch = time.Date(2026, time.October, 6, 10, 0, 0, 0, time.UTC)

type fixtureClock struct {
	mutex sync.RWMutex
	now   time.Time
}

func (clock *fixtureClock) Now() time.Time {
	clock.mutex.RLock()
	defer clock.mutex.RUnlock()
	return clock.now
}

func (clock *fixtureClock) Set(now time.Time) {
	clock.mutex.Lock()
	clock.now = now
	clock.mutex.Unlock()
}

type fixtureRevocation struct{}

func (fixtureRevocation) Revoked([]byte) (bool, error) { return false, nil }

type keypair struct {
	certificatePEM []byte
	privateKeyPEM  []byte
}

type fixtureAuthority struct {
	certificate    *x509.Certificate
	privateKey     *ecdsa.PrivateKey
	certificatePEM []byte
}

type registrationRecord struct {
	request models.ReplicaRegistrationRequest
	expires time.Time
	active  bool
}

type fakeCore struct {
	mutex            sync.Mutex
	directoryMutex   sync.Mutex
	contract         infrastructure.ReplicaLifecycleContract
	clock            *fixtureClock
	records          map[string]registrationRecord
	directory        []byte
	directoryETag    string
	directoryChanged chan struct{}
	handler          http.Handler
}

func main() {
	result, err := run()
	if err != nil {
		write(map[string]string{"failure": "replica lifecycle fixture failed"})
		os.Exit(1)
	}
	write(result)
}

func run() (map[string]any, error) {
	httpContract, err := infrastructure.LoadHTTPContract()
	if err != nil {
		return nil, err
	}
	replicaContract, err := infrastructure.LoadReplicaLifecycleContract()
	if err != nil {
		return nil, err
	}
	clock := &fixtureClock{now: fixtureEpoch}
	authority, err := newAuthority()
	if err != nil {
		return nil, err
	}
	coreIdentity := models.PeerIdentity{CommonName: "fixture-core", UniformResourceIdentifier: "spiffe://liapoldus/core/v2-fixture"}
	corePair, err := authority.issue("fixture-core", coreIdentity.UniformResourceIdentifier, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, []string{"127.0.0.1"})
	if err != nil {
		return nil, err
	}
	coreCertificate, err := tls.X509KeyPair(corePair.certificatePEM, corePair.privateKeyPEM)
	if err != nil {
		return nil, err
	}
	coreCertificate.Leaf, err = x509.ParseCertificate(coreCertificate.Certificate[0])
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(authority.certificate)
	core := &fakeCore{contract: replicaContract, clock: clock, records: make(map[string]registrationRecord), directoryChanged: make(chan struct{})}
	core.handler = http.HandlerFunc(core.handle)
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	server := &http.Server{
		Handler:           core.handler,
		ReadHeaderTimeout: time.Second,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    pool,
			Certificates: []tls.Certificate{coreCertificate},
		},
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(tls.NewListener(listener, server.TLSConfig)) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		process.Must(server.Shutdown(ctx))
		<-serveDone
	}()
	baseURL := "https://" + listener.Addr().String()
	clientAIdentity := models.PeerReplicaID{InstanceID: "forms-db", ReplicaID: "forms-1", IncarnationID: "incarnation-a", PlacementID: "node-a"}
	clientBIdentity := models.PeerReplicaID{InstanceID: "forms-db", ReplicaID: "forms-1", IncarnationID: "incarnation-b", PlacementID: "node-a"}
	clientA, transportA, err := newRegistrationClient(httpContract, replicaContract, authority, corePair, baseURL, coreIdentity, clientAIdentity)
	if err != nil {
		return nil, err
	}
	defer transportA.CloseIdleConnections()
	requestA := registrationRequest(replicaContract.ContractVersion, clientAIdentity)
	registered, err := clientA.Register(context.Background(), requestA)
	if err != nil {
		return nil, err
	}
	clock.Set(fixtureEpoch.Add(10 * time.Second))
	renewed, err := clientA.Renew(context.Background(), models.ReplicaRenewalRequest{
		ContractVersion: replicaContract.ContractVersion,
		Identity:        clientAIdentity, AppliedGeneration: "config-7", Ready: true,
	})
	if err != nil {
		return nil, err
	}
	pollContract, err := infrastructure.LoadPeerDirectoryPollContract()
	if err != nil {
		return nil, err
	}
	directoryClient, err := infrastructure.NewPeerDirectoryClient(pollContract, baseURL, transportA, clientAIdentity)
	if err != nil {
		return nil, err
	}
	core.publishDirectory(fixturePeerDirectory(clientAIdentity, "directory-1"))
	initialDirectory, err := directoryClient.Poll(context.Background(), "", 0)
	if err != nil || initialDirectory.Directory.Generation != "directory-1" || initialDirectory.ETag != `"directory-1"` || initialDirectory.NotModified {
		return nil, errors.New("initial peer-directory poll failed")
	}
	type pollResult struct {
		result infrastructure.PeerDirectoryPollResult
		err    error
	}
	changedResult := make(chan pollResult, 1)
	go func() {
		result, err := directoryClient.Poll(context.Background(), initialDirectory.ETag, 5000)
		changedResult <- pollResult{result: result, err: err}
	}()
	time.Sleep(25 * time.Millisecond)
	core.publishDirectory(fixturePeerDirectory(clientAIdentity, "directory-2"))
	changedPoll := <-changedResult
	if changedPoll.err != nil || changedPoll.result.Directory.Generation != "directory-2" || changedPoll.result.ETag != `"directory-2"` {
		return nil, errors.New("long poll did not deliver the changed directory")
	}
	unchangedDirectory, err := directoryClient.Poll(context.Background(), changedPoll.result.ETag, 15)
	if err != nil || !unchangedDirectory.NotModified || unchangedDirectory.ETag != changedPoll.result.ETag {
		return nil, errors.New("unchanged directory did not return not-modified")
	}
	core.publishDirectory(fixturePeerDirectory(clientBIdentity, "directory-3"))
	_, callerMismatchErr := directoryClient.Poll(context.Background(), "", 0)
	if !errors.Is(callerMismatchErr, models.ErrInvalidPeerDirectory) {
		return nil, errors.New("client accepted a directory for another replica")
	}
	core.publishDirectory(fixturePeerDirectory(clientAIdentity, "directory-2"))
	cancelledContext, cancelPoll := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancelPoll()
	_, cancelledPollErr := directoryClient.Poll(cancelledContext, `"directory-2"`, 5000)
	if !errors.Is(cancelledPollErr, infrastructure.ErrPeerDirectoryUnavailable) {
		return nil, errors.New("peer-directory long poll did not observe cancellation")
	}
	clock.Set(fixtureEpoch.Add(41 * time.Second))
	_, expiredErr := clientA.Renew(context.Background(), models.ReplicaRenewalRequest{
		ContractVersion: replicaContract.ContractVersion, Identity: clientAIdentity,
		AppliedGeneration: "config-7", Ready: true,
	})
	if !errors.Is(expiredErr, models.ErrReplicaLeaseExpired) {
		return nil, errors.New("lease expiry was not enforced")
	}
	clock.Set(fixtureEpoch.Add(40 * time.Second))
	clientB, transportB, err := newRegistrationClient(httpContract, replicaContract, authority, corePair, baseURL, coreIdentity, clientBIdentity)
	if err != nil {
		return nil, err
	}
	defer transportB.CloseIdleConnections()
	requestB := registrationRequest(replicaContract.ContractVersion, clientBIdentity)
	reconnected, err := clientB.Register(context.Background(), requestB)
	if err != nil {
		return nil, err
	}
	spoofedCertificate := ""
	if _, err := infrastructure.NewReplicaLifecycleClient(replicaContract, baseURL, transportB, clientAIdentity); errors.Is(err, models.ErrReplicaIdentityMismatch) {
		spoofedCertificate = "invalid_client_identity"
	} else {
		return nil, errors.New("client accepted a different replica certificate identity")
	}
	spoofedBody, err := rawSpoofedRegistration(transportB, baseURL, replicaContract, requestA)
	if err != nil || spoofedBody != "identity_mismatch" {
		return nil, errors.New("core fixture did not reject the identity spoof")
	}
	mutatedEndpoint := requestB
	mutatedEndpoint.RestEndpoint = "https://127.0.0.1:9444"
	_, endpointErr := clientB.Register(context.Background(), mutatedEndpoint)
	if !errors.Is(endpointErr, models.ErrReplicaIdentityConflict) {
		return nil, errors.New("core fixture did not reject an endpoint mutation")
	}
	mutatedPlacement := requestB
	mutatedPlacement.Identity.PlacementID = "node-b"
	_, placementErr := clientB.Register(context.Background(), mutatedPlacement)
	if !errors.Is(placementErr, models.ErrReplicaIdentityMismatch) && !errors.Is(placementErr, models.ErrReplicaIdentityConflict) {
		return nil, errors.New("core fixture did not reject a placement mutation")
	}
	invalidRelease := requestB
	invalidRelease.Release.Version = "01.2.3"
	_, invalidReleaseErr := clientB.Register(context.Background(), invalidRelease)
	invalidCompatibility := requestB
	invalidCompatibility.AcceptedContracts = []models.ContractRange{{
		ContractID: "org.example.peer", MinimumVersion: "2.0.0", MaximumVersionExclusive: "1.0.0",
	}}
	_, invalidCompatibilityErr := clientB.Register(context.Background(), invalidCompatibility)
	cohortA := requestA
	cohortB := requestB
	cohortB.Release.Version = "1.3.0"
	cohortB.Release.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte("fixture release B")))
	cohortB.AdvertisedContracts = []models.ContractVersion{{ContractID: "org.example.settings", Version: "1.1.0", SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("settings schema 1.1")))}}
	cohortA.AdvertisedContracts = []models.ContractVersion{{ContractID: "org.example.settings", Version: "1.0.0", SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("settings schema 1.0")))}}
	cohortA.AcceptedContracts = []models.ContractRange{{ContractID: "org.example.settings", MinimumVersion: "1.0.0", MaximumVersionExclusive: "2.0.0"}}
	cohortB.AcceptedContracts = []models.ContractRange{{ContractID: "org.example.settings", MinimumVersion: "1.0.0", MaximumVersionExclusive: "2.0.0"}}
	mutuallyAccepted := models.ReleaseCohortCompatible([]models.ReplicaRegistrationRequest{cohortA, cohortB})
	cohortA.AcceptedContracts[0].MaximumVersionExclusive = "1.1.0"
	oneWayAccepted := models.ReleaseCohortCompatible([]models.ReplicaRegistrationRequest{cohortA, cohortB})
	noEvidenceA := requestA
	noEvidenceB := requestB
	noEvidenceA.AdvertisedContracts = []models.ContractVersion{}
	noEvidenceA.AcceptedContracts = []models.ContractRange{}
	noEvidenceB.AdvertisedContracts = []models.ContractVersion{}
	noEvidenceB.AcceptedContracts = []models.ContractRange{}
	noEvidenceA.Release.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte("release without evidence A")))
	noEvidenceB.Release.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte("release without evidence B")))
	noEvidenceAccepted := models.ReleaseCohortCompatible([]models.ReplicaRegistrationRequest{noEvidenceA, noEvidenceB})
	sameReleaseA := noEvidenceA
	sameReleaseB := noEvidenceB
	sameReleaseB.Release.SHA256 = sameReleaseA.Release.SHA256
	sameReleaseAccepted := models.ReleaseCohortCompatible([]models.ReplicaRegistrationRequest{sameReleaseA, sameReleaseB})
	deregistered, err := clientB.Deregister(context.Background(), models.ReplicaDeregistrationRequest{
		ContractVersion: replicaContract.ContractVersion, Identity: clientBIdentity,
	})
	if err != nil {
		return nil, err
	}
	clientCIdentity := models.PeerReplicaID{InstanceID: "forms-db", ReplicaID: "forms-2", IncarnationID: "incarnation-c", PlacementID: "node-a"}
	clientDIdentity := models.PeerReplicaID{InstanceID: "forms-db", ReplicaID: "forms-3", IncarnationID: "incarnation-d", PlacementID: "node-a"}
	clientC, transportC, err := newRegistrationClient(httpContract, replicaContract, authority, corePair, baseURL, coreIdentity, clientCIdentity)
	if err != nil {
		return nil, err
	}
	defer transportC.CloseIdleConnections()
	clientD, transportD, err := newRegistrationClient(httpContract, replicaContract, authority, corePair, baseURL, coreIdentity, clientDIdentity)
	if err != nil {
		return nil, err
	}
	defer transportD.CloseIdleConnections()
	cancelledLifecycle, cancelLifecycle := context.WithCancel(context.Background())
	cancelLifecycle()
	_, cancelledRegisterErr := clientC.Register(cancelledLifecycle, registrationRequest(replicaContract.ContractVersion, clientCIdentity))
	_, cancelledRenewErr := clientC.Renew(cancelledLifecycle, models.ReplicaRenewalRequest{
		ContractVersion: replicaContract.ContractVersion, Identity: clientCIdentity,
		AppliedGeneration: "config-7", Ready: true,
	})
	_, cancelledDeregisterErr := clientC.Deregister(cancelledLifecycle, models.ReplicaDeregistrationRequest{
		ContractVersion: replicaContract.ContractVersion, Identity: clientCIdentity,
	})
	_, unreplayedErr := clientC.Deregister(context.Background(), models.ReplicaDeregistrationRequest{
		ContractVersion: replicaContract.ContractVersion, Identity: clientCIdentity,
	})
	if !errors.Is(unreplayedErr, models.ErrReplicaNotRegistered) {
		return nil, errors.New("a cancelled lifecycle call reached Core")
	}
	registeredAfterCancellation, err := clientC.Register(context.Background(), registrationRequest(replicaContract.ContractVersion, clientCIdentity))
	if err != nil {
		return nil, err
	}
	_, unknownDeregistrationErr := clientD.Deregister(context.Background(), models.ReplicaDeregistrationRequest{
		ContractVersion: replicaContract.ContractVersion, Identity: clientDIdentity,
	})
	deregisteredClientC, err := clientC.Deregister(context.Background(), models.ReplicaDeregistrationRequest{
		ContractVersion: replicaContract.ContractVersion, Identity: clientCIdentity,
	})
	if err != nil {
		return nil, err
	}
	_, repeatedDeregistrationErr := clientC.Deregister(context.Background(), models.ReplicaDeregistrationRequest{
		ContractVersion: replicaContract.ContractVersion, Identity: clientCIdentity,
	})
	overLimitAdvertised := registrationRequest(replicaContract.ContractVersion, clientCIdentity)
	overLimitAdvertised.AdvertisedContracts = make([]models.ContractVersion, 0, replicaContract.Limits.MaximumContractsPerList+1)
	for index := 0; index <= replicaContract.Limits.MaximumContractsPerList; index++ {
		overLimitAdvertised.AdvertisedContracts = append(overLimitAdvertised.AdvertisedContracts, models.ContractVersion{
			ContractID: fmt.Sprintf("org.example.limit-%d", index), Version: "1.0.0",
			SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("limit contract %d", index)))),
		})
	}
	_, overLimitAdvertisedErr := clientC.Register(context.Background(), overLimitAdvertised)
	overLimitAccepted := registrationRequest(replicaContract.ContractVersion, clientCIdentity)
	overLimitAccepted.AcceptedContracts = make([]models.ContractRange, 0, replicaContract.Limits.MaximumContractsPerList+1)
	for index := 0; index <= replicaContract.Limits.MaximumContractsPerList; index++ {
		overLimitAccepted.AcceptedContracts = append(overLimitAccepted.AcceptedContracts, models.ContractRange{
			ContractID: fmt.Sprintf("org.example.limit-%d", index), MinimumVersion: "1.0.0", MaximumVersionExclusive: "2.0.0",
		})
	}
	_, overLimitAcceptedErr := clientC.Register(context.Background(), overLimitAccepted)
	if replicaContract.Limits.MaximumPeerEndpoints != 4 || replicaContract.Limits.MaximumContractsPerList != 64 {
		return nil, errors.New("published replica lifecycle limits changed")
	}
	overLimitPeerEndpoints := registrationRequest(replicaContract.ContractVersion, clientCIdentity)
	overLimitPeerEndpoints.PeerEndpoints = []models.ReplicaPeerEndpoint{
		{Carrier: models.PeerCarrierTCP, Endpoint: "forms-1.internal:9443", SecurityProfile: models.PeerSecurityMTLS},
		{Carrier: models.PeerCarrierQUIC, Endpoint: "forms-1.internal:9444", SecurityProfile: models.PeerSecurityMTLS},
		{Carrier: models.PeerCarrierUnix, Endpoint: "/var/run/forms.sock", SecurityProfile: models.PeerSecurityMTLS},
		{Carrier: models.PeerCarrierWindowsPipe, Endpoint: `\\.\pipe\forms`, SecurityProfile: models.PeerSecurityMTLS},
		{Carrier: models.PeerCarrierTCP, Endpoint: "forms-2.internal:9443", SecurityProfile: models.PeerSecurityMTLS},
	}
	_, overLimitPeerEndpointsErr := clientC.Register(context.Background(), overLimitPeerEndpoints)
	return map[string]any{
		"peerDirectoryInitial":        initialDirectory.Directory.Generation == "directory-1",
		"peerDirectoryChanged":        changedPoll.result.Directory.Generation == "directory-2",
		"peerDirectoryUnchanged":      unchangedDirectory.NotModified,
		"peerDirectoryCallerMismatch": errors.Is(callerMismatchErr, models.ErrInvalidPeerDirectory),
		"peerDirectoryCancelled":      errors.Is(cancelledPollErr, infrastructure.ErrPeerDirectoryUnavailable),
		"registered":                  registered,
		"renewed":                     renewed,
		"expiredRenewal":              errorName(expiredErr, models.ErrReplicaLeaseExpired, "lease_expired"),
		"reconnected":                 reconnected,
		"spoofedCertificate":          map[string]string{"error": spoofedCertificate},
		"spoofedBody":                 map[string]string{"error": spoofedBody},
		"mutatedEndpoint":             errorName(endpointErr, models.ErrReplicaIdentityConflict, "identity_conflict"),
		"immutablePlacement":          errorNameEither(placementErr, "identity_mismatch", models.ErrReplicaIdentityMismatch, models.ErrReplicaIdentityConflict),
		"invalidRelease":              errorName(invalidReleaseErr, models.ErrInvalidReplicaRegistration, "invalid_registration"),
		"invalidCompatibility":        errorName(invalidCompatibilityErr, models.ErrInvalidReplicaRegistration, "invalid_registration"),
		"releaseCohortCompatibility": map[string]bool{
			"sameDigest": sameReleaseAccepted, "mutuallyAccepted": mutuallyAccepted,
			"oneWayAcceptance": oneWayAccepted, "missingEvidence": noEvidenceAccepted,
		},
		"deregistered":                deregistered,
		"cancelledRegister":           errorName(cancelledRegisterErr, infrastructure.ErrReplicaLifecycleUnavailable, "lifecycle_unavailable"),
		"cancelledRenew":              errorName(cancelledRenewErr, infrastructure.ErrReplicaLifecycleUnavailable, "lifecycle_unavailable"),
		"cancelledDeregister":         errorName(cancelledDeregisterErr, infrastructure.ErrReplicaLifecycleUnavailable, "lifecycle_unavailable"),
		"cancelledCallsNotReplayed":   errorName(unreplayedErr, models.ErrReplicaNotRegistered, "replica_not_registered"),
		"registeredAfterCancellation": registeredAfterCancellation,
		"unknownDeregistration":       errorName(unknownDeregistrationErr, models.ErrReplicaNotRegistered, "replica_not_registered"),
		"deregisteredClientC":         deregisteredClientC,
		"repeatedDeregistration":      errorName(repeatedDeregistrationErr, models.ErrReplicaNotRegistered, "replica_not_registered"),
		"overLimitAdvertised":         errorName(overLimitAdvertisedErr, models.ErrInvalidReplicaRegistration, "invalid_registration"),
		"overLimitAccepted":           errorName(overLimitAcceptedErr, models.ErrInvalidReplicaRegistration, "invalid_registration"),
		"maximumPeerEndpoints":        replicaContract.Limits.MaximumPeerEndpoints,
		"maximumContractsPerList":     replicaContract.Limits.MaximumContractsPerList,
		"overLimitPeerEndpoints":      errorName(overLimitPeerEndpointsErr, models.ErrInvalidReplicaRegistration, "invalid_registration"),
	}, nil
}

func newRegistrationClient(httpContract infrastructure.HTTPContract, replicaContract infrastructure.ReplicaLifecycleContract, authority *fixtureAuthority, core keypair, baseURL string, coreIdentity models.PeerIdentity, identity models.PeerReplicaID) (*infrastructure.ReplicaLifecycleClient, *infrastructure.MutualTLSClient, error) {
	uri, err := replicaContract.ReplicaIdentityURI(identity)
	if err != nil {
		return nil, nil, err
	}
	clientPair, err := authority.issue(identity.ReplicaID, uri, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil)
	if err != nil {
		return nil, nil, err
	}
	credentials, err := infrastructure.LoadCredentials(httpContract, infrastructure.CredentialsMaterial{
		CABundle:             authority.certificatePEM,
		ServerCertificatePEM: core.certificatePEM,
		ServerKeyPEM:         core.privateKeyPEM,
		ClientCertificatePEM: clientPair.certificatePEM,
		ClientKeyPEM:         clientPair.privateKeyPEM,
	})
	if err != nil {
		return nil, nil, err
	}
	provider, err := infrastructure.NewStaticCredentialsProvider(credentials)
	if err != nil {
		return nil, nil, err
	}
	mutualTLS, err := infrastructure.NewMutualTLSClient(httpContract, provider, infrastructure.MutualTLSClientConfig{
		Peer: coreIdentity, ServerName: "127.0.0.1", Revocation: fixtureRevocation{}, Clock: realClock{},
	})
	if err != nil {
		return nil, nil, err
	}
	client, err := infrastructure.NewReplicaLifecycleClient(replicaContract, baseURL, mutualTLS, identity)
	return client, mutualTLS, err
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func registrationRequest(contractVersion string, identity models.PeerReplicaID) models.ReplicaRegistrationRequest {
	digest := sha256.Sum256([]byte("fixture release"))
	contractDigest := sha256.Sum256([]byte("fixture peer contract"))
	return models.ReplicaRegistrationRequest{
		ContractVersion:     contractVersion,
		Identity:            identity,
		RestEndpoint:        "https://127.0.0.1:9443",
		PeerEndpoints:       []models.ReplicaPeerEndpoint{{Carrier: models.PeerCarrierQUIC, Endpoint: "forms-1.internal:9443", SecurityProfile: models.PeerSecurityMTLS}},
		Release:             models.ReplicaRelease{Version: "1.2.3", SHA256: fmt.Sprintf("%x", digest)},
		AdvertisedContracts: []models.ContractVersion{{ContractID: "org.example.peer", Version: "1.4.0", SHA256: fmt.Sprintf("%x", contractDigest)}},
		AcceptedContracts:   []models.ContractRange{{ContractID: "org.example.peer", MinimumVersion: "1.0.0", MaximumVersionExclusive: "2.0.0"}},
		AppliedGeneration:   "config-7", Ready: true,
	}
}

func (core *fakeCore) handle(writer http.ResponseWriter, request *http.Request) {
	if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 {
		core.writeProblem(writer, "unauthenticated")
		return
	}
	if request.URL.Path == "/internal/v2/plugin-peer-directory" {
		core.handlePeerDirectory(writer, request)
		return
	}
	identity := models.PeerReplicaID{}
	var operation string
	for name, endpoint := range core.contract.Endpoints {
		if request.Method == endpoint.Method && request.URL.Path == endpoint.Path {
			operation = name
			break
		}
	}
	if operation == "" {
		http.NotFound(writer, request)
		return
	}
	contract := core.contract.Requests[operation]
	request.Body = http.MaxBytesReader(writer, request.Body, contract.MaximumBytes)
	var payload any
	switch operation {
	case "register":
		payload = &models.ReplicaRegistrationRequest{}
	case "renew":
		payload = &models.ReplicaRenewalRequest{}
	case "deregister":
		payload = &models.ReplicaDeregistrationRequest{}
	}
	if request.Header.Get("content-type") != contract.MediaType || json.NewDecoder(request.Body).Decode(payload) != nil {
		core.writeProblem(writer, "invalidRequest")
		return
	}
	switch document := payload.(type) {
	case *models.ReplicaRegistrationRequest:
		identity = document.Identity
	case *models.ReplicaRenewalRequest:
		identity = document.Identity
	case *models.ReplicaDeregistrationRequest:
		identity = document.Identity
	}
	expectedURI, err := core.contract.ReplicaIdentityURI(identity)
	if err != nil || !certificateHasURI(request.TLS.PeerCertificates[0], expectedURI) {
		core.writeProblem(writer, "identityMismatch")
		return
	}
	core.mutex.Lock()
	defer core.mutex.Unlock()
	key := identity.InstanceID + "\x00" + identity.ReplicaID
	current, exists := core.records[key]
	switch document := payload.(type) {
	case *models.ReplicaRegistrationRequest:
		if document.Validate() != nil || document.ContractVersion != core.contract.ContractVersion {
			core.writeProblem(writer, "invalidRequest")
			return
		}
		if exists && current.expires.After(core.clock.Now()) {
			if current.request.Identity != document.Identity || !immutableEqual(current.request, *document) {
				core.writeProblem(writer, "identityConflict")
				return
			}
		} else if exists && current.request.Identity.IncarnationID == document.Identity.IncarnationID && current.active {
			core.writeProblem(writer, "leaseExpired")
			return
		}
		lease := core.clock.Now().Add(time.Duration(core.contract.Lease.TTLSeconds) * time.Second)
		core.records[key] = registrationRecord{request: *document, expires: lease, active: true}
		core.writeSuccess(writer, operation, models.ReplicaRegistrationResponse{
			ContractVersion: core.contract.ContractVersion, Identity: document.Identity,
			ServerTime: core.clock.Now(), LeaseExpiresAt: lease,
		})
	case *models.ReplicaRenewalRequest:
		if document.Validate() != nil || document.ContractVersion != core.contract.ContractVersion {
			core.writeProblem(writer, "invalidRequest")
			return
		}
		if !exists || !current.active {
			core.writeProblem(writer, "notRegistered")
			return
		}
		if !current.expires.After(core.clock.Now()) {
			core.writeProblem(writer, "leaseExpired")
			return
		}
		if current.request.Identity != document.Identity {
			core.writeProblem(writer, "identityConflict")
			return
		}
		current.request.AppliedGeneration = document.AppliedGeneration
		current.request.Ready = document.Ready
		current.expires = core.clock.Now().Add(time.Duration(core.contract.Lease.TTLSeconds) * time.Second)
		core.records[key] = current
		core.writeSuccess(writer, operation, models.ReplicaLease{
			ContractVersion: core.contract.ContractVersion, Identity: document.Identity,
			ServerTime: core.clock.Now(), LeaseExpiresAt: current.expires,
		})
	case *models.ReplicaDeregistrationRequest:
		if document.Validate() != nil || document.ContractVersion != core.contract.ContractVersion {
			core.writeProblem(writer, "invalidRequest")
			return
		}
		if !exists || !current.active {
			core.writeProblem(writer, "notRegistered")
			return
		}
		if current.request.Identity != document.Identity {
			core.writeProblem(writer, "identityConflict")
			return
		}
		current.active = false
		core.records[key] = current
		core.writeSuccess(writer, operation, models.ReplicaDeregistrationResponse{
			ContractVersion: core.contract.ContractVersion, Identity: document.Identity, Deregistered: true,
		})
	}
}

func (core *fakeCore) handlePeerDirectory(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet || len(request.URL.Query()) != 1 || len(request.URL.Query()["waitMs"]) != 1 {
		core.writeProblem(writer, "invalidRequest")
		return
	}
	waitMs, err := strconv.Atoi(request.URL.Query().Get("waitMs"))
	if err != nil || waitMs < 0 || waitMs > 20000 {
		core.writeProblem(writer, "invalidRequest")
		return
	}
	if !core.authenticatedReplica(request.TLS.PeerCertificates[0]) {
		core.writeProblem(writer, "identityMismatch")
		return
	}
	if request.Header.Get("accept") != "application/json" {
		core.writeProblem(writer, "invalidRequest")
		return
	}
	conditionalHeaders := request.Header.Values("if-none-match")
	if len(conditionalHeaders) > 1 || len(conditionalHeaders) == 0 && waitMs != 0 {
		core.writeProblem(writer, "invalidRequest")
		return
	}
	requestedETag := ""
	if len(conditionalHeaders) == 1 {
		requestedETag = conditionalHeaders[0]
	}
	timer := time.NewTimer(time.Duration(waitMs) * time.Millisecond)
	defer timer.Stop()
	for {
		core.directoryMutex.Lock()
		body := append([]byte(nil), core.directory...)
		etag := core.directoryETag
		changed := core.directoryChanged
		core.directoryMutex.Unlock()
		if len(body) == 0 || etag == "" {
			core.writeProblem(writer, "unavailable")
			return
		}
		if requestedETag == "" || requestedETag != etag {
			writer.Header().Set("cache-control", "no-store")
			writer.Header().Set("content-type", "application/json")
			writer.Header().Set("etag", etag)
			writer.WriteHeader(http.StatusOK)
			process.Write(writer, body)
			return
		}
		if waitMs == 0 {
			writer.Header().Set("cache-control", "no-store")
			writer.Header().Set("etag", etag)
			writer.WriteHeader(http.StatusNotModified)
			return
		}
		select {
		case <-changed:
			continue
		case <-timer.C:
			waitMs = 0
			continue
		case <-request.Context().Done():
			return
		}
	}
}

func (core *fakeCore) authenticatedReplica(certificate *x509.Certificate) bool {
	core.mutex.Lock()
	defer core.mutex.Unlock()
	for _, record := range core.records {
		if !record.active || !record.expires.After(core.clock.Now()) {
			continue
		}
		identityURI, err := core.contract.ReplicaIdentityURI(record.request.Identity)
		if err == nil && certificateHasURI(certificate, identityURI) {
			return true
		}
	}
	return false
}

func (core *fakeCore) publishDirectory(directory models.PeerDirectory) {
	body, err := json.Marshal(directory)
	if err != nil {
		panic("invalid fixture peer directory")
	}
	core.directoryMutex.Lock()
	core.directory = body
	core.directoryETag = `"` + directory.Generation + `"`
	close(core.directoryChanged)
	core.directoryChanged = make(chan struct{})
	core.directoryMutex.Unlock()
}

func fixturePeerDirectory(caller models.PeerReplicaID, generation string) models.PeerDirectory {
	issuedAt := time.Now().UTC()
	expiresAt := issuedAt.Add(20 * time.Second)
	return models.PeerDirectory{
		ContractVersion: models.PeerDirectoryContractVersion,
		Generation:      generation, IssuedAt: issuedAt, ExpiresAt: expiresAt, Caller: caller,
		Links: []models.PeerLink{{
			LinkID: "server-link", TargetInstanceID: "server",
			PlacementRule: models.PeerPlacementRemote, Carrier: models.PeerCarrierQUIC,
			SecurityProfile:       models.PeerSecurityMTLS,
			RequiredPeerContracts: []models.ContractRange{},
			Replicas: []models.PeerDirectoryReplica{{
				Identity: models.PeerReplicaID{InstanceID: "server", ReplicaID: "server-1", IncarnationID: "server-incarnation", PlacementID: "node-b"},
				Endpoint: "server-1.internal:9443", Eligibility: models.PeerEligibilityReady,
				EligibleUntil: &expiresAt, Weight: 1, PeerContracts: []models.ContractVersion{},
			}},
		}},
	}
}

func immutableEqual(left, right models.ReplicaRegistrationRequest) bool {
	left.AppliedGeneration, right.AppliedGeneration = "", ""
	left.Ready, right.Ready = false, false
	return reflect.DeepEqual(left, right)
}

func (core *fakeCore) writeSuccess(writer http.ResponseWriter, operation string, value any) {
	response := core.contract.Responses[operation]
	writer.Header().Set("content-type", response.MediaType)
	writer.WriteHeader(response.Status)
	process.Must(json.NewEncoder(writer).Encode(value))
}

func (core *fakeCore) writeProblem(writer http.ResponseWriter, name string) {
	problem := core.contract.Problems[name]
	writer.Header().Set("content-type", "application/json")
	writer.WriteHeader(problem.Status)
	process.Must(json.NewEncoder(writer).Encode(map[string]string{"code": problem.Code}))
}

func rawSpoofedRegistration(client *infrastructure.MutualTLSClient, baseURL string, contract infrastructure.ReplicaLifecycleContract, request models.ReplicaRegistrationRequest) (string, error) {
	endpoint := contract.Endpoints["register"]
	request.Identity = models.PeerReplicaID{InstanceID: "forms-db", ReplicaID: "forms-1", IncarnationID: "incarnation-a", PlacementID: "node-a"}
	body, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	target, err := url.JoinPath(baseURL, endpoint.Path)
	if err != nil {
		return "", err
	}
	httpRequest, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpRequest.Header.Set("content-type", "application/json")
	response, err := client.HTTPClient().Do(httpRequest) //nolint:bodyclose // the fixture closes the response body below.
	if err != nil {
		return "", err
	}
	defer process.Close(response.Body)
	var result struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.Code, nil
}

func certificateHasURI(certificate *x509.Certificate, expected string) bool {
	for _, uri := range certificate.URIs {
		if uri.String() == expected {
			return true
		}
	}
	return false
}

func errorName(err, expected error, value string) map[string]string {
	if errors.Is(err, expected) {
		return map[string]string{"error": value}
	}
	return map[string]string{"error": "unexpected"}
}

func errorNameEither(err error, value string, expected ...error) map[string]string {
	for _, candidate := range expected {
		if errors.Is(err, candidate) {
			return map[string]string{"error": value}
		}
	}
	return map[string]string{"error": "unexpected"}
}

func newAuthority() (*fixtureAuthority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture-v2-ca"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true, IsCA: true, MaxPathLen: 2,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &fixtureAuthority{certificate: certificate, privateKey: key, certificatePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}, nil
}

func (authority *fixtureAuthority) issue(commonName, identityURI string, usages []x509.ExtKeyUsage, ips []string) (keypair, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return keypair{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return keypair{}, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: commonName},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(12 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: usages, BasicConstraintsValid: true,
	}
	if identityURI != "" {
		parsed, err := url.Parse(identityURI)
		if err != nil {
			return keypair{}, err
		}
		template.URIs = []*url.URL{parsed}
	}
	for _, ip := range ips {
		template.IPAddresses = append(template.IPAddresses, net.ParseIP(ip))
	}
	der, err := x509.CreateCertificate(rand.Reader, template, authority.certificate, &key.PublicKey, authority.privateKey)
	if err != nil {
		return keypair{}, err
	}
	privateDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return keypair{}, err
	}
	return keypair{
		certificatePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		privateKeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateDER}),
	}, nil
}

func write(value any) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		os.Exit(2)
	}
}

var _ interfaces.RevocationSource = fixtureRevocation{}
