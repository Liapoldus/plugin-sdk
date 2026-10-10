package application

import (
	"context"
	"errors"

	"github.com/Liapoldus/plugin-sdk/v2/domain/interfaces"
	"github.com/Liapoldus/plugin-sdk/v2/domain/models"
)

// InProcessReplicaConfig supplies the plugin-owned half of an explicitly
// selected trusted Go composition. Core owns publication; the plugin owns
// validation, atomic application, readiness and observations.
type InProcessReplicaConfig struct {
	Source   interfaces.ConfigurationSource
	Applier  interfaces.ConfigurationApplier
	Identity models.ReplicaIdentity
	Observer interfaces.LifecycleObserver
	// PeerIdentity and Release make an in-process replica eligible for Core's
	// exact-cohort rollout controls. They are intentionally explicit: Core must
	// never invent an incarnation or artifact digest for a trusted plugin.
	PeerIdentity models.PeerReplicaID
	Release      models.ReplicaRelease
	// ValidateConfiguration is the plugin-owned preflight used before Core
	// persists a traffic-rollout candidate. It must not mutate active state.
	ValidateConfiguration func(context.Context, []byte) error
}

// InProcessReplica is the SDK lifecycle adapter consumed by a Core host. It
// exposes the same Reload/acknowledgement boundary as the REST client while
// keeping transport, listeners and persistence out of the plugin.
type InProcessReplica struct {
	publisher interfaces.ConfigurationPublisher
	lifecycle *Lifecycle
	peerID    models.PeerReplicaID
	release   models.ReplicaRelease
	validate  func(context.Context, []byte) error
}

func NewInProcessReplica(configuration InProcessReplicaConfig) (*InProcessReplica, error) {
	if configuration.Source == nil || configuration.Identity.InstanceID == "" {
		return nil, ErrMissingLifecycleDependency
	}
	lifecycle, err := NewLifecycle(LifecycleConfiguration{
		Source: configuration.Source, Applier: configuration.Applier,
		Identity: configuration.Identity, Observer: configuration.Observer,
	})
	if err != nil {
		return nil, err
	}
	publisher, ok := configuration.Source.(interfaces.ConfigurationPublisher)
	if !ok {
		return nil, ErrMissingLifecycleDependency
	}
	peerID := configuration.PeerIdentity
	if peerID.InstanceID == "" {
		peerID = models.PeerReplicaID{
			InstanceID: configuration.Identity.InstanceID,
			ReplicaID:  configuration.Identity.ReplicaID,
		}
	}
	return &InProcessReplica{
		publisher: publisher, lifecycle: lifecycle,
		peerID: peerID, release: configuration.Release,
		validate: configuration.ValidateConfiguration,
	}, nil
}

func (replica *InProcessReplica) InstanceID() string {
	if replica == nil || replica.lifecycle == nil {
		return ""
	}
	return replica.lifecycle.Readiness().InstanceID
}

func (replica *InProcessReplica) ReplicaID() string {
	if replica == nil || replica.lifecycle == nil {
		return ""
	}
	return replica.lifecycle.Readiness().ReplicaID
}

func (replica *InProcessReplica) Reload(ctx context.Context, request models.Reload) (models.ReloadAcknowledgement, error) {
	if replica == nil || replica.lifecycle == nil {
		return models.ReloadAcknowledgement{}, errors.New("in-process replica is unavailable")
	}
	return replica.lifecycle.Reload(ctx, request)
}

func (replica *InProcessReplica) Publish(snapshot models.InProcessConfigurationSnapshot) error {
	if replica == nil || replica.publisher == nil {
		return ErrMissingLifecycleDependency
	}
	return replica.publisher.Publish(snapshot)
}

func (replica *InProcessReplica) Readiness() models.Readiness {
	if replica == nil || replica.lifecycle == nil {
		return models.Readiness{}
	}
	return replica.lifecycle.Readiness()
}

func (replica *InProcessReplica) Registration(contractVersion string) (models.Registration, error) {
	if replica == nil || replica.lifecycle == nil {
		return models.Registration{}, models.ErrInvalidIdentity
	}
	return replica.lifecycle.Registration(contractVersion)
}

// PeerIdentity returns the immutable identity used for Core rollout cohorts.
// A zero value means this adapter was created without rollout metadata and is
// still valid for ordinary in-process configuration activation only.
func (replica *InProcessReplica) PeerIdentity() models.PeerReplicaID {
	if replica == nil {
		return models.PeerReplicaID{}
	}
	return replica.peerID
}

// Release returns the immutable artifact metadata supplied by the trusted
// composition root.
func (replica *InProcessReplica) Release() models.ReplicaRelease {
	if replica == nil {
		return models.ReplicaRelease{}
	}
	return replica.release
}

// ValidateConfiguration delegates candidate validation to the plugin-owned
// validator. A missing validator is deliberately reported as unavailable.
func (replica *InProcessReplica) ValidateConfiguration(ctx context.Context, raw []byte) error {
	if replica == nil || replica.validate == nil {
		return ErrMissingLifecycleDependency
	}
	return replica.validate(ctx, append([]byte(nil), raw...))
}
