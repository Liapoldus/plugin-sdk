package infrastructure

import "errors"

const expectedLoopbackPlaintextVersion = "liapoldus.plugin-sdk.loopback-plaintext.v2"

var ErrInvalidLoopbackPlaintextProfile = errors.New("invalid Plugin SDK loopback plaintext profile")

type LoopbackPlaintextProfile struct {
	ContractVersion  string `json:"contractVersion"`
	EnabledByDefault bool   `json:"enabledByDefault"`
	Transport        struct {
		Network                    string `json:"network"`
		HostAddressMustBeLiteralIP bool   `json:"hostAddressMustBeLiteralIP"`
		HostMustBeLoopback         bool   `json:"hostMustBeLoopback"`
		RemoteBindAllowed          bool   `json:"remoteBindAllowed"`
		TLSFailureFallback         bool   `json:"tlsFailureFallback"`
	} `json:"transport"`
	ExposedEndpoint struct {
		HTTPContractVersion string `json:"httpContractVersion"`
		EndpointKey         string `json:"endpointKey"`
	} `json:"exposedEndpoint"`
	NotFoundProblemKey                  string   `json:"notFoundProblemKey"`
	ProblemResponseField                string   `json:"problemResponseField"`
	ExcludedEndpointKeys                []string `json:"excludedEndpointKeys"`
	CoreEndpointsAlwaysRequireMutualTLS []string `json:"coreEndpointsAlwaysRequireMutualTLS"`
}

// LoadLoopbackPlaintextProfile returns the immutable SDK-owned transport profile.
func LoadLoopbackPlaintextProfile() (LoopbackPlaintextProfile, error) {
	profile := newLoopbackPlaintextProfile()
	if profile.ContractVersion != expectedLoopbackPlaintextVersion ||
		profile.EnabledByDefault || profile.Transport.Network != "tcp" ||
		!profile.Transport.HostAddressMustBeLiteralIP || !profile.Transport.HostMustBeLoopback ||
		profile.Transport.RemoteBindAllowed || profile.Transport.TLSFailureFallback ||
		profile.ExposedEndpoint.HTTPContractVersion != expectedContractVersion ||
		profile.ExposedEndpoint.EndpointKey == "" || profile.NotFoundProblemKey == "" ||
		profile.ProblemResponseField == "" || len(profile.ExcludedEndpointKeys) == 0 ||
		len(profile.CoreEndpointsAlwaysRequireMutualTLS) == 0 {
		return LoopbackPlaintextProfile{}, ErrInvalidLoopbackPlaintextProfile
	}
	return profile, nil
}
