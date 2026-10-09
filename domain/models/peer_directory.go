package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	PeerDirectoryContractVersion  = "liapoldus.plugin-sdk.peer-directory.v1"
	PeerDirectoryMaximumBytes     = 1 << 20
	PeerDirectoryMaximumLinks     = 256
	PeerDirectoryMaximumReplicas  = 512
	PeerDirectoryMaximumContracts = 64
	PeerDirectoryMaximumWeight    = 100
	PeerDirectoryMaximumTTL       = 30 * time.Second
	PeerDirectoryMaximumKeyBytes  = 256
	ContractMaximumVersionBytes   = 128
)

type PeerPlacementRule string

const (
	PeerPlacementSame   PeerPlacementRule = "same-placement"
	PeerPlacementRemote PeerPlacementRule = "remote"
)

type PeerCarrier string

const (
	PeerCarrierTCP         PeerCarrier = "tcp"
	PeerCarrierQUIC        PeerCarrier = "quic"
	PeerCarrierUnix        PeerCarrier = "unix"
	PeerCarrierWindowsPipe PeerCarrier = "windows-named-pipe"
)

type PeerSecurityProfile string

const PeerSecurityMTLS PeerSecurityProfile = "mtls"

type PeerEligibility string

const (
	PeerEligibilityReady        PeerEligibility = "ready"
	PeerEligibilityNotReady     PeerEligibility = "not-ready"
	PeerEligibilityDraining     PeerEligibility = "draining"
	PeerEligibilityLeaseExpired PeerEligibility = "lease-expired"
	PeerEligibilityFenced       PeerEligibility = "fenced"
	PeerEligibilityIncompatible PeerEligibility = "incompatible"
)

// PeerDirectory is a Core-authorized, expiring view of caller-to-target links.
// It contains routing metadata only; it never carries peer payloads or methods.
type PeerDirectory struct {
	ContractVersion string        `json:"contractVersion"`
	Generation      string        `json:"generation"`
	IssuedAt        time.Time     `json:"issuedAt"`
	ExpiresAt       time.Time     `json:"expiresAt"`
	Caller          PeerReplicaID `json:"caller"`
	Links           []PeerLink    `json:"links"`
}

type PeerReplicaID struct {
	InstanceID    string `json:"instanceId"`
	ReplicaID     string `json:"replicaId"`
	IncarnationID string `json:"incarnationId"`
	PlacementID   string `json:"placementId"`
}

type PeerLink struct {
	LinkID                string                 `json:"linkId"`
	TargetInstanceID      string                 `json:"targetInstanceId"`
	PlacementRule         PeerPlacementRule      `json:"placementRule"`
	Carrier               PeerCarrier            `json:"carrier"`
	SecurityProfile       PeerSecurityProfile    `json:"securityProfile"`
	RequiredPeerContracts []ContractRange        `json:"requiredPeerContracts"`
	Replicas              []PeerDirectoryReplica `json:"replicas"`
}

// ContractRange is a generic compatibility requirement. ContractID is opaque
// to the SDK; version comparison follows SemVer 2.0.0 and uses the half-open
// interval [MinimumVersion, MaximumVersionExclusive).
type ContractRange struct {
	ContractID              string `json:"contractId"`
	MinimumVersion          string `json:"minimumVersion"`
	MaximumVersionExclusive string `json:"maximumVersionExclusive"`
}

// ContractVersion is one opaque contract advertised by a release. SHA256
// identifies the exact immutable contract document for this version.
type ContractVersion struct {
	ContractID string `json:"contractId"`
	Version    string `json:"version"`
	SHA256     string `json:"sha256"`
}

type PeerDirectoryReplica struct {
	Identity      PeerReplicaID     `json:"identity"`
	Endpoint      string            `json:"endpoint"`
	Eligibility   PeerEligibility   `json:"eligibility"`
	EligibleUntil *time.Time        `json:"eligibleUntil"`
	Weight        uint16            `json:"weight"`
	PeerContracts []ContractVersion `json:"peerContracts"`
}

