package models

import (
	"net/url"
	"strings"
	"time"
)

// ReplicaRegistrationRequest is the immutable metadata a replica registers for
// one incarnation. Core binds Identity to the authenticated certificate URI.
type ReplicaRegistrationRequest struct {
	ContractVersion string                `json:"contractVersion"`
	Identity        PeerReplicaID         `json:"identity"`
	RestEndpoint    string                `json:"restEndpoint"`
	PeerEndpoints   []ReplicaPeerEndpoint `json:"peerEndpoints"`
	Release         ReplicaRelease        `json:"release"`
	// These are generic, plugin-owned release compatibility claims; Core and
	// SDK treat ContractID as opaque.
	AdvertisedContracts []ContractVersion `json:"advertisedContracts"`
	AcceptedContracts   []ContractRange   `json:"acceptedContracts"`
	AppliedGeneration   string            `json:"appliedGeneration"`
	Ready               bool              `json:"ready"`
}

// ReplicaRegistrationResponse confirms a Core-owned lease for an exact replica
// incarnation. ServerTime is authoritative for lease-expiry comparison.
type ReplicaRegistrationResponse struct {
	ContractVersion string        `json:"contractVersion"`
	Identity        PeerReplicaID `json:"identity"`
	ServerTime      time.Time     `json:"serverTime"`
	LeaseExpiresAt  time.Time     `json:"leaseExpiresAt"`
}

func (request ReplicaRegistrationRequest) Validate() error {
	if request.ContractVersion == "" || !request.Identity.Valid() ||
		!validControlEndpoint(request.RestEndpoint) || request.PeerEndpoints == nil ||
		request.AdvertisedContracts == nil || request.AcceptedContracts == nil ||
		!request.Release.Valid() || request.AppliedGeneration != "" && !ValidGeneration(request.AppliedGeneration) ||
		request.Ready && request.AppliedGeneration == "" {
		return ErrInvalidReplicaRegistration
	}
	carriers := make(map[PeerCarrier]struct{}, len(request.PeerEndpoints))
	for _, endpoint := range request.PeerEndpoints {
		if !endpoint.Valid() {
			return ErrInvalidReplicaRegistration
		}
		if _, exists := carriers[endpoint.Carrier]; exists {
			return ErrInvalidReplicaRegistration
		}
		carriers[endpoint.Carrier] = struct{}{}
	}
	if !validContractVersions(request.AdvertisedContracts) || !validContractRanges(request.AcceptedContracts) {
		return ErrInvalidReplicaRegistration
	}
	return nil
}

func (response ReplicaRegistrationResponse) Validate(expectedContractVersion string, expectedIdentity PeerReplicaID) error {
	if response.ContractVersion != expectedContractVersion || response.Identity != expectedIdentity ||
		response.ServerTime.IsZero() || !response.LeaseExpiresAt.After(response.ServerTime) {
		return ErrInvalidReplicaLease
	}
	return nil
}

func validControlEndpoint(endpoint string) bool {
	if endpoint == "" || len(endpoint) > 2048 || strings.ContainsAny(endpoint, "\x00\r\n") || strings.TrimSpace(endpoint) != endpoint {
		return false
	}
	parsed, err := url.Parse(endpoint)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil &&
		parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == "" && parsed.Opaque == ""
}

func validContractVersions(values []ContractVersion) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !value.Valid() {
			return false
		}
		if _, exists := seen[value.ContractID]; exists {
			return false
		}
		seen[value.ContractID] = struct{}{}
	}
	return true
}

func validContractRanges(values []ContractRange) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !value.Valid() {
			return false
		}
		if _, exists := seen[value.ContractID]; exists {
			return false
		}
		seen[value.ContractID] = struct{}{}
	}
	return true
}
