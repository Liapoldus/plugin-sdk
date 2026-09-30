package models

// Reload is the Core notification that an immutable generation is available and
// must become the active plugin configuration. It is a notification, not a
// configuration transfer: it never carries the document.
type Reload struct {
	Generation    string `json:"generation"`
	SHA256        string `json:"sha256"`
	SchemaVersion string `json:"schemaVersion"`
}

// ReloadAcknowledgement is returned only after the plugin-owned validator and
// applier have atomically activated the exact pulled document.
type ReloadAcknowledgement struct {
	Generation    string  `json:"generation"`
	SHA256        string  `json:"sha256"`
	SchemaVersion string  `json:"schemaVersion"`
	Applied       bool    `json:"applied"`
	Outcome       Outcome `json:"outcome"`
}

func (reload Reload) Validate() error {
	if !ValidGeneration(reload.Generation) || !ValidDigest(reload.SHA256) || reload.SchemaVersion == "" {
		return ErrInvalidReload
	}
	return nil
}

// SameDescriptor reports byte-for-byte equality of the reload descriptor that
// makes a repeat notification idempotent.
func (reload Reload) SameDescriptor(other Reload) bool {
	return reload.Generation == other.Generation &&
		reload.SHA256 == other.SHA256 &&
		reload.SchemaVersion == other.SchemaVersion
}
