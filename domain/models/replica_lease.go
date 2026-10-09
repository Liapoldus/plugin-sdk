package models

import "time"

// ReplicaRenewalRequest reports the current readiness of an already registered
// incarnation. It cannot change placement, endpoints, release or contracts.
type ReplicaRenewalRequest struct {
	ContractVersion   string        `json:"contractVersion"`
	Identity          PeerReplicaID `json:"identity"`
	AppliedGeneration string        `json:"appliedGeneration"`
	Ready             bool          `json:"ready"`
}

// ReplicaLease is the authoritative lease response from Core.
type ReplicaLease struct {
	ContractVersion string        `json:"contractVersion"`
	Identity        PeerReplicaID `json:"identity"`
	ServerTime      time.Time     `json:"serverTime"`
	LeaseExpiresAt  time.Time     `json:"leaseExpiresAt"`
}

func (request ReplicaRenewalRequest) Validate() error {
	if request.ContractVersion == "" || !request.Identity.Valid() ||
		request.AppliedGeneration != "" && !ValidGeneration(request.AppliedGeneration) ||
		request.Ready && request.AppliedGeneration == "" {
		return ErrInvalidReplicaRegistration
	}
	return nil
}

func (lease ReplicaLease) Validate(expectedContractVersion string, expectedIdentity PeerReplicaID) error {
	if lease.ContractVersion != expectedContractVersion || lease.Identity != expectedIdentity ||
		lease.ServerTime.IsZero() || !lease.LeaseExpiresAt.After(lease.ServerTime) {
		return ErrInvalidReplicaLease
	}
	return nil
}
