package models

import "errors"

var ErrReplicaIdentityConflict = errors.New("plugin SDK replica incarnation conflicts with Core state")
