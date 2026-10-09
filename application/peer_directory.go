package application

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"sort"

	"github.com/Liapoldus/plugin-sdk/domain/models"
)

// ResolvePeer chooses one target from exactly one declared directory link. It
// has no transport client, hidden state, clock, retry or fallback behavior.
func ResolvePeer(directory models.PeerDirectory, request models.PeerResolution) (models.ResolvedPeer, error) {
	if err := directory.Validate(); err != nil {
		return models.ResolvedPeer{}, models.ErrInvalidPeerDirectory
	}
	if err := request.Validate(); err != nil {
		return models.ResolvedPeer{}, err
	}
	if !request.Now.Before(directory.ExpiresAt) {
		return models.ResolvedPeer{}, models.ErrPeerDirectoryExpired
	}
	if request.Now.Before(directory.IssuedAt) {
		return models.ResolvedPeer{}, models.ErrPeerDirectoryNotYetValid
	}
	var selectedLink *models.PeerLink
	for index := range directory.Links {
		if directory.Links[index].LinkID == request.LinkID {
			selectedLink = &directory.Links[index]
			break
		}
	}
	if selectedLink == nil {
		return models.ResolvedPeer{}, models.ErrPeerLinkNotFound
	}
	eligible := make([]models.PeerDirectoryReplica, 0, len(selectedLink.Replicas))
	for _, replica := range selectedLink.Replicas {
		if replica.Eligibility != models.PeerEligibilityReady || replica.EligibleUntil == nil || !replica.EligibleUntil.After(request.Now) {
			continue
		}
		if peerContractsCompatible(selectedLink.RequiredPeerContracts, replica.PeerContracts) {
			eligible = append(eligible, replica)
		}
	}
	if len(eligible) == 0 {
		return models.ResolvedPeer{}, models.ErrNoEligiblePeer
	}
	var selected models.PeerDirectoryReplica
	if request.StableRoutingKey != "" {
		selected = selectStablePeer(request.StableRoutingKey, *selectedLink, eligible)
	} else {
		selected = selectSequencePeer(*request.SelectionOrdinal, eligible)
	}
	return models.ResolvedPeer{
		DirectoryGeneration: directory.Generation,
		LinkID:              selectedLink.LinkID,
		TargetInstanceID:    selectedLink.TargetInstanceID,
		ReplicaID:           selected.Identity.ReplicaID,
		IncarnationID:       selected.Identity.IncarnationID,
		PlacementID:         selected.Identity.PlacementID,
		Carrier:             selectedLink.Carrier,
		SecurityProfile:     selectedLink.SecurityProfile,
		Endpoint:            selected.Endpoint,
		EligibleUntil:       *selected.EligibleUntil,
	}, nil
}

func selectSequencePeer(ordinal uint64, replicas []models.PeerDirectoryReplica) models.PeerDirectoryReplica {
	ordered := append([]models.PeerDirectoryReplica(nil), replicas...)
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].Identity.ReplicaID != ordered[right].Identity.ReplicaID {
			return ordered[left].Identity.ReplicaID < ordered[right].Identity.ReplicaID
		}
		return ordered[left].Identity.IncarnationID < ordered[right].Identity.IncarnationID
	})
	var total uint64
	for _, replica := range ordered {
		total += uint64(replica.Weight)
	}
	selection := ordinal % total
	for _, replica := range ordered {
		if selection < uint64(replica.Weight) {
			return replica
		}
		selection -= uint64(replica.Weight)
	}
	return ordered[len(ordered)-1]
}

func selectStablePeer(key string, link models.PeerLink, replicas []models.PeerDirectoryReplica) models.PeerDirectoryReplica {
	var selected models.PeerDirectoryReplica
	var highest [sha256.Size]byte
	selectedSet := false
	for _, replica := range replicas {
		for ticket := uint16(0); ticket < replica.Weight; ticket++ {
			score := stablePeerScore(key, link.LinkID, link.TargetInstanceID, replica.Identity, ticket)
			if !selectedSet || bytes.Compare(score[:], highest[:]) > 0 ||
				(bytes.Equal(score[:], highest[:]) && peerIdentityLess(replica.Identity, selected.Identity)) {
				highest = score
				selected = replica
				selectedSet = true
			}
		}
	}
	return selected
}

func stablePeerScore(key, linkID, targetInstanceID string, identity models.PeerReplicaID, ticket uint16) [sha256.Size]byte {
	hash := sha256.New()
	for _, value := range []string{key, linkID, targetInstanceID, identity.ReplicaID, identity.IncarnationID} {
		var size [4]byte
		if uint64(len(value)) > uint64(^uint32(0)) {
			panic("peer score input too large")
		}
		binary.BigEndian.PutUint32(size[:], uint32(len(value))) //nolint:gosec // length is bounded immediately above.
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(value))
	}
	var ticketBytes [2]byte
	binary.BigEndian.PutUint16(ticketBytes[:], ticket)
	_, _ = hash.Write(ticketBytes[:])
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func peerIdentityLess(left, right models.PeerReplicaID) bool {
	if left.InstanceID != right.InstanceID {
		return left.InstanceID < right.InstanceID
	}
	if left.ReplicaID != right.ReplicaID {
		return left.ReplicaID < right.ReplicaID
	}
	return left.IncarnationID < right.IncarnationID
}

func peerContractsCompatible(required []models.ContractRange, advertised []models.ContractVersion) bool {
	versions := make(map[string]string, len(advertised))
	for _, contract := range advertised {
		versions[contract.ContractID] = contract.Version
	}
	for _, requirement := range required {
		version, exists := versions[requirement.ContractID]
		if !exists || !requirement.Contains(version) {
			return false
		}
	}
	return true
}
