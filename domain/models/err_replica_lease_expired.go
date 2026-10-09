package models

import "errors"

var ErrReplicaLeaseExpired = errors.New("plugin SDK replica lease expired")
