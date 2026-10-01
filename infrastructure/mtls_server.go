package infrastructure

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Liapoldus/plugin-sdk/domain/interfaces"
	"github.com/Liapoldus/plugin-sdk/domain/models"
)

var (
	// ErrInvalidServerTLS rejects a plugin-side server configuration that cannot
	// enforce per-replica mutual TLS.
	ErrInvalidServerTLS = errors.New("invalid Plugin SDK mutual TLS server configuration")
	// ErrInvalidClientTLS rejects a plugin-to-Core client configuration that
	// cannot enforce per-replica mutual TLS.
	ErrInvalidClientTLS = errors.New("invalid Plugin SDK mutual TLS client configuration")
	// ErrPeerNotAuthorized rejects a verified peer that is not the single
	// registered Core replica identity.
	ErrPeerNotAuthorized = errors.New("peer is not the registered Core replica")
	// ErrPeerCertificateExpired rejects a verified peer whose certificate is
	// outside its validity window.
	ErrPeerCertificateExpired = errors.New("peer certificate is not currently valid")
	// ErrPeerCertificateRevoked rejects a verified peer that a trusted
	// certificate revocation list reports as revoked.
	ErrPeerCertificateRevoked = errors.New("peer certificate is revoked")
	// ErrPeerRevocationUnknown rejects a verified peer whose revocation status
	// could not be established. It is never downgraded to an admitted peer.
	ErrPeerRevocationUnknown = errors.New("peer certificate revocation could not be verified")
	// ErrCredentialRotationFailed reports that rotated credential material could
	// not be validated. The previously validated material stays in force.
	ErrCredentialRotationFailed = errors.New("failed Plugin SDK credential rotation")
	// ErrShutdownGraceExpired reports that in-flight connections outlived the
	// contract shutdown grace.
	ErrShutdownGraceExpired = errors.New("server did not stop within the Plugin SDK contract shutdown grace")
	// ErrMutualTLSRedirect refuses an HTTP redirect on a control-plane call, so a
	// redirect can never move a request to another origin with the replica
	// credentials attached.
	ErrMutualTLSRedirect = errors.New("refused Plugin SDK control-plane redirect")
)

// MutualTLSServerConfig describes the plugin-side HTTPS server. The handler comes
// from the presentation layer; everything below it is transport policy owned by
// this package.
type MutualTLSServerConfig struct {
	// Handler is the presentation-layer HTTP handler. It is required.
	Handler http.Handler
	// Provider supplies operator credential material and is the rotation seam, so
	// a certificate rotated on disk reaches the next handshake.
	Provider CredentialsProvider
	// Peer is the one Core replica identity this plugin process accepts. It is
	// required: per-replica identity is never optional, never a set, and never a
	// wildcard.
	Peer models.PeerIdentity
	// Authorizer is an optional additional gate. When set, both it and the pinned
	// Peer identity must accept the peer, so an authorizer can only narrow the
	// accepted peers and can never widen them.
	Authorizer interfaces.PeerAuthorizer
	// Revocation is the fail-closed revocation source. It is required.
	Revocation interfaces.RevocationSource
	// ErrorLog receives net/http transport diagnostics. It must not point at a
	// sink that retains peer addresses.
	ErrorLog *log.Logger
	// Clock supplies the current time for the peer certificate validity check.
	Clock interfaces.Clock
}

// MutualTLSServer is the plugin-side HTTPS server. It requires and verifies a
// client certificate against the operator trust roots, applies the contract
// minimum TLS version and deadlines, and then gates every handshake on a
// fail-closed revocation check and on the single registered Core replica
// identity.
//
// Revocation and identity are decided once per TLS handshake, which is the only
// point at which a peer certificate is presented. A connection that was admitted
// before a revocation landed therefore keeps serving until it is closed, and the
// contract idle deadline is what bounds that window. Callers that need a shorter
// bound must close connections themselves; nothing here silently re-checks a
// request that arrived on an already-verified connection.
type MutualTLSServer struct {
	contract HTTPContract
	server   *http.Server
	provider CredentialsProvider
	gate     *mutualTLSPeerGate
	snapshot atomic.Pointer[mutualTLSServerState]
	grace    time.Duration
	mutex    sync.Mutex
}

