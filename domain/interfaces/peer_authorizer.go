package interfaces

// PeerAuthorizer decides whether a verified mutual-TLS peer identity may call
// the plugin lifecycle API. It is deliberately policy-agnostic: the operator
// supplies the expected identity, and the SDK enforces per-replica mutual TLS
// with no plaintext and no bearer-only downgrade path.
type PeerAuthorizer interface {
	// AuthorizePeer reports whether the verified certificate common name and
	// the verified uniform resource identifiers are an accepted Core peer.
	AuthorizePeer(commonName string, uniformResourceIdentifiers []string) bool
}
