package interfaces

import (
	"context"
	"time"

	"github.com/Liapoldus/plugin-sdk/domain/models"
)

// SecretBroker issues and redeems scoped, expiring, one-use grants for secret
// references. The SDK is a client only: it holds no credential store, performs
// no long-term caching and keeps no secret material after the generation that
// requested it is replaced. Handles are treated as bearer secrets and are never
// logged, never used as a metric label and never embedded in an error.
type SecretBroker interface {
	IssueGrant(ctx context.Context, request models.SecretGrantRequest) (models.SecretGrant, error)
	Redeem(ctx context.Context, redemption models.SecretRedemption) (models.SecretValue, error)
}

// Clock supplies the current time so lifecycle deadlines and grant expiry stay
// testable and so the SDK never reads a wall clock directly.
type Clock interface {
	Now() time.Time
}
