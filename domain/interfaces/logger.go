package interfaces

import (
	"context"

	"github.com/Liapoldus/plugin-sdk/domain/models"
)

// Field is one structured log record entry. Values reaching a Logger must
// already be redacted and bounded; the SDK redacts again at the transport edge
// as a second line of defense.
type Field struct {
	Key   string
	Value string
}

// Logger emits structured, newline-delimited JSON to operator-owned output. The
// SDK never writes settings, configuration documents, secret values, grant
// handles, certificate material or peer addresses to any log sink.
type Logger interface {
	Debug(ctx context.Context, message string, fields ...Field)
	Info(ctx context.Context, message string, fields ...Field)
	Warn(ctx context.Context, message string, fields ...Field)
	Error(ctx context.Context, message string, fields ...Field)
}

// LifecycleObserver records one bounded lifecycle event. Implementations must
// treat outcome as a closed vocabulary and must never accept a configuration
// document, secret value or grant handle as a label value.
type LifecycleObserver interface {
	Observe(ctx context.Context, kind string, outcome models.Outcome)
}
