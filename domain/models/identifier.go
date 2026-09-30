package models

import (
	"encoding/hex"
	"strings"
	"unicode"
)

// Identifier constraints shared by instance, replica and generation syntax.
// Identifiers are opaque to the SDK: it never parses their ordering or meaning.
const identifierMaximumBytes = 256

// ValidGeneration reports whether a Core generation identifier is safe to embed
// in an exact-generation request path and safe to compare byte-for-byte.
func ValidGeneration(value string) bool {
	return validIdentifier(value)
}

// ValidInstanceID reports whether an instance identifier satisfies the contract
// syntax rules.
func ValidInstanceID(value string) bool {
	return validIdentifier(value)
}

// ValidReplicaID reports whether a replica identifier satisfies the contract
// syntax rules. Identities are unique per replica.
func ValidReplicaID(value string) bool {
	return validIdentifier(value)
}

func validIdentifier(value string) bool {
	if value == "" || value == "." || value == ".." ||
		len(value) > identifierMaximumBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character > unicode.MaxASCII {
			return false
		}
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' || character == '~' {
			continue
		}
		return false
	}
	return true
}

// ValidDigest reports whether a value is a lowercase hexadecimal SHA-256 digest.
func ValidDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}