type mutualTLSServerState struct {
	credentials *Credentials
	config      *tls.Config
}

// NewMutualTLSServer builds the plugin-side HTTPS server. It refuses to build a
// server that would accept a client without a certificate, that has no
// revocation source to fail closed with, or that has no single registered Core
// peer identity.
func NewMutualTLSServer(contract HTTPContract, configuration MutualTLSServerConfig) (*MutualTLSServer, error) {
	if configuration.Handler == nil || configuration.Provider == nil || configuration.Revocation == nil {
		return nil, ErrInvalidServerTLS
	}
	if !contract.TransportSecurity.ClientCertificateRequired ||
		contract.TransportSecurity.MinimumTLSVersion < tls.VersionTLS12 {
		return nil, ErrInvalidServerTLS
	}
	authorizer, err := NewMutualTLSPeerAuthorizer(contract, configuration.Peer)
	if err != nil {
		return nil, ErrInvalidServerTLS
	}
	credentials, err := configuration.Provider.Credentials()
	if err != nil || credentials == nil {
		return nil, ErrInvalidServerTLS
	}
	if _, ok := credentials.ServerCertificate(); !ok {
		return nil, ErrInvalidServerTLS
	}
	mutualTLS := &MutualTLSServer{
		contract: contract,
		provider: configuration.Provider,
		gate: &mutualTLSPeerGate{
			authorizer: peerAuthorizerChain{primary: authorizer, additional: configuration.Authorizer},
			revocation: configuration.Revocation,
			clock:      configuration.Clock,
		},
		grace: time.Duration(contract.Deadlines.PluginShutdownGraceSeconds) * time.Second,
	}
	if err := mutualTLS.publish(credentials); err != nil {
		return nil, ErrInvalidServerTLS
	}
	mutualTLS.server = &http.Server{
		Handler:           configuration.Handler,
		TLSConfig:         mutualTLS.snapshot.Load().config,
		ErrorLog:          configuration.ErrorLog,
		ReadHeaderTimeout: time.Duration(contract.Deadlines.PluginReadHeaderSeconds) * time.Second,
		ReadTimeout:       time.Duration(contract.Deadlines.PluginReadSeconds) * time.Second,
		WriteTimeout:      time.Duration(contract.Deadlines.PluginWriteSeconds) * time.Second,
		IdleTimeout:       time.Duration(contract.Deadlines.PluginIdleSeconds) * time.Second,
	}
	return mutualTLS, nil
}

// Server exposes the configured http.Server so a process owner can attach its own
// connection accounting. Transport policy is not mutable through it.
func (mutualTLS *MutualTLSServer) Server() *http.Server {
	if mutualTLS == nil {
		return nil
	}
	return mutualTLS.server
}

// TLSConfig returns the TLS configuration currently in force. The result is a
// snapshot and must not be mutated.
func (mutualTLS *MutualTLSServer) TLSConfig() *tls.Config {
	if mutualTLS == nil {
		return nil
	}
	return mutualTLS.snapshot.Load().config
}

// Serve wraps an already-bound listener in the mutual-TLS listener and serves the
// presentation handler. The caller owns the listener and the process lifecycle;
// the SDK never opens a plaintext listener.
func (mutualTLS *MutualTLSServer) Serve(listener net.Listener) error {
	if mutualTLS == nil || listener == nil {
		return ErrInvalidServerTLS
	}
	return mutualTLS.server.Serve(tls.NewListener(listener, mutualTLS.TLSConfig()))
}

