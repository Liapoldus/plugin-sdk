package infrastructure

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/Liapoldus/plugin-sdk/v2/domain/interfaces"
	"github.com/Liapoldus/plugin-sdk/v2/domain/models"
)

// InProcessConfigurationSnapshot remains available from infrastructure as the
// source-adapter spelling; the canonical model belongs to domain.
type InProcessConfigurationSnapshot = models.InProcessConfigurationSnapshot

var (
	// ErrInvalidInProcessSnapshot means that the composition root tried to
	// publish a snapshot that cannot be exposed as an exact Core generation.
	ErrInvalidInProcessSnapshot = errors.New("invalid Plugin SDK in-process configuration snapshot")
	// ErrInProcessGenerationUnavailable is deliberately opaque. The lifecycle
	// application layer classifies it as a failed Core pull and never receives a
	// path, payload or mutable store detail.
	ErrInProcessGenerationUnavailable = errors.New("in-process configuration generation unavailable")
)

type inProcessConfigurationSnapshot struct {
	instanceID string
	active     models.Configuration
	previous   *models.Configuration
}

// InProcessConfigurationSource implements the domain ConfigurationSource port
// without opening a listener or reading Core's durable store. The source is bound to one
// plugin instance at construction and every pull is restricted to the latest
// atomically published active/previous snapshot for that instance.
type InProcessConfigurationSource struct {
	instanceID string
	snapshot   atomic.Pointer[inProcessConfigurationSnapshot]
}

// NewInProcessConfigurationSource binds an exact-generation source to one
// instance. The source refuses invalid or duplicate slot generations before it
// becomes available to a lifecycle.
func NewInProcessConfigurationSource(snapshot models.InProcessConfigurationSnapshot) (*InProcessConfigurationSource, error) {
	if !validInProcessSnapshot(snapshot) {
		return nil, ErrInvalidInProcessSnapshot
	}
	source := &InProcessConfigurationSource{instanceID: snapshot.InstanceID}
	source.snapshot.Store(cloneInProcessSnapshot(snapshot))
	return source, nil
}

// NewInProcessConfigurationSourceForInstance creates an initially unavailable
// source for a trusted composition root. Core publishes the retained active /
// previous pair after Core's durable snapshot is ready; a plugin cannot observe a
// partial bootstrap state.
func NewInProcessConfigurationSourceForInstance(instanceID string) (*InProcessConfigurationSource, error) {
	if !models.ValidInstanceID(instanceID) {
		return nil, ErrInvalidInProcessSnapshot
	}
	return &InProcessConfigurationSource{instanceID: instanceID}, nil
}

// InstanceID returns the immutable instance scope of this source.
func (source *InProcessConfigurationSource) InstanceID() string {
	if source == nil {
		return ""
	}
	return source.instanceID
}

// Publish atomically replaces the two-slot snapshot. A failed publication
// leaves the previously visible snapshot unchanged. Core composition roots
// call this after their durable promotion transaction commits.
func (source *InProcessConfigurationSource) Publish(snapshot models.InProcessConfigurationSnapshot) error {
	if source == nil || snapshot.InstanceID != source.instanceID || !validInProcessSnapshot(snapshot) {
		return ErrInvalidInProcessSnapshot
	}
	source.snapshot.Store(cloneInProcessSnapshot(snapshot))
	return nil
}

// PullExact returns only the requested active or previous generation. It never
// lists generations, substitutes a different generation, or exposes staging.
func (source *InProcessConfigurationSource) PullExact(ctx context.Context, generation string) (interfaces.PullResult, error) {
	if source == nil || !models.ValidGeneration(generation) {
		return interfaces.PullResult{}, ErrInProcessGenerationUnavailable
	}
	select {
	case <-ctx.Done():
		return interfaces.PullResult{}, ctx.Err()
	default:
	}
	snapshot := source.snapshot.Load()
	if snapshot == nil {
		return interfaces.PullResult{}, ErrInProcessGenerationUnavailable
	}
	if snapshot.active.Generation == generation {
		return interfaces.PullResult{
			Configuration: snapshot.active,
			State:         interfaces.GenerationStateActive,
		}, nil
	}
	if snapshot.previous != nil && snapshot.previous.Generation == generation {
		return interfaces.PullResult{
			Configuration: *snapshot.previous,
			State:         interfaces.GenerationStatePrevious,
		}, nil
	}
	return interfaces.PullResult{}, ErrInProcessGenerationUnavailable
}

func validInProcessSnapshot(snapshot models.InProcessConfigurationSnapshot) bool {
	if !models.ValidInstanceID(snapshot.InstanceID) || !validConfiguration(snapshot.Active) {
		return false
	}
	if snapshot.Previous == nil {
		return true
	}
	return snapshot.Previous.Generation != snapshot.Active.Generation && validConfiguration(*snapshot.Previous)
}

func validConfiguration(configuration models.Configuration) bool {
	return configuration.Validate() == nil && models.Digest(configuration.Bytes()) == configuration.SHA256
}

func cloneInProcessSnapshot(snapshot models.InProcessConfigurationSnapshot) *inProcessConfigurationSnapshot {
	value := &inProcessConfigurationSnapshot{
		instanceID: snapshot.InstanceID,
		active:     cloneConfiguration(snapshot.Active),
	}
	if snapshot.Previous != nil {
		previous := cloneConfiguration(*snapshot.Previous)
		value.previous = &previous
	}
	return value
}

func cloneConfiguration(configuration models.Configuration) models.Configuration {
	return models.Configuration{
		Generation:    configuration.Generation,
		SchemaVersion: configuration.SchemaVersion,
		SHA256:        configuration.SHA256,
		RawJSON:       configuration.Bytes(),
	}
}
