package application

import (
	"liapoldus.local/plugin-sdk/domain/models"
)

// Readiness reports the configuration generation this replica has actually
// applied, together with its identity and the most recent generation it
// refused. It never claims a generation that failed verification or application,
// and it never reports a desired state on behalf of Core.
//
// Ready is false until the first successful apply. After that it stays true: a
// refusal leaves the previously applied configuration fully active and only
// changes PendingGeneration, so Core can fence a degraded replica without the
// plugin guessing what Core wants.
func (lifecycle *Lifecycle) Readiness() models.Readiness {
	lifecycle.stateMu.RLock()
	defer lifecycle.stateMu.RUnlock()

	readiness := models.Readiness{
		InstanceID:        lifecycle.identity.InstanceID,
		ReplicaID:         lifecycle.identity.ReplicaID,
		PendingGeneration: lifecycle.pending,
	}
	if lifecycle.active != nil {
		readiness.Ready = true
		readiness.Generation = lifecycle.active.Generation
		readiness.SHA256 = lifecycle.active.SHA256
		readiness.SchemaVersion = lifecycle.active.SchemaVersion
	}
	return readiness
}

// Registration returns the bootstrap identity document this replica publishes so
// that Core can reconcile a reconnected replica without a management write. The
// applied generation is the one Readiness reports, never a pending one.
//
// It fails with models.ErrInvalidIdentity when the contract version or the
// replica identity is not acceptable, so the caller can refuse to serve the
// document instead of publishing an unusable identity.
func (lifecycle *Lifecycle) Registration(contractVersion string) (models.Registration, error) {
	readiness := lifecycle.Readiness()
	registration := models.Registration{
		ContractVersion:   contractVersion,
		InstanceID:        readiness.InstanceID,
		ReplicaID:         readiness.ReplicaID,
		AppliedGeneration: readiness.Generation,
		Ready:             readiness.Ready,
	}
	if err := registration.Validate(); err != nil {
		return models.Registration{}, err
	}
	return registration, nil
}
