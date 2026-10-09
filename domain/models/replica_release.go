package models

// ReplicaRelease identifies the immutable binary release active in one
// replica incarnation.
type ReplicaRelease struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

func (release ReplicaRelease) Valid() bool {
	_, validVersion := parseSemver(release.Version)
	return validVersion && ValidDigest(release.SHA256) && release.SHA256 == lowerASCII(release.SHA256)
}

func lowerASCII(value string) string {
	result := []byte(value)
	for index, character := range result {
		if character >= 'A' && character <= 'Z' {
			result[index] = character + ('a' - 'A')
		}
	}
	return string(result)
}
