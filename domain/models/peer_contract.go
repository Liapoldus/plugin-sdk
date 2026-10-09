package models

import (
	"strings"
)

// Valid reports whether the generic contract requirement is a valid
// half-open SemVer 2.0.0 range [minimum, maximumExclusive).
func (requirement ContractRange) Valid() bool {
	if !validContractID(requirement.ContractID) {
		return false
	}
	minimum, minOK := parseSemver(requirement.MinimumVersion)
	maximum, maxOK := parseSemver(requirement.MaximumVersionExclusive)
	return minOK && maxOK && compareSemver(minimum, maximum) < 0
}

func (requirement ContractRange) Contains(version string) bool {
	if !requirement.Valid() {
		return false
	}
	minimum, _ := parseSemver(requirement.MinimumVersion)
	maximum, _ := parseSemver(requirement.MaximumVersionExclusive)
	actual, valid := parseSemver(version)
	return valid && compareSemver(actual, minimum) >= 0 && compareSemver(actual, maximum) < 0
}

func (contract ContractVersion) Valid() bool {
	_, validVersion := parseSemver(contract.Version)
	return validContractID(contract.ContractID) && validVersion && ValidDigest(contract.SHA256) &&
		contract.SHA256 == strings.ToLower(contract.SHA256)
}

type semver struct {
	major      string
	minor      string
	patch      string
	prerelease []string
}

func parseSemver(value string) (semver, bool) {
	if value == "" || len(value) > ContractMaximumVersionBytes || strings.HasPrefix(value, "v") {
		return semver{}, false
	}
	coreAndBuild := strings.SplitN(value, "+", 2)
	if len(coreAndBuild) == 2 && !validSemverIdentifiers(coreAndBuild[1], false) {
		return semver{}, false
	}
	coreAndPrerelease := strings.SplitN(coreAndBuild[0], "-", 2)
	core := strings.Split(coreAndPrerelease[0], ".")
	if len(core) != 3 {
		return semver{}, false
	}
	for _, part := range core {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return semver{}, false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return semver{}, false
			}
		}
	}
	var prerelease []string
	if len(coreAndPrerelease) == 2 {
		if !validSemverIdentifiers(coreAndPrerelease[1], true) {
			return semver{}, false
		}
		prerelease = strings.Split(coreAndPrerelease[1], ".")
	}
	return semver{major: core[0], minor: core[1], patch: core[2], prerelease: prerelease}, true
}

func validSemverIdentifiers(value string, rejectLeadingZero bool) bool {
	if value == "" {
		return false
	}
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return false
		}
		numeric := true
		for _, character := range identifier {
			valid := (character >= '0' && character <= '9') || (character >= 'A' && character <= 'Z') ||
				(character >= 'a' && character <= 'z') || character == '-'
			if !valid {
				return false
			}
			if character < '0' || character > '9' {
				numeric = false
			}
		}
		if rejectLeadingZero && numeric && len(identifier) > 1 && identifier[0] == '0' {
			return false
		}
	}
	return true
}

func compareSemver(left, right semver) int {
	for _, component := range [][2]string{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if compared := compareNumericText(component[0], component[1]); compared != 0 {
			return compared
		}
	}
	if len(left.prerelease) == 0 && len(right.prerelease) == 0 {
		return 0
	}
	if len(left.prerelease) == 0 {
		return 1
	}
	if len(right.prerelease) == 0 {
		return -1
	}
	for index := 0; index < len(left.prerelease) && index < len(right.prerelease); index++ {
		leftID, rightID := left.prerelease[index], right.prerelease[index]
		leftNumeric, rightNumeric := numericSemverIdentifier(leftID), numericSemverIdentifier(rightID)
		switch {
		case leftNumeric && rightNumeric:
			if compared := compareNumericText(leftID, rightID); compared != 0 {
				return compared
			}
		case leftNumeric:
			return -1
		case rightNumeric:
			return 1
		case leftID != rightID:
			if leftID < rightID {
				return -1
			}
			return 1
		}
	}
	if len(left.prerelease) < len(right.prerelease) {
		return -1
	}
	if len(left.prerelease) > len(right.prerelease) {
		return 1
	}
	return 0
}

func numericSemverIdentifier(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func compareNumericText(left, right string) int {
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}
