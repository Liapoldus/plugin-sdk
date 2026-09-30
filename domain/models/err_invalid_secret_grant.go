package models

import "errors"

// ErrInvalidSecretGrant is returned when a secret reference, grant or redemption
// violates the contract syntax rules. It never carries the handle or the value.
var ErrInvalidSecretGrant = errors.New("invalid plugin secret grant")
