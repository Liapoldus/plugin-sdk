package models

// ReplicaDeregistrationRequest identifies the exact incarnation being removed.
type ReplicaDeregistrationRequest struct {
	ContractVersion string        `json:"contractVersion"`
	Identity        PeerReplicaID `json:"identity"`
}

// ReplicaDeregistrationResponse confirms that Core removed the exact
// incarnation from eligibility.
type ReplicaDeregistrationResponse struct {
	ContractVersion string        `json:"contractVersion"`
	Identity        PeerReplicaID `json:"identity"`
	Deregistered    bool          `json:"deregistered"`
}

func (request ReplicaDeregistrationRequest) Validate() error {
	if request.ContractVersion == "" || !request.Identity.Valid() {
		return ErrInvalidReplicaRegistration
	}
	return nil
}

func (response ReplicaDeregistrationResponse) Validate(expectedContractVersion string, expectedIdentity PeerReplicaID) error {
	if response.ContractVersion != expectedContractVersion || response.Identity != expectedIdentity || !response.Deregistered {
		return ErrInvalidReplicaLease
	}
	return nil
}
