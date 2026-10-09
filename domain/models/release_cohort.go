package models

// ReleaseCohortCompatible reports whether every release in one logical plugin
// instance can safely coexist. Identical binary digests need no compatibility
// claims. Different releases require explicit mutual acceptance of every
// advertised contract version; an empty claim set is not evidence.
func ReleaseCohortCompatible(registrations []ReplicaRegistrationRequest) bool {
	if len(registrations) < 2 {
		return true
	}
	for _, registration := range registrations {
		if registration.Validate() != nil {
			return false
		}
	}
	for left := 0; left < len(registrations); left++ {
		for right := left + 1; right < len(registrations); right++ {
			first := registrations[left]
			second := registrations[right]
			if first.Release.SHA256 == second.Release.SHA256 {
				continue
			}
			if len(first.AdvertisedContracts) == 0 && len(second.AdvertisedContracts) == 0 {
				return false
			}
			if !acceptsAll(first.AdvertisedContracts, second.AcceptedContracts) ||
				!acceptsAll(second.AdvertisedContracts, first.AcceptedContracts) {
				return false
			}
		}
	}
	return true
}

func acceptsAll(advertised []ContractVersion, accepted []ContractRange) bool {
	ranges := make(map[string]ContractRange, len(accepted))
	for _, contractRange := range accepted {
		ranges[contractRange.ContractID] = contractRange
	}
	for _, contract := range advertised {
		contractRange, found := ranges[contract.ContractID]
		if !found || !contractRange.Contains(contract.Version) {
			return false
		}
	}
	return true
}
