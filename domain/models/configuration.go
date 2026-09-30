package models

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"unicode/utf8"
)

// Configuration is one exact immutable document pulled from Core. The raw bytes
// are never decoded, canonicalised or remarshalled: the digest is always
// computed over the same bytes Core stored and will compare against.
type Configuration struct {
	Generation    string
	SchemaVersion string
	SHA256        string
	RawJSON       []byte
}

func NewConfiguration(generation, schemaVersion, sha256Hex string, rawJSON []byte) (Configuration, error) {
	configuration := Configuration{
		Generation:    generation,
		SchemaVersion: schemaVersion,
		SHA256:        sha256Hex,
		RawJSON:       bytes.Clone(rawJSON),
	}
	if err := configuration.Validate(); err != nil {
		return Configuration{}, err
	}
	return configuration, nil
}

func (configuration Configuration) Bytes() []byte {
	return bytes.Clone(configuration.RawJSON)
}

func (configuration Configuration) Validate() error {
	if !ValidGeneration(configuration.Generation) || configuration.SchemaVersion == "" ||
		!ValidDigest(configuration.SHA256) {
		return ErrInvalidConfiguration
	}
	return ValidateDocument(configuration.RawJSON)
}

// ValidateDocument checks the generic syntax rules the SDK owns: valid UTF-8, a
// single JSON object, no duplicate object keys at any nesting level and a
// digest that matches the exact bytes. It never inspects a product field.
func ValidateDocument(rawJSON []byte) error {
	if len(rawJSON) == 0 || !utf8.Valid(rawJSON) || !ValidJSONObject(rawJSON) {
		return ErrInvalidDocument
	}
	return nil
}

// Digest computes the SHA-256 of the exact document bytes as lowercase hex.
func Digest(rawJSON []byte) string {
	sum := sha256.Sum256(rawJSON)
	return hex.EncodeToString(sum[:])
}

// MatchesDescriptor reports whether an exact pull matches the generation,
// digest and schema version announced by the Reload notification. Any mismatch
// is a separate contract outcome and never activates a document.
func (configuration Configuration) MatchesDescriptor(reload Reload) bool {
	return configuration.Generation == reload.Generation &&
		configuration.SHA256 == reload.SHA256 &&
		configuration.SchemaVersion == reload.SchemaVersion
}