// ListenAndServe binds address and serves the mutual-TLS listener. The configured
// TLS configuration supplies the certificate, so no key material is passed on a
// command line, an argument vector or an environment variable. An empty address
// binds every interface on the default https port.
func (mutualTLS *MutualTLSServer) ListenAndServe(address string) error {
	if mutualTLS == nil {
		return ErrInvalidServerTLS
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	return mutualTLS.Serve(listener)
}

// GracefulShutdown stops accepting connections and drains in-flight requests
// within the contract shutdown grace, never exceeding a deadline the caller
// already set on ctx.
func (mutualTLS *MutualTLSServer) GracefulShutdown(ctx context.Context) error {
	if mutualTLS == nil || mutualTLS.server == nil {
		return ErrInvalidServerTLS
	}
	grace := time.Now().Add(mutualTLS.grace)
	if caller, ok := ctx.Deadline(); ok && caller.Before(grace) {
		grace = caller
	}
	bounded, cancel := context.WithDeadline(ctx, grace)
	defer cancel()
	// A drain that overruns the contract grace leaves the remaining connections
	// open rather than reporting success, so the caller learns that in-flight work
	// outlived its deadline. The transport cause is not surfaced, because it
	// carries context that is not safe to expose.
	if err := mutualTLS.server.Shutdown(bounded); err != nil {
		return ErrShutdownGraceExpired
	}
	return nil
}

// RefreshCredentials re-reads operator credential material and any reloadable
// revocation source. A failure leaves the previously validated material in force
// and returns an error; it never weakens the active configuration. The server
// also refreshes on its own during a handshake, so this call is only needed to
// surface a rotation failure to the process owner.
func (mutualTLS *MutualTLSServer) RefreshCredentials() error {
	if mutualTLS == nil {
		return ErrInvalidServerTLS
	}
	if err := mutualTLS.reloadRevocation(); err != nil {
		return err
	}
	credentials, err := mutualTLS.provider.Credentials()
	if err != nil || credentials == nil {
		return ErrCredentialRotationFailed
	}
	return mutualTLS.publish(credentials)
}

// currentConfig returns the configuration in force for the next handshake. A
// provider that cannot revalidate its material keeps the last validated set in
// force rather than dropping every connection; revocation and peer identity are
// re-evaluated per handshake either way, so posture is unchanged.
func (mutualTLS *MutualTLSServer) currentConfig() *tls.Config {
	if credentials, err := mutualTLS.provider.Credentials(); err == nil && credentials != nil &&
		credentials != mutualTLS.snapshot.Load().credentials {
		_ = mutualTLS.reloadRevocation()
		_ = mutualTLS.publish(credentials)
	}
	return mutualTLS.snapshot.Load().config
}

func (mutualTLS *MutualTLSServer) publish(credentials *Credentials) error {
	serverCertificate, ok := credentials.ServerCertificate()
	if !ok {
		return ErrCredentialRotationFailed
	}
	config := &tls.Config{
		MinVersion:   credentials.minimumTLSVersion,
		Certificates: []tls.Certificate{serverCertificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    credentials.TrustPool(),
		// The standard verifier already proved the chain and the client key usage
		// because ClientAuth is RequireAndVerifyClientCert. Revocation and the
		// registered replica identity are separate, additional gates.
		VerifyPeerCertificate: mutualTLS.gate.verify,
		// A rotation publishes a new configuration instead of mutating a
		// configuration that another handshake may still be reading. crypto/tls
		// consults this once per handshake and does not recurse.
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return mutualTLS.currentConfig(), nil
		},
	}
	mutualTLS.mutex.Lock()
	defer mutualTLS.mutex.Unlock()
	mutualTLS.snapshot.Store(&mutualTLSServerState{credentials: credentials, config: config})
	return nil
}

func (mutualTLS *MutualTLSServer) reloadRevocation() error {
	reloader, ok := mutualTLS.gate.revocation.(RevocationReloader)
	if !ok {
		return nil
	}
	return reloader.Reload()
}

// peerAuthorizerChain requires the pinned per-replica identity and any additional
// operator policy to accept the peer. An additional authorizer can only narrow.
type peerAuthorizerChain struct {
	primary    interfaces.PeerAuthorizer
	additional interfaces.PeerAuthorizer
}

func (chain peerAuthorizerChain) AuthorizePeer(commonName string, uniformResourceIdentifiers []string) bool {
	if chain.primary == nil || !chain.primary.AuthorizePeer(commonName, uniformResourceIdentifiers) {
		return false
	}
	if chain.additional == nil {
		return true
	}
	return chain.additional.AuthorizePeer(commonName, uniformResourceIdentifiers)
}

// MutualTLSPeerAuthorizer pins one Core replica identity. It applies the
// contract's peer-identity rules and refuses any wildcard form, so a certificate
// that merely resembles the expected one is refused.
type MutualTLSPeerAuthorizer struct {
	contract HTTPContract
	expected models.PeerIdentity
}

// NewMutualTLSPeerAuthorizer returns an authorizer for exactly one registered
// Core replica identity. An empty, malformed or wildcard identity is refused: the
// SDK never accepts a peer set of zero and never accepts a pattern.
func NewMutualTLSPeerAuthorizer(contract HTTPContract, expected models.PeerIdentity) (*MutualTLSPeerAuthorizer, error) {
	if expected.CommonName == "" || !expected.Valid() || mutualTLSHasWildcard(expected) {
		return nil, ErrPeerNotAuthorized
	}
	return &MutualTLSPeerAuthorizer{contract: contract, expected: expected}, nil
}

// AuthorizePeer reports whether a verified common name plus verified uniform
// resource identifiers are the one registered Core replica.
func (authorizer *MutualTLSPeerAuthorizer) AuthorizePeer(commonName string, uniformResourceIdentifiers []string) bool {
	if authorizer == nil {
		return false
	}
	peer := authorizer.contract.TransportSecurity.PeerIdentity
	if peer.CommonNameRequired && commonName == "" {
		return false
	}
	if peer.CommonNameMaximumLength > 0 && len(commonName) > peer.CommonNameMaximumLength {
		return false
	}
	for _, identifier := range uniformResourceIdentifiers {
		if !strings.HasPrefix(identifier, peer.UniformResourceIdentifierPrefix) {
			return false
		}
	}
	return authorizer.expected.Matches(commonName, uniformResourceIdentifiers)
}

func mutualTLSHasWildcard(identity models.PeerIdentity) bool {
	return strings.ContainsAny(identity.CommonName, "*?") ||
		strings.ContainsAny(identity.UniformResourceIdentifier, "*?")
}

// mutualTLSPeerGate is the post-handshake gate. It runs only after the standard
// verifier has already proved the chain against the operator roots, and it never
// returns a reason that could echo certificate material, a subject name or an
// address.
type mutualTLSPeerGate struct {
	authorizer interfaces.PeerAuthorizer
	revocation interfaces.RevocationSource
	clock      interfaces.Clock
}

func (gate *mutualTLSPeerGate) verify(rawCertificates [][]byte, verifiedChains [][]*x509.Certificate) error {
	if gate == nil || len(rawCertificates) == 0 {
		return ErrPeerNotAuthorized
	}
	leaf, err := x509.ParseCertificate(rawCertificates[0])
	if err != nil {
		return ErrPeerNotAuthorized
	}
	now := clockNow(gate.clock)
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return ErrPeerCertificateExpired
	}
	if err := gate.checkRevocation(leaf, verifiedChains); err != nil {
		return err
	}
	if !gate.authorizer.AuthorizePeer(leaf.Subject.CommonName, mutualTLSUniformResourceIdentifiers(leaf)) {
		return ErrPeerNotAuthorized
	}
	return nil
}

