package models

// ReplicaPeerEndpoint advertises one generic peer carrier supported by a
// replica. The SDK does not open the connection.
type ReplicaPeerEndpoint struct {
	Carrier         PeerCarrier         `json:"carrier"`
	Endpoint        string              `json:"endpoint"`
	SecurityProfile PeerSecurityProfile `json:"securityProfile"`
}

func (endpoint ReplicaPeerEndpoint) Valid() bool {
	return endpoint.Carrier.valid() && endpoint.SecurityProfile == PeerSecurityMTLS &&
		validPeerEndpoint(endpoint.Carrier, endpoint.Endpoint)
}
