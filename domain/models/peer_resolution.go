package models

import "time"

// PeerResolution is a deterministic choice request. StableRoutingKey provides
// affinity; when absent, SelectionOrdinal is required and drives weighted
// sequence selection. Now is explicit so this model and its resolver do not
// read wall-clock state implicitly.
type PeerResolution struct {
	LinkID           string    `json:"linkId"`
	Now              time.Time `json:"now"`
	StableRoutingKey string    `json:"stableRoutingKey,omitempty"`
	SelectionOrdinal *uint64   `json:"selectionOrdinal,omitempty"`
}

func (request PeerResolution) Validate() error {
	if !ValidGeneration(request.LinkID) || request.Now.IsZero() ||
		len(request.StableRoutingKey) > PeerDirectoryMaximumKeyBytes ||
		(request.StableRoutingKey == "" && request.SelectionOrdinal == nil) {
		return ErrInvalidPeerResolution
	}
	return nil
}

type ResolvedPeer struct {
	DirectoryGeneration string              `json:"directoryGeneration"`
	LinkID              string              `json:"linkId"`
	TargetInstanceID    string              `json:"targetInstanceId"`
	ReplicaID           string              `json:"replicaId"`
	IncarnationID       string              `json:"incarnationId"`
	PlacementID         string              `json:"placementId"`
	Carrier             PeerCarrier         `json:"carrier"`
	SecurityProfile     PeerSecurityProfile `json:"securityProfile"`
	Endpoint            string              `json:"endpoint"`
	EligibleUntil       time.Time           `json:"eligibleUntil"`
}