// checkRevocation walks the whole verified chain, not just the leaf, so a revoked
// intermediate cannot terminate a connection. A revocation error is a refusal.
func (gate *mutualTLSPeerGate) checkRevocation(leaf *x509.Certificate, verifiedChains [][]*x509.Certificate) error {
	chain := []*x509.Certificate{leaf}
	if len(verifiedChains) > 0 && len(verifiedChains[0]) > 0 {
		chain = verifiedChains[0]
	}
	for _, certificate := range chain {
		revoked, err := gate.revocation.Revoked(certificate.SerialNumber.Bytes())
		if err != nil {
			return ErrPeerRevocationUnknown
		}
		if revoked {
			return ErrPeerCertificateRevoked
		}
	}
	return nil
}

// MutualTLSClientConfig describes the plugin-to-Core HTTPS client.
type MutualTLSClientConfig struct {
	// Peer is the one Core identity this plugin process dials. It is required.
	Peer models.PeerIdentity
	// ServerName is the name or address the Core replica presents its certificate
	// under. It is required and is never written to a log or an error.
	ServerName string
	// Revocation is the fail-closed revocation source for the Core certificate.
	Revocation interfaces.RevocationSource
	// Clock supplies the current time for the server certificate validity check.
	Clock interfaces.Clock
}

