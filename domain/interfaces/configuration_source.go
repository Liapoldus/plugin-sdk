package interfaces

import (
	"context"

	"github.com/Liapoldus/plugin-sdk/v2/domain/models"
)

// GenerationState is the durable slot Core reports for a pulled generation.
// Only the active slot may be applied; a generation that is no longer desired
// is a separate contract outcome and is never activated.
type GenerationState string

const (
	GenerationStateActive   GenerationState = "active"
	GenerationStatePrevious GenerationState = "previous"
)

// PullResult is one exact-generation pull. The document bytes are exactly what
// Core stored, the descriptors come from response headers, and no field is
// derived or normalized by the SDK.
type PullResult struct {
	Configuration models.Configuration
	State         GenerationState
}

// ConfigurationSource reads one exact immutable generation from Core over the
// private mutual-TLS control API. It must never list generations and must never
// substitute a different document for the requested one.
type ConfigurationSource interface {
	PullExact(ctx context.Context, generation string) (PullResult, error)
}