// ParsePeerDirectory strictly decodes one bounded JSON document and validates
// its versioned shape before the resolver can use it.
func ParsePeerDirectory(raw []byte) (PeerDirectory, error) {
	if len(raw) == 0 || len(raw) > PeerDirectoryMaximumBytes || !ValidJSONObject(raw) {
		return PeerDirectory{}, ErrInvalidPeerDirectory
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var directory PeerDirectory
	if err := decoder.Decode(&directory); err != nil {
		return PeerDirectory{}, ErrInvalidPeerDirectory
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return PeerDirectory{}, ErrInvalidPeerDirectory
	}
	if err := directory.Validate(); err != nil {
		return PeerDirectory{}, err
	}
	return directory, nil
}

func (directory PeerDirectory) Validate() error {
	if directory.ContractVersion != PeerDirectoryContractVersion || !ValidGeneration(directory.Generation) ||
		directory.IssuedAt.IsZero() || directory.ExpiresAt.IsZero() ||
		!directory.ExpiresAt.After(directory.IssuedAt) || directory.ExpiresAt.Sub(directory.IssuedAt) > PeerDirectoryMaximumTTL ||
		!directory.Caller.Valid() || directory.Links == nil || len(directory.Links) > PeerDirectoryMaximumLinks {
		return ErrInvalidPeerDirectory
	}
	links := make(map[string]struct{}, len(directory.Links))
	contractDigests := make(map[string]string)
	for _, link := range directory.Links {
		if !ValidGeneration(link.LinkID) || !ValidInstanceID(link.TargetInstanceID) ||
			!link.PlacementRule.valid() || !link.Carrier.valid() || link.SecurityProfile != PeerSecurityMTLS ||
			!validPlacementCarrier(link.PlacementRule, link.Carrier) ||
			link.Replicas == nil || link.RequiredPeerContracts == nil ||
			len(link.Replicas) > PeerDirectoryMaximumReplicas || len(link.RequiredPeerContracts) > PeerDirectoryMaximumContracts {
			return ErrInvalidPeerDirectory
		}
		if _, exists := links[link.LinkID]; exists {
			return ErrInvalidPeerDirectory
		}
		links[link.LinkID] = struct{}{}
		requirements := make(map[string]struct{}, len(link.RequiredPeerContracts))
		for _, requirement := range link.RequiredPeerContracts {
			if !requirement.Valid() {
				return ErrInvalidPeerDirectory
			}
			if _, exists := requirements[requirement.ContractID]; exists {
				return ErrInvalidPeerDirectory
			}
			requirements[requirement.ContractID] = struct{}{}
		}
		replicas := make(map[string]struct{}, len(link.Replicas))
		for _, replica := range link.Replicas {
			if !replica.Identity.Valid() || replica.Identity.InstanceID != link.TargetInstanceID ||
				!ValidInstanceID(replica.Identity.PlacementID) || replica.Weight == 0 || replica.Weight > PeerDirectoryMaximumWeight ||
				!replica.Eligibility.valid() || replica.PeerContracts == nil || len(replica.PeerContracts) > PeerDirectoryMaximumContracts {
				return ErrInvalidPeerDirectory
			}
			if _, exists := replicas[replica.Identity.ReplicaID]; exists {
				return ErrInvalidPeerDirectory
			}
			replicas[replica.Identity.ReplicaID] = struct{}{}
			if replica.Eligibility == PeerEligibilityReady {
				if replica.Endpoint == "" || replica.EligibleUntil == nil ||
					!replica.EligibleUntil.After(directory.IssuedAt) || replica.EligibleUntil.After(directory.ExpiresAt) ||
					(link.PlacementRule == PeerPlacementSame && replica.Identity.PlacementID != directory.Caller.PlacementID) ||
					(link.PlacementRule == PeerPlacementRemote && replica.Identity.PlacementID == directory.Caller.PlacementID) ||
					!validPeerEndpoint(link.Carrier, replica.Endpoint) {
					return ErrInvalidPeerDirectory
				}
			} else if replica.Endpoint != "" || replica.EligibleUntil != nil {
				return ErrInvalidPeerDirectory
			}
			contracts := make(map[string]struct{}, len(replica.PeerContracts))
			for _, peerContract := range replica.PeerContracts {
				if !peerContract.Valid() {
					return ErrInvalidPeerDirectory
				}
				if _, exists := contracts[peerContract.ContractID]; exists {
					return ErrInvalidPeerDirectory
				}
				contracts[peerContract.ContractID] = struct{}{}
				key := peerContract.ContractID + "\x00" + peerContract.Version
				if prior, exists := contractDigests[key]; exists && prior != peerContract.SHA256 {
					return ErrInvalidPeerDirectory
				}
				contractDigests[key] = peerContract.SHA256
			}
		}
	}
	return nil
}

func (identity PeerReplicaID) Valid() bool {
	return ValidInstanceID(identity.InstanceID) && ValidReplicaID(identity.ReplicaID) &&
		ValidGeneration(identity.IncarnationID) && ValidInstanceID(identity.PlacementID)
}

func (rule PeerPlacementRule) valid() bool {
	return rule == PeerPlacementSame || rule == PeerPlacementRemote
}

func (carrier PeerCarrier) valid() bool {
	switch carrier {
	case PeerCarrierTCP, PeerCarrierQUIC, PeerCarrierUnix, PeerCarrierWindowsPipe:
		return true
	default:
		return false
	}
}

func (eligibility PeerEligibility) valid() bool {
	switch eligibility {
	case PeerEligibilityReady, PeerEligibilityNotReady, PeerEligibilityDraining, PeerEligibilityLeaseExpired, PeerEligibilityFenced, PeerEligibilityIncompatible:
		return true
	default:
		return false
	}
}

func validPlacementCarrier(rule PeerPlacementRule, carrier PeerCarrier) bool {
	if rule == PeerPlacementSame {
		return carrier == PeerCarrierUnix || carrier == PeerCarrierWindowsPipe
	}
	return carrier == PeerCarrierTCP || carrier == PeerCarrierQUIC
}

func validContractID(value string) bool {
	if value == "" || len(value) > identifierMaximumBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._:/-", character) {
			continue
		}
		return false
	}
	return true
}