// MutualTLSClient dials Core over mutual TLS. The transport is rebuilt from the
// credentials currently in force on every new connection, so a rotation on disk
// reaches the next call without restarting the plugin process.
type MutualTLSClient struct {
	contract   HTTPContract
	provider   CredentialsProvider
	gate       *mutualTLSPeerGate
	serverName string
	transport  *http.Transport
	client     *http.Client
	snapshot   atomic.Pointer[mutualTLSClientState]
	mutex      sync.Mutex
}

type mutualTLSClientState struct {
	credentials *Credentials
	config      *tls.Config
}

// NewMutualTLSClient builds the plugin-to-Core client over a credential provider.
// The client is HTTPS-only: there is no plaintext or bearer-only path to
// downgrade to.
func NewMutualTLSClient(contract HTTPContract, provider CredentialsProvider, configuration MutualTLSClientConfig) (*MutualTLSClient, error) {
	if provider == nil || configuration.Revocation == nil || configuration.ServerName == "" {
		return nil, ErrInvalidClientTLS
	}
	if contract.TransportSecurity.MinimumTLSVersion < tls.VersionTLS12 {
		return nil, ErrInvalidClientTLS
	}
	authorizer, err := NewMutualTLSPeerAuthorizer(contract, configuration.Peer)
	if err != nil {
		return nil, ErrInvalidClientTLS
	}
	credentials, err := provider.Credentials()
	if err != nil || credentials == nil {
		return nil, ErrInvalidClientTLS
	}
	mutualTLS := &MutualTLSClient{
		contract:   contract,
		provider:   provider,
		serverName: configuration.ServerName,
		gate: &mutualTLSPeerGate{
			authorizer: authorizer,
			revocation: configuration.Revocation,
			clock:      configuration.Clock,
		},
	}
	if err := mutualTLS.publish(credentials); err != nil {
		return nil, ErrInvalidClientTLS
	}
	mutualTLS.transport = &http.Transport{
		DialTLSContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialer := &tls.Dialer{
				NetDialer: &net.Dialer{
					Timeout: time.Duration(contract.Deadlines.ClientDialSeconds) * time.Second,
				},
				Config: mutualTLS.currentConfig(),
			}
			return dialer.DialContext(ctx, network, address)
		},
		ResponseHeaderTimeout: time.Duration(contract.Deadlines.ClientResponseHeaderSeconds) * time.Second,
		ExpectContinueTimeout: time.Duration(contract.Deadlines.ClientDialSeconds) * time.Second,
		IdleConnTimeout:       time.Duration(contract.Deadlines.PluginIdleSeconds) * time.Second,
	}
	mutualTLS.client = &http.Client{
		Transport: mutualTLS.transport,
		Timeout:   time.Duration(contract.Deadlines.CoreConfigPullSeconds) * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrMutualTLSRedirect
		},
	}
	return mutualTLS, nil
}

