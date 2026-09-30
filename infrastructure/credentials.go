package infrastructure

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"sync"
	"time"
)

var (
	// ErrInvalidCredentials rejects credential material that is absent,
	// ambiguous, unparsable, issued by an unknown authority, used for the wrong
	// role, or not matched by the presented private key. The cause is
	// deliberately never attached: a credential failure must not echo certificate
	// bytes, key bytes or subject names into a log or an error.
	ErrInvalidCredentials = errors.New("invalid Plugin SDK TLS credentials")
	// ErrWeakCredentialKey rejects a syntactically valid but too small private
	// key before it can be offered to a TLS handshake.
	ErrWeakCredentialKey = errors.New("weak Plugin SDK TLS private key")
	// ErrCredentialsUnavailable reports that operator-supplied credential
	// material could not be read or reloaded from its source.
	ErrCredentialsUnavailable = errors.New("unavailable Plugin SDK TLS credentials")
)

const (
	credentialsMinimumRSABits = 2048
	// credentialsMaximumMaterialBytes bounds one operator-supplied PEM file. It
	// is a defensive read bound for operator material, not a contract limit.
	credentialsMaximumMaterialBytes = 1 << 20
)

// CredentialsMaterial is the operator-supplied TLS material for one replica. The
// operator owns the trust roots: the SDK embeds no CA, generates no
// certificate and falls back to no certificate. Every field is either an
// explicit file path or already-parsed PEM bytes supplied by the caller; a role
// that sets both is ambiguous and rejected rather than silently preferred.
type CredentialsMaterial struct {
	// CAFile or CABundle supplies the trust roots. One of the two is required and
	// at least one certificate must be a valid authority.
	CAFile   string
	CABundle []byte

	// ServerCertificateFile/ServerCertificatePEM and ServerKeyFile/ServerKeyPEM
	// are the plugin-side keypair. Both are required.
	ServerCertificateFile string
	ServerKeyFile         string
	ServerCertificatePEM  []byte
	ServerKeyPEM          []byte

	// ClientCertificateFile/ClientCertificatePEM and
	// ClientKeyFile/ClientKeyPEM are the Core-side keypair. They are required
	// only by the plugin-to-Core client and optional elsewhere.
	ClientCertificateFile string
	ClientKeyFile         string
	ClientCertificatePEM  []byte
	ClientKeyPEM          []byte
}

// Credentials is validated, ready-to-serve TLS material for one replica. It
// holds no PEM in any exported field: callers receive parsed keypairs, a trust
// pool and the contract minimum TLS version. A Credentials value is immutable
// once built, except for Zeroize, which the owner must not race with handshakes
// that still reference the parsed key objects.
type Credentials struct {
	minimumTLSVersion uint16
	trust             *x509.CertPool
	authorities       []*x509.Certificate
	server            *tls.Certificate
	client            *tls.Certificate
	keyMaterial       [][]byte
	mutex             sync.RWMutex
}

// LoadCredentials parses, validates and returns operator-supplied credential
// material. It rejects a material set unless the trust pool is non-empty, every
// keypair's leaf certificate chains to that pool for the role it must serve, the
// leaf permits digital signature plus the role's extended key usage, the
// presented private key matches the leaf's public key, and the key is at least
// 2048-bit RSA, P-256-or-larger ECDSA or Ed25519. Symmetric and undersized keys
// are refused. The returned credentials never retain the caller's PEM slices.
func LoadCredentials(contract HTTPContract, material CredentialsMaterial) (*Credentials, error) {
	if contract.TransportSecurity.MinimumTLSVersion < tls.VersionTLS12 {
		return nil, ErrInvalidCredentials
	}
	resolved, err := resolveCredentialMaterial(material)
	if err != nil {
		return nil, err
	}
	trust, authorities, err := credentialsTrustPool(resolved.caPEM)
	if err != nil {
		return nil, err
	}
	credentials := &Credentials{
		minimumTLSVersion: contract.TransportSecurity.MinimumTLSVersion,
		trust:             trust,
		authorities:       authorities,
	}
	server, err := credentialsKeyPair(resolved.serverCertificatePEM, resolved.serverKeyPEM, trust, x509.ExtKeyUsageServerAuth)
	if err != nil {
		return nil, err
	}
	credentials.server = &server
	if len(resolved.clientCertificatePEM) > 0 || len(resolved.clientKeyPEM) > 0 {
		client, err := credentialsKeyPair(resolved.clientCertificatePEM, resolved.clientKeyPEM, trust, x509.ExtKeyUsageClientAuth)
		if err != nil {
			return nil, err
		}
		credentials.client = &client
	}
	credentials.keyMaterial = ownedKeyMaterial(resolved.serverKeyPEM, resolved.clientKeyPEM)
	return credentials, nil
}

