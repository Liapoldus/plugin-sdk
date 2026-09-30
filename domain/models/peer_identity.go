package models

import (
	"net/url"
	"strings"
)

// PeerIdentity is the expected certificate identity of a Core management
// replica on the Core-to-plugin REST connection. The plugin accepts no peer that
// matches neither the common name nor the uniform resource identifier. Both
// fields are operator-registered values, never values discovered from the wire.
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

// Valid requires an explicit common name plus an optional absolute URI. An
// empty allowlist entry is never a wildcard.
func (identity PeerIdentity) Valid() bool {
	if identity.CommonName == "" || len(identity.CommonName) > identifierMaximumBytes ||
		strings.TrimSpace(identity.CommonName) != identity.CommonName {
		return false
	}
	if identity.UniformResourceIdentifier == "" {
		return true
	}
	parsed, err := url.Parse(identity.UniformResourceIdentifier)
	return err == nil && parsed.IsAbs() && parsed.Scheme != "" && parsed.Host != "" &&
		parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" &&
		strings.HasPrefix(parsed.Path, "/") &&
		len(identity.UniformResourceIdentifier) <= identifierMaximumBytes
}

// Matches reports whether a verified certificate identity is the expected peer.
// Comparison is exact and byte-for-byte on both candidate fields.
func (identity PeerIdentity) Matches(commonName string, uniformResourceIdentifiers []string) bool {
	if !identity.Valid() {
		return false
	}
	if commonName == identity.CommonName {
		return true
	}
	for _, candidate := range uniformResourceIdentifiers {
		if candidate != "" && candidate == identity.UniformResourceIdentifier {
			return true
		}
	}
	return false
}
