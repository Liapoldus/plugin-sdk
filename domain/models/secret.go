package models

import "time"

// SecretReference is an opaque pointer to a secret value. The SDK never
// interprets, resolves or stores the referenced value; it only scopes a grant.
type SecretReference struct {
	Reference string `json:"reference"`
}

func (reference SecretReference) Validate() error {
	if reference.Reference == "" || len(reference.Reference) > identifierMaximumBytes {
		return ErrInvalidSecretGrant
	}
	return nil
}

// SecretGrantRequest asks Core for a grant bound to this replica, the generation
// the plugin has applied, and a single declared purpose.
type SecretGrantRequest struct {
	Reference  string `json:"reference"`
	Purpose    string `json:"purpose"`
	Generation string `json:"generation"`
}

func (request SecretGrantRequest) Validate() error {
	if request.Reference == "" || len(request.Reference) > identifierMaximumBytes ||
		request.Purpose == "" || len(request.Purpose) > identifierMaximumBytes ||
		!ValidGeneration(request.Generation) {
		return ErrInvalidSecretGrant
	}
	return nil
}

// SecretGrant is the scoped, expiring, one-use authorization Core issued. The
// handle is an opaque bearer value: it is never logged, never placed in a metric
// label, never returned in an acknowledgement and never included in an error.
type SecretGrant struct {
	Handle     string    `json:"handle"`
	Reference  string    `json:"reference"`
	Purpose    string    `json:"purpose"`
	Generation string    `json:"generation"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

func (grant SecretGrant) Validate() error {
	if grant.Handle == "" || len(grant.Handle) > 4*identifierMaximumBytes ||
		grant.Reference == "" || grant.Purpose == "" || !ValidGeneration(grant.Generation) {
		return ErrInvalidSecretGrant
	}
	return nil
}

func (grant SecretGrant) Expired(now time.Time) bool {
	return !grant.ExpiresAt.After(now)
}

// SecretRedemption asks Core to exchange a grant for the referenced value.
type SecretRedemption struct {
	Handle string `json:"handle"`
}

func (redemption SecretRedemption) Validate() error {
	if redemption.Handle == "" || len(redemption.Handle) > 4*identifierMaximumBytes {
		return ErrInvalidSecretGrant
	}
	return nil
}

// SecretValue holds redeemed secret bytes in memory only. It has no String
// method so it cannot be interpolated into a log line by accident, it is never
// serialized by the SDK, and Destroy zeroes the buffer. The plugin drops it after
// the generation it belongs to is replaced or the process shuts down.
type SecretValue struct {
	contents []byte
}

func NewSecretValue(contents []byte) SecretValue {
	return SecretValue{contents: append([]byte(nil), contents...)}
}

func (value SecretValue) Bytes() []byte {
	return append([]byte(nil), value.contents...)
}

func (value *SecretValue) Destroy() {
	if value == nil {
		return
	}
	for index := range value.contents {
		value.contents[index] = 0
	}
	value.contents = nil
}
