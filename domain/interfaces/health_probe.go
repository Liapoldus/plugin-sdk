package interfaces

import "context"

// HealthProbe is the plugin-owned liveness detail. The SDK serves the generic
// health and readiness endpoints either way; a plugin uses this only to add its
// own bounded liveness detail without exposing internal state.
type HealthProbe interface {
	Check(ctx context.Context) error
}
