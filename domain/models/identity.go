package models

// ReplicaIdentity is the unique per-replica identity of one manually started
// plugin process. Core registers the same instanceId/replicaId pair together
// with the REST endpoint and the expected peer identity.
type ReplicaIdentity struct {
	InstanceID string `json:"instanceId"`
	ReplicaID  string `json:"replicaId"`
}

func NewReplicaIdentity(instanceID, replicaID string) (ReplicaIdentity, error) {
	identity := ReplicaIdentity{InstanceID: instanceID, ReplicaID: replicaID}
	if !identity.Valid() {
		return ReplicaIdentity{}, ErrInvalidIdentity
	}
	return identity, nil
}

func (identity ReplicaIdentity) Valid() bool {
	return ValidInstanceID(identity.InstanceID) && ValidReplicaID(identity.ReplicaID)
}
