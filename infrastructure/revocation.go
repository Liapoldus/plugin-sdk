package infrastructure

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"sort"
	"sync"
	"time"

	"liapoldus.local/plugin-sdk/domain/interfaces"
)

var (
	// ErrRevocationUnavailable reports that the revocation set could not be
	// evaluated: nothing is configured, a configured list is unreadable, a list is
	// unparsable, or a list is outside its validity window. It is always fatal,
	// because the alternative would be admitting a peer that cannot be re-checked.
	ErrRevocationUnavailable = errors.New("unavailable Plugin SDK revocation state")
	// ErrInvalidRevocationSet reports configured revocation material that is not
	// usable as trust material, for example an unparsable list or a list that no
	// trusted authority signed.
	ErrInvalidRevocationSet = errors.New("invalid Plugin SDK revocation set")
	// ErrRevocationQueryRejected reports a revocation query for a certificate
	// serial that cannot be evaluated. It is treated as a failure, never as an
	// absent entry.
	ErrRevocationQueryRejected = errors.New("rejected Plugin SDK revocation query")
)

// RevocationPolicy selects how a replica behaves while the revocation set cannot
// be evaluated.
type RevocationPolicy int

const (
	// RevocationFailClosed refuses the peer. It is the zero value and the only
	// posture described by the fail-closed requirement in the versioned contract.
	RevocationFailClosed RevocationPolicy = iota
	// RevocationPermitAbsent admits the peer only when the operator configured no
	// revocation list at all. A configured but unreadable, unparsable, unsigned or
	// expired list still fails closed, and every connection is still pinned to a
	// single expected peer identity. It is a weaker posture than the contract
	// requires and is only reachable through this explicit flag.
	RevocationPermitAbsent
)

// RevocationReloader is the optional rotation seam of a revocation source. The
// plugin-side server calls it on refresh so a rotated list takes effect without
// restarting the process.
type RevocationReloader interface {
	Reload() error
}

// RevocationConfiguration describes operator-supplied revocation material. The
// trusted authorities are mandatory: a certificate revocation list is trust
// material only when an authority the operator already trusts has signed it.
type RevocationConfiguration struct {
	// Authorities are the operator trust roots, as parsed. Obtain them from
	// Credentials.TrustAuthorities so the list and the connection use one trust
	// decision.
	Authorities []*x509.Certificate
	// Files are certificate revocation list files to watch. Each file is re-read
	// on Reload.
	Files []string
	// Bundles are already-read revocation lists supplied by the caller, in PEM or
	// DER form.
	Bundles [][]byte
	// Policy defaults to RevocationFailClosed because it is the zero value.
	Policy RevocationPolicy
	// Clock supplies the current time for the ThisUpdate and NextUpdate window.
	Clock interfaces.Clock
}

// Revocation is a certificate revocation list source that never guesses. It
// indexes the revoked serial numbers of every configured list, remembers the
// authority key identifier of each list, and honours each list's validity
// window. An unavailable, unparsable, unsigned or expired set makes Revoked
// return an error rather than false, so every caller fails closed.
type Revocation struct {
	policy      RevocationPolicy
	authorities []*x509.Certificate
	clock       interfaces.Clock
	files       []string
	bundles     [][]byte
	lists       []revocationList
	unavailable error
	mutex       sync.RWMutex
}

type revocationList struct {
	authorityKeyID string
	thisUpdate     time.Time
	nextUpdate     time.Time
	revoked        map[string]struct{}
}

// NewRevocation parses and validates every configured revocation list. It fails
// when a configured list cannot be read, parsed or attributed to a trusted
// authority, so a replica never starts believing it is protected by a list that
// was silently discarded.
func NewRevocation(configuration RevocationConfiguration) (*Revocation, error) {
	if len(configuration.Authorities) == 0 {
		return nil, ErrInvalidRevocationSet
	}
	if configuration.Policy != RevocationFailClosed && configuration.Policy != RevocationPermitAbsent {
		return nil, ErrInvalidRevocationSet
	}
	revocation := &Revocation{
		policy:      configuration.Policy,
		authorities: append([]*x509.Certificate(nil), configuration.Authorities...),
		clock:       configuration.Clock,
		files:       append([]string(nil), configuration.Files...),
		bundles:     append([][]byte(nil), configuration.Bundles...),
	}
	if err := revocation.loadLocked(); err != nil {
		return nil, err
	}
	return revocation, nil
}

// Revoked reports whether a certificate serial number appears in any configured
// list that is currently inside its validity window. It returns an error whenever
// the answer cannot be established, so a caller must treat a non-nil error as a
// refusal rather than as an absent entry.
func (revocation *Revocation) Revoked(serialNumber []byte) (bool, error) {
	if revocation == nil || len(serialNumber) == 0 {
		return false, ErrRevocationQueryRejected
	}
	revocation.mutex.RLock()
	lists := revocation.lists
	policy := revocation.policy
	unavailable := revocation.unavailable
	revocation.mutex.RUnlock()
	// A reload that could not rebuild the index leaves the replica unable to
	// re-check a peer, so the failure is reported on every query until a reload
	// succeeds. A stale index is never reported as a clean answer.
	if unavailable != nil {
		return false, unavailable
	}
	if len(lists) == 0 {
		if policy == RevocationPermitAbsent {
			return false, nil
		}
		return false, ErrRevocationUnavailable
	}
	now := clockNow(revocation.clock)
	serial := revocationSerialKey(serialNumber)
	for _, list := range lists {
		if now.Before(list.thisUpdate) || (!list.nextUpdate.IsZero() && now.After(list.nextUpdate)) {
			return false, ErrRevocationUnavailable
		}
		if _, revoked := list.revoked[serial]; revoked {
			return true, nil
		}
	}
	return false, nil
}

