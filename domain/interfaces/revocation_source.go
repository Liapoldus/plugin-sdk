package interfaces

// RevocationSource supplies the trust material that decides whether an already
// verified certificate must still be refused. Returning an error is fatal: the
// SDK fails closed rather than admitting a peer it cannot re-check.
type RevocationSource interface {
	// Revoked reports whether a certificate serial number is revoked. It must
	// return an error instead of a default when the revocation set is
	// unavailable, so the caller can fail closed.
	Revoked(serialNumber []byte) (bool, error)
}
