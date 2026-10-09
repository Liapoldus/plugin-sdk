package infrastructure

// PeerDirectorySchema returns an owned copy of the canonical JSON Schema so a
// Core or plugin integration can inspect the exact DTO contract without
// importing a second model definition.
func PeerDirectorySchema() []byte {
	return schemaDocument(peerDirectoryDefinition())
}
