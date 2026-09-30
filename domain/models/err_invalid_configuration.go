package models

import "errors"

// ErrInvalidConfiguration is returned when an exact-generation document does not
// match the generation, schema version and digest advertised by Core.
var ErrInvalidConfiguration = errors.New("invalid plugin configuration document")