func validPeerEndpoint(carrier PeerCarrier, endpoint string) bool {
	if endpoint == "" || len(endpoint) > 2048 || strings.ContainsAny(endpoint, "\x00\r\n") || strings.TrimSpace(endpoint) != endpoint {
		return false
	}
	switch carrier {
	case PeerCarrierTCP, PeerCarrierQUIC:
		host, portText, err := net.SplitHostPort(endpoint)
		if err != nil || !validPeerHost(host) {
			return false
		}
		port, err := strconv.Atoi(portText)
		return err == nil && port > 0 && port <= 65535
	case PeerCarrierUnix:
		return strings.HasPrefix(endpoint, "/") && path.Clean(endpoint) == endpoint
	case PeerCarrierWindowsPipe:
		const prefix = `\\.\pipe\`
		if !strings.HasPrefix(strings.ToLower(endpoint), prefix) {
			return false
		}
		name := strings.TrimPrefix(strings.ToLower(endpoint), prefix)
		return name != "" && !strings.Contains(name, "..") && !strings.ContainsAny(name, "/:\\")
	default:
		return false
	}
}

func validPeerHost(host string) bool {
	if host == "" || strings.TrimSpace(host) != host {
		return false
	}
	address := host
	if zoneStart := strings.LastIndexByte(host, '%'); zoneStart >= 0 {
		zone := host[zoneStart+1:]
		if zone == "" {
			return false
		}
		for _, character := range zone {
			valid := (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') || character == '_' || character == '.' || character == '-'
			if !valid {
				return false
			}
		}
		address = host[:zoneStart]
	}
	if net.ParseIP(address) != nil {
		return true
	}
	if strings.Contains(address, ":") || len(address) > 253 || strings.HasSuffix(address, ".") {
		return false
	}
	for _, label := range strings.Split(address, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			valid := (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') || character == '-'
			if !valid {
				return false
			}
		}
	}
	return true
}
