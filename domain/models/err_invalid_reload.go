package models

import "errors"

// ErrInvalidReload is returned when a Reload request or acknowledgement violates
// the local generation, digest or schema version syntax rules.
var ErrInvalidReload = errors.New("invalid plugin reload request")
