package interfaces

import "context"

// PluginMetadata exposes the plugin-owned Manifest and versioned settings JSON
// Schema. Both documents stay opaque to the SDK: it publishes the exact bytes
// and never interprets a field, capability or product name.
type PluginMetadata interface {
	Manifest(ctx context.Context) ([]byte, error)
	ConfigurationSchema(ctx context.Context) ([]byte, error)
}
