package models

import "errors"

// ErrInvalidIdentity is returned when a replica identity or an expected peer
// identity violates the contract syntax rules.
var ErrInvalidIdentity = errors.New("invalid plugin replica identity")