// MinimumTLSVersion reports the contract floor that both the server and the
// client must apply. It is never lower than TLS 1.2.
func (credentials *Credentials) MinimumTLSVersion() uint16 {
	if credentials == nil {
		return 0
	}
	return credentials.minimumTLSVersion
}

// TrustPool returns a copy of the operator trust pool. A copy keeps a caller
// from widening or emptying the roots the SDK verifies against.
func (credentials *Credentials) TrustPool() *x509.CertPool {
	if credentials == nil || credentials.trust == nil {
		return nil
	}
	return credentials.trust.Clone()
}

// TrustAuthorityCount reports how many authorities the operator supplied. The
// SDK never accepts an empty pool.
func (credentials *Credentials) TrustAuthorityCount() int {
	if credentials == nil {
		return 0
	}
	return len(credentials.authorities)
}

// TrustAuthorities returns the parsed operator trust roots. A certificate
// revocation list is accepted as trust material only when one of these signed
// it, so the connection trust decision and the revocation trust decision can
// never be made against different roots.
func (credentials *Credentials) TrustAuthorities() []*x509.Certificate {
	if credentials == nil {
		return nil
	}
	return append([]*x509.Certificate(nil), credentials.authorities...)
}

// ServerCertificate returns the plugin-side keypair used for TLS server
// authentication. The second result is false when no server keypair is present.
func (credentials *Credentials) ServerCertificate() (tls.Certificate, bool) {
	if credentials == nil || credentials.server == nil {
		return tls.Certificate{}, false
	}
	return *credentials.server, true
}

// ClientCertificate returns the Core-side keypair used for TLS client
// authentication. The second result is false when no client keypair is present.
func (credentials *Credentials) ClientCertificate() (tls.Certificate, bool) {
	if credentials == nil || credentials.client == nil {
		return tls.Certificate{}, false
	}
	return *credentials.client, true
}

// Zeroize overwrites the retained private key PEM copies. The SDK keeps exactly
// one private copy of each key so this method has something real to destroy. The
// parsed key objects handed to crypto/tls are Go allocations that cannot be
// wiped in place, so callers that require stronger guarantees must unload the
// process; this method never leaves retrievable key PEM behind.
func (credentials *Credentials) Zeroize() {
	if credentials == nil {
		return
	}
	credentials.mutex.Lock()
	defer credentials.mutex.Unlock()
	for _, material := range credentials.keyMaterial {
		for index := range material {
			material[index] = 0
		}
	}
	credentials.keyMaterial = nil
}

// CredentialsProvider supplies the credential material currently in force. A
// returned error is fatal for the caller: the SDK never continues with
// plaintext, with an anonymous connection, or with a weakened configuration.
type CredentialsProvider interface {
	Credentials() (*Credentials, error)
}

// StaticCredentialsProvider serves one already-validated credential set. It is
// the provider to use when the operator supplies material once and never
// rotates it.
type StaticCredentialsProvider struct {
	credentials *Credentials
}

// NewStaticCredentialsProvider returns a provider over one validated credential
// set.
func NewStaticCredentialsProvider(credentials *Credentials) (*StaticCredentialsProvider, error) {
	if credentials == nil || credentials.trust == nil || credentials.server == nil {
		return nil, ErrInvalidCredentials
	}
	return &StaticCredentialsProvider{credentials: credentials}, nil
}

// Credentials returns the one credential set. It never fails after construction.
func (provider *StaticCredentialsProvider) Credentials() (*Credentials, error) {
	if provider == nil || provider.credentials == nil {
		return nil, ErrCredentialsUnavailable
	}
	return provider.credentials, nil
}

// Zeroize destroys the retained private key PEM copies of the served set.
func (provider *StaticCredentialsProvider) Zeroize() {
	if provider != nil {
		provider.credentials.Zeroize()
	}
}

// FileCredentialsProvider reloads credential material from operator files when
// a watched file's size or modification time changes, so a rotation performed on
// disk takes effect without restarting the plugin process. A reload that fails
// is reported as an error and is not retried until a watched file changes again;
// the provider never silently serves material it could not validate.
type FileCredentialsProvider struct {
	contract   HTTPContract
	material   CredentialsMaterial
	mutex      sync.Mutex
	current    *Credentials
	observed   credentialFingerprint
	attempted  bool
	attemptErr error
}