// HTTPClient returns the shared client, so a caller that builds its own requests
// cannot attach the replica credentials to a plaintext or redirected transport.
func (mutualTLS *MutualTLSClient) HTTPClient() *http.Client {
	if mutualTLS == nil {
		return nil
	}
	return mutualTLS.client
}

// Do performs one control-plane call with the contract's default deadline.
func (mutualTLS *MutualTLSClient) Do(request *http.Request) (*http.Response, error) {
	if mutualTLS == nil || request == nil || mutualTLS.client == nil {
		return nil, ErrInvalidClientTLS
	}
	if request.URL == nil || request.URL.Scheme != "https" {
		return nil, ErrInvalidClientTLS
	}
	return mutualTLS.client.Do(request)
}

// CloseIdleConnections releases pooled connections, so a rotated credential is not
// served over an already-established connection.
func (mutualTLS *MutualTLSClient) CloseIdleConnections() {
	if mutualTLS != nil && mutualTLS.transport != nil {
		mutualTLS.transport.CloseIdleConnections()
	}
}

// RefreshCredentials re-reads operator credential material and any reloadable
// revocation source. A failure leaves the previously validated material in force.
func (mutualTLS *MutualTLSClient) RefreshCredentials() error {
	if mutualTLS == nil {
		return ErrInvalidClientTLS
	}
	if reloader, ok := mutualTLS.gate.revocation.(RevocationReloader); ok {
		if err := reloader.Reload(); err != nil {
			return err
		}
	}
	credentials, err := mutualTLS.provider.Credentials()
	if err != nil || credentials == nil {
		return ErrCredentialRotationFailed
	}
	return mutualTLS.publish(credentials)
}

// TLSConfig returns the client TLS configuration currently in force as a snapshot
// that must not be mutated.
func (mutualTLS *MutualTLSClient) TLSConfig() *tls.Config {
	if mutualTLS == nil {
		return nil
	}
	return mutualTLS.snapshot.Load().config
}

// currentConfig returns the configuration in force for the next connection. A
// provider that cannot revalidate its material keeps the last validated set in
// force rather than failing every control-plane call; revocation and the pinned
// Core identity are re-evaluated on every connection either way.
func (mutualTLS *MutualTLSClient) currentConfig() *tls.Config {
	if credentials, err := mutualTLS.provider.Credentials(); err == nil && credentials != nil &&
		credentials != mutualTLS.snapshot.Load().credentials {
		if reloader, ok := mutualTLS.gate.revocation.(RevocationReloader); ok {
			_ = reloader.Reload()
		}
		_ = mutualTLS.publish(credentials)
	}
	return mutualTLS.snapshot.Load().config
}

func (mutualTLS *MutualTLSClient) publish(credentials *Credentials) error {
	clientCertificate, ok := credentials.ClientCertificate()
	if !ok {
		return ErrCredentialRotationFailed
	}
	// The pinned Core identity and the fail-closed revocation check are applied to
	// the server certificate as an additional gate on top of standard chain
	// verification. There is no InsecureSkipVerify path.
	config := &tls.Config{
		MinVersion:            credentials.minimumTLSVersion,
		RootCAs:               credentials.TrustPool(),
		Certificates:          []tls.Certificate{clientCertificate},
		ServerName:            mutualTLS.serverName,
		VerifyPeerCertificate: mutualTLS.gate.verify,
	}
	mutualTLS.mutex.Lock()
	defer mutualTLS.mutex.Unlock()
	mutualTLS.snapshot.Store(&mutualTLSClientState{credentials: credentials, config: config})
	return nil
}

// mutualTLSUniformResourceIdentifiers returns the verified uniform resource
// identifiers of a peer certificate as opaque strings for identity comparison.
func mutualTLSUniformResourceIdentifiers(certificate *x509.Certificate) []string {
	identifiers := make([]string, 0, len(certificate.URIs))
	for _, identifier := range certificate.URIs {
		identifiers = append(identifiers, identifier.String())
	}
	return identifiers
}
