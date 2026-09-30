package models

// Registration is the bootstrap identity document a plugin publishes so that
// Core can reconcile a reconnected replica without a management write.
type Registration struct {
	ContractVersion   string `json:"contractVersion"`
	InstanceID        string `json:"instanceId"`
	ReplicaID         string `json:"replicaId"`
	AppliedGeneration string `json:"appliedGeneration"`
	Ready             bool   `json:"ready"`
}

func (registration Registration) Validate() error {
	if registration.ContractVersion == "" ||
		!ValidInstanceID(registration.InstanceID) ||
		!ValidReplicaID(registration.ReplicaID) ||
		registration.AppliedGeneration != "" && !ValidGeneration(registration.AppliedGeneration) {
		return ErrInvalidIdentity
	}
	return nil
}