// NewFileCredentialsProvider returns a provider that reloads from disk. It
// validates only the shape of the material, so construction succeeds before the
// operator has written the files; the first Credentials call does the real
// validation.
func NewFileCredentialsProvider(contract HTTPContract, material CredentialsMaterial) (*FileCredentialsProvider, error) {
	if contract.TransportSecurity.MinimumTLSVersion < tls.VersionTLS12 {
		return nil, ErrInvalidCredentials
	}
	if err := credentialMaterialShape(material); err != nil {
		return nil, err
	}
	return &FileCredentialsProvider{contract: contract, material: material}, nil
}

// Credentials returns the credential set in force. It reloads and revalidates
// whenever a watched file's size or modification time changed, and returns
// ErrCredentialsUnavailable when the material cannot be read or validated.
//
// A failed reload is sticky: the same failure is reported until a watched input
// changes again, so a caller can never observe a rotation failure once and then
// be handed the superseded set as if it were current.
func (provider *FileCredentialsProvider) Credentials() (*Credentials, error) {
	if provider == nil {
		return nil, ErrCredentialsUnavailable
	}
	provider.mutex.Lock()
	defer provider.mutex.Unlock()
	observed := provider.materialFingerprint()
	if provider.attempted && observed.equal(provider.observed) {
		if provider.attemptErr != nil {
			return nil, provider.attemptErr
		}
		return provider.current, nil
	}
	loaded, err := LoadCredentials(provider.contract, provider.material)
	provider.observed = observed
	provider.attempted = true
	if err != nil {
		provider.attemptErr = err
		return nil, err
	}
	provider.attemptErr = nil
	previous := provider.current
	provider.current = loaded
	previous.Zeroize()
	return loaded, nil
}

// Zeroize destroys the retained private key PEM copies of the current set.
func (provider *FileCredentialsProvider) Zeroize() {
	if provider == nil {
		return
	}
	provider.mutex.Lock()
	defer provider.mutex.Unlock()
	provider.current.Zeroize()
	provider.current = nil
	provider.attempted = false
}

// materialFingerprint records every watched input. A file that is absent or
// unreadable is fingerprinted as missing rather than as an error, so a fixed
// file is picked up on the next call instead of being retried on every
// handshake.
func (provider *FileCredentialsProvider) materialFingerprint() credentialFingerprint {
	material := provider.material
	fingerprint := credentialFingerprint{observed: true}
	for _, path := range []string{
		material.CAFile,
		material.ServerCertificateFile,
		material.ServerKeyFile,
		material.ClientCertificateFile,
		material.ClientKeyFile,
	} {
		if path == "" {
			continue
		}
		entry := credentialFileStamp{present: true, name: path}
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			entry.size = info.Size()
			entry.modified = info.ModTime().UnixNano()
		} else {
			entry.present = false
		}
		fingerprint.files = append(fingerprint.files, entry)
	}
	for _, inline := range [][]byte{
		material.CABundle,
		material.ServerCertificatePEM,
		material.ServerKeyPEM,
		material.ClientCertificatePEM,
		material.ClientKeyPEM,
	} {
		fingerprint.inline = append(fingerprint.inline, len(inline))
	}
	return fingerprint
}

type credentialFileStamp struct {
	name     string
	size     int64
	modified int64
	present  bool
}

// credentialFingerprint is the cheap rotation signal: file identity, size and
// modification time plus the length of every inline input. A content change
// that preserves all of those is not detected, which is the documented cost of
// avoiding a full re-parse on every handshake.
type credentialFingerprint struct {
	observed bool
	files    []credentialFileStamp
	inline   []int
}

func (fingerprint credentialFingerprint) equal(other credentialFingerprint) bool {
	if fingerprint.observed != other.observed || len(fingerprint.files) != len(other.files) ||
		len(fingerprint.inline) != len(other.inline) {
		return false
	}
	for index, entry := range fingerprint.files {
		if entry != other.files[index] {
			return false
		}
	}
	for index, length := range fingerprint.inline {
		if length != other.inline[index] {
			return false
		}
	}
	return true
}

type resolvedCredentialMaterial struct {
	caPEM                []byte
	serverCertificatePEM []byte
	serverKeyPEM         []byte
	clientCertificatePEM []byte
	clientKeyPEM         []byte
}

