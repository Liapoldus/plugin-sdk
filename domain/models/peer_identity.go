package models

import (
	"net/url"
	"strings"
)

// PeerIdentity is the expected certificate identity of one REST peer. A peer
// may be pinned by common name, URI SAN, or both. Configured values are
// operator-registered; values discovered from the wire never become expected
// identity implicitly.
type PeerIdentity struct {
	CommonName                string
	UniformResourceIdentifier string
}

func NewPeerIdentity(commonName, uniformResourceIdentifier string) (PeerIdentity, error) {
	identity := PeerIdentity{CommonName: commonName, UniformResourceIdentifier: uniformResourceIdentifier}
	if !identity.Valid() {
		return PeerIdentity{}, ErrInvalidIdentity
	}
	return identity, nil
}

// Valid requires at least one explicit identity field. An absent field is not a
// wildcard, and an empty identity is never a valid allowlist entry.
func (identity PeerIdentity) Valid() bool {
	if identity.CommonName != "" && (len(identity.CommonName) > identifierMaximumBytes ||
		strings.TrimSpace(identity.CommonName) != identity.CommonName) {
		return false
	}
	if identity.CommonName == "" && identity.UniformResourceIdentifier == "" {
		return false
	}
	if identity.UniformResourceIdentifier == "" {
		return true
	}
	if len(identity.UniformResourceIdentifier) > identifierMaximumBytes {
		return false
	}
	parsed, err := url.Parse(identity.UniformResourceIdentifier)
	return err == nil && parsed.IsAbs() && parsed.Scheme != "" && parsed.Host != "" &&
		parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" &&
		strings.HasPrefix(parsed.Path, "/")
}

// Matches reports whether a verified certificate identity is the expected peer.
// Comparison is exact and byte-for-byte on both candidate fields.
func (identity PeerIdentity) Matches(commonName string, uniformResourceIdentifiers []string) bool {
	if !identity.Valid() {
		return false
	}
	if identity.CommonName != "" && commonName == identity.CommonName {
		return true
	}
	if identity.UniformResourceIdentifier != "" {
		for _, candidate := range uniformResourceIdentifiers {
			if candidate != "" && candidate == identity.UniformResourceIdentifier {
				return true
			}
		}
	}
	return false
}
