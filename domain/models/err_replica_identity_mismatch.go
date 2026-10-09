package models

import "errors"

var ErrReplicaIdentityMismatch = errors.New("plugin SDK replica identity does not match its certificate")