// credentialMaterialShape rejects a material set that could never be usable,
// without reading any file. It is what the file-backed provider can check before
// the operator has written anything to disk.
func credentialMaterialShape(material CredentialsMaterial) error {
	roles := []struct {
		path   string
		inline []byte
		needed bool
	}{
		{material.CAFile, material.CABundle, true},
		{material.ServerCertificateFile, material.ServerCertificatePEM, true},
		{material.ServerKeyFile, material.ServerKeyPEM, true},
		{material.ClientCertificateFile, material.ClientCertificatePEM, false},
		{material.ClientKeyFile, material.ClientKeyPEM, false},
	}
	for _, role := range roles {
		// Both inputs for one role is an ambiguity the SDK refuses rather than
		// silently prefers.
		if role.path != "" && len(role.inline) > 0 {
			return ErrInvalidCredentials
		}
		if role.path == "" && len(role.inline) == 0 && role.needed {
			return ErrInvalidCredentials
		}
	}
	// Half a client keypair is never usable, and silently ignoring one half would
	// hide an operator mistake until the first Core call.
	hasClientCertificate := material.ClientCertificateFile != "" || len(material.ClientCertificatePEM) > 0
	hasClientKey := material.ClientKeyFile != "" || len(material.ClientKeyPEM) > 0
	if hasClientCertificate != hasClientKey {
		return ErrInvalidCredentials
	}
	return nil
}

func resolveCredentialMaterial(material CredentialsMaterial) (resolvedCredentialMaterial, error) {
	var resolved resolvedCredentialMaterial
	var err error
	if resolved.caPEM, err = credentialMaterialBytes(material.CAFile, material.CABundle, true); err != nil {
		return resolvedCredentialMaterial{}, err
	}
	if resolved.serverCertificatePEM, err = credentialMaterialBytes(material.ServerCertificateFile, material.ServerCertificatePEM, true); err != nil {
		return resolvedCredentialMaterial{}, err
	}
	if resolved.serverKeyPEM, err = credentialMaterialBytes(material.ServerKeyFile, material.ServerKeyPEM, true); err != nil {
		return resolvedCredentialMaterial{}, err
	}
	if resolved.clientCertificatePEM, err = credentialMaterialBytes(material.ClientCertificateFile, material.ClientCertificatePEM, false); err != nil {
		return resolvedCredentialMaterial{}, err
	}
	if resolved.clientKeyPEM, err = credentialMaterialBytes(material.ClientKeyFile, material.ClientKeyPEM, false); err != nil {
		return resolvedCredentialMaterial{}, err
	}
	// Half a client keypair is never usable, and silently ignoring one half would
	// hide an operator mistake until the first Core call.
	if (len(resolved.clientCertificatePEM) == 0) != (len(resolved.clientKeyPEM) == 0) {
		return resolvedCredentialMaterial{}, ErrInvalidCredentials
	}
	return resolved, nil
}

func credentialMaterialBytes(path string, inline []byte, required bool) ([]byte, error) {
	if path != "" && len(inline) > 0 {
		return nil, ErrInvalidCredentials
	}
	if path == "" {
		if len(inline) == 0 {
			if required {
				return nil, ErrInvalidCredentials
			}
			return nil, nil
		}
		return inline, nil
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > credentialsMaximumMaterialBytes {
		return nil, ErrCredentialsUnavailable
	}
	contents, err := os.ReadFile(path)
	if err != nil || len(contents) == 0 {
		return nil, ErrCredentialsUnavailable
	}
	return contents, nil
}

// ownedKeyMaterial copies the key PEM so the SDK owns the only retained key
// bytes. Zeroize can then destroy a private buffer without mutating memory the
// caller still owns.
func ownedKeyMaterial(materials ...[]byte) [][]byte {
	owned := make([][]byte, 0, len(materials))
	for _, material := range materials {
		if len(material) == 0 {
			continue
		}
		owned = append(owned, append([]byte(nil), material...))
	}
	return owned
}

func credentialsTrustPool(caPEM []byte) (*x509.CertPool, []*x509.Certificate, error) {
	pool := x509.NewCertPool()
	authorities := make([]*x509.Certificate, 0, 4)
	rest := caPEM
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		authority, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !authority.IsCA || !authority.BasicConstraintsValid {
			return nil, nil, ErrInvalidCredentials
		}
		pool.AddCert(authority)
		authorities = append(authorities, authority)
	}
	if len(authorities) == 0 {
		return nil, nil, ErrInvalidCredentials
	}
	return pool, authorities, nil
}