// Reload re-reads the configured files and rebuilds the index, so a rotated list
// takes effect without restarting the process.
//
// A failed reload keeps the previous index for reference but marks the source
// unavailable, and every Revoked query then returns an error. Restoring the
// previous index is not a silent fallback to a stale answer: the replica refuses
// peers until a reload succeeds.
func (revocation *Revocation) Reload() error {
	if revocation == nil {
		return ErrRevocationUnavailable
	}
	revocation.mutex.Lock()
	defer revocation.mutex.Unlock()
	previous := revocation.lists
	if err := revocation.loadLocked(); err != nil {
		revocation.lists = previous
		revocation.unavailable = err
		return err
	}
	revocation.unavailable = nil
	return nil
}

// ListCount reports how many revocation lists are currently enforced. It is zero
// while the source is unavailable, because a retained-but-unreloaded index is not
// being enforced and must not be reported as protection.
func (revocation *Revocation) ListCount() int {
	if revocation == nil {
		return 0
	}
	revocation.mutex.RLock()
	defer revocation.mutex.RUnlock()
	if revocation.unavailable != nil {
		return 0
	}
	return len(revocation.lists)
}

// Available reports whether the revocation set can currently be evaluated. A false
// result means every connection attempt will be refused.
func (revocation *Revocation) Available() bool {
	if revocation == nil {
		return false
	}
	revocation.mutex.RLock()
	defer revocation.mutex.RUnlock()
	return revocation.unavailable == nil
}

// AuthorityKeyIDs returns the sorted authority key identifier of every indexed
// list, so an operator can confirm which issuers the replica enforces. A list
// that carries no authority key identifier contributes an empty entry.
func (revocation *Revocation) AuthorityKeyIDs() []string {
	if revocation == nil {
		return nil
	}
	revocation.mutex.RLock()
	defer revocation.mutex.RUnlock()
	if revocation.unavailable != nil {
		return nil
	}
	identifiers := make([]string, 0, len(revocation.lists))
	for _, list := range revocation.lists {
		identifiers = append(identifiers, list.authorityKeyID)
	}
	sort.Strings(identifiers)
	return identifiers
}

func (revocation *Revocation) loadLocked() error {
	lists := make([]revocationList, 0, len(revocation.files)+len(revocation.bundles))
	for _, bundle := range revocation.bundles {
		list, err := revocationParseList(bundle, revocation.authorities)
		if err != nil {
			return err
		}
		lists = append(lists, list)
	}
	for _, path := range revocation.files {
		contents, err := os.ReadFile(path)
		if err != nil || len(contents) == 0 {
			return ErrRevocationUnavailable
		}
		list, err := revocationParseList(contents, revocation.authorities)
		if err != nil {
			return err
		}
		lists = append(lists, list)
	}
	revocation.lists = lists
	revocation.unavailable = nil
	return nil
}

func revocationParseList(contents []byte, authorities []*x509.Certificate) (revocationList, error) {
	parsed, err := x509.ParseRevocationList(revocationDER(contents))
	if err != nil {
		return revocationList{}, ErrInvalidRevocationSet
	}
	if err := revocationVerifySignature(parsed, authorities); err != nil {
		return revocationList{}, err
	}
	if parsed.ThisUpdate.IsZero() {
		return revocationList{}, ErrInvalidRevocationSet
	}
	list := revocationList{
		thisUpdate: parsed.ThisUpdate,
		nextUpdate: parsed.NextUpdate,
		revoked:    make(map[string]struct{}, len(parsed.RevokedCertificateEntries)),
	}
	if len(parsed.AuthorityKeyId) > 0 {
		list.authorityKeyID = string(parsed.AuthorityKeyId)
	}
	for _, entry := range parsed.RevokedCertificateEntries {
		if entry.SerialNumber == nil || entry.SerialNumber.Sign() <= 0 {
			return revocationList{}, ErrInvalidRevocationSet
		}
		list.revoked[entry.SerialNumber.Text(16)] = struct{}{}
	}
	return list, nil
}

// revocationDER accepts either a PEM-encoded revocation list or a raw DER one. A
// PEM block that is not a revocation list is a configuration error rather than a
// reason to fall through to an unverified DER parse.
func revocationDER(contents []byte) []byte {
	block, _ := pem.Decode(contents)
	if block == nil {
		return contents
	}
	switch block.Type {
	case "X509 CRL", "CRL":
		return block.Bytes
	}
	return contents
}

// revocationVerifySignature requires a trusted authority to have signed the
// list. An attacker-planted list can then neither revoke a peer nor hide a
// revoked one, because an untrusted list is never indexed at all.
func revocationVerifySignature(list *x509.RevocationList, authorities []*x509.Certificate) error {
	for _, authority := range authorities {
		if !bytes.Equal(authority.RawSubject, list.RawIssuer) {
			continue
		}
		// The verification error is intentionally discarded: x509 error strings
		// embed subject names, so the SDK reports its own opaque failure.
		if err := list.CheckSignatureFrom(authority); err == nil {
			return nil
		}
	}
	return ErrInvalidRevocationSet
}

func revocationSerialKey(serialNumber []byte) string {
	return new(big.Int).SetBytes(serialNumber).Text(16)
}

func clockNow(clock interfaces.Clock) time.Time {
	if clock == nil {
		return time.Now()
	}
	return clock.Now()
}
