package models

import "errors"

// ErrInvalidDocument is returned when a configuration document is not a single
// UTF-8 JSON object free of duplicate object keys at any nesting level.
var ErrInvalidDocument = errors.New("invalid plugin configuration document syntax")