func credentialsKeyPair(certificatePEM, keyPEM []byte, trust *x509.CertPool, usage x509.ExtKeyUsage) (tls.Certificate, error) {
	chain, leaf, err := credentialsCertificateChain(certificatePEM)
	if err != nil {
		return tls.Certificate{}, err
	}
	if !credentialsUsableForRole(leaf, usage) || !credentialsTrustedForRole(leaf, chain, trust, usage) {
		return tls.Certificate{}, ErrInvalidCredentials
	}
	signer, err := credentialsPrivateKey(keyPEM)
	if err != nil {
		return tls.Certificate{}, err
	}
	if !credentialsKeyMatches(leaf, signer) {
		return tls.Certificate{}, ErrInvalidCredentials
	}
	keypair := tls.Certificate{PrivateKey: signer, Leaf: leaf}
	keypair.Certificate = append(keypair.Certificate, leaf.Raw)
	for _, intermediate := range chain[1:] {
		keypair.Certificate = append(keypair.Certificate, intermediate.Raw)
	}
	return keypair, nil
}

func credentialsCertificateChain(certificatePEM []byte) ([]*x509.Certificate, *x509.Certificate, error) {
	chain := make([]*x509.Certificate, 0, 2)
	rest := certificatePEM
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, nil, ErrInvalidCredentials
		}
		chain = append(chain, certificate)
	}
	if len(chain) == 0 {
		return nil, nil, ErrInvalidCredentials
	}
	return chain, chain[0], nil
}

func credentialsUsableForRole(leaf *x509.Certificate, usage x509.ExtKeyUsage) bool {
	// The contract floor is TLS 1.3, where every connection is authenticated by
	// signature, so digital signature is mandatory for both roles.
	if leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return false
	}
	for _, candidate := range leaf.ExtKeyUsage {
		if candidate == usage || candidate == x509.ExtKeyUsageAny {
			return true
		}
	}
	return false
}

func credentialsTrustedForRole(leaf *x509.Certificate, chain []*x509.Certificate, trust *x509.CertPool, usage x509.ExtKeyUsage) bool {
	intermediates := x509.NewCertPool()
	for _, intermediate := range chain[1:] {
		intermediates.AddCert(intermediate)
	}
	// The verification error is intentionally discarded: x509 error strings can
	// embed subject names, so the SDK reports its own opaque failure instead.
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         trust,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{usage},
		CurrentTime:   time.Now(),
	}); err != nil {
		return false
	}
	return true
}

func credentialsPrivateKey(keyPEM []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, ErrInvalidCredentials
	}
	var (
		parsed any
		err    error
	)
	if block.Type == "PRIVATE KEY" {
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	} else {
		// PKCS#1 and SEC1 share a single generic PEM header, so the RSA form is
		// tried first and the elliptic form second.
		parsed, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			parsed, err = x509.ParseECPrivateKey(block.Bytes)
		}
	}
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, ErrInvalidCredentials
	}
	if err := credentialsStrongEnough(signer); err != nil {
		return nil, err
	}
	return signer, nil
}

func credentialsStrongEnough(signer crypto.Signer) error {
	switch key := signer.(type) {
	case *rsa.PrivateKey:
		if key.N.BitLen() < credentialsMinimumRSABits {
			return ErrWeakCredentialKey
		}
		if err := key.Validate(); err != nil {
			return ErrInvalidCredentials
		}
	case *ecdsa.PrivateKey:
		switch key.Curve {
		case elliptic.P256(), elliptic.P384(), elliptic.P521():
		default:
			return ErrWeakCredentialKey
		}
	case ed25519.PrivateKey:
		if len(key) != ed25519.PrivateKeySize {
			return ErrInvalidCredentials
		}
	default:
		return ErrInvalidCredentials
	}
	return nil
}

func credentialsKeyMatches(leaf *x509.Certificate, signer crypto.Signer) bool {
	switch published := leaf.PublicKey.(type) {
	case *rsa.PublicKey:
		derived, ok := signer.Public().(*rsa.PublicKey)
		return ok && derived.N.Cmp(published.N) == 0 && derived.E == published.E
	case *ecdsa.PublicKey:
		derived, ok := signer.Public().(*ecdsa.PublicKey)
		// Equal is the supported comparison and it already covers the curve as
		// well as both coordinates, so no coordinate is read here.
		return ok && derived.Equal(published)
	case ed25519.PublicKey:
		derived, ok := signer.Public().(ed25519.PublicKey)
		return ok && derived.Equal(published)
	}
	return false
}
