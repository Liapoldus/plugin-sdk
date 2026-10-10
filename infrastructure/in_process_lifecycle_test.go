package infrastructure

import (
	"context"
	"testing"

	"github.com/Liapoldus/plugin-sdk/v2/application"
	"github.com/Liapoldus/plugin-sdk/v2/domain/models"
)

type inProcessLifecycleApplier struct {
	applications []models.Configuration
}

func (applier *inProcessLifecycleApplier) Apply(_ context.Context, configuration models.Configuration) error {
	applier.applications = append(applier.applications, configuration)
	return nil
}

type inProcessLifecycleObserver struct {
	outcomes []models.Outcome
}

func (observer *inProcessLifecycleObserver) Observe(_ context.Context, _ string, outcome models.Outcome) {
	observer.outcomes = append(observer.outcomes, outcome)
}

func TestInProcessConfigurationSourceUsesTheSameLifecycleContractAsREST(t *testing.T) {
	active := testLifecycleConfiguration(t, "generation-active", `{"service":"server","enabled":true}`)
	previous := testLifecycleConfiguration(t, "generation-previous", `{"service":"server","enabled":false}`)
	source, err := NewInProcessConfigurationSource(InProcessConfigurationSnapshot{
		InstanceID: "server",
		Active:     active,
		Previous:   &previous,
	})
	if err != nil {
		t.Fatalf("create in-process source: %v", err)
	}
	applier := &inProcessLifecycleApplier{}
	observer := &inProcessLifecycleObserver{}
	identity, err := models.NewReplicaIdentity("server", "replica-1")
	if err != nil {
		t.Fatalf("create replica identity: %v", err)
	}
	lifecycle, err := application.NewLifecycle(application.LifecycleConfiguration{
		Source:   source,
		Applier:  applier,
		Identity: identity,
		Observer: observer,
	})
	if err != nil {
		t.Fatalf("create lifecycle: %v", err)
	}

	request := models.Reload{Generation: active.Generation, SHA256: active.SHA256, SchemaVersion: active.SchemaVersion}
	acknowledgement, err := lifecycle.Reload(context.Background(), request)
	if err != nil || acknowledgement.Outcome != models.OutcomeApplied || len(applier.applications) != 1 {
		t.Fatalf("active reload = %#v, err=%v, applications=%d", acknowledgement, err, len(applier.applications))
	}
	acknowledgement, err = lifecycle.Reload(context.Background(), request)
	if err != nil || acknowledgement.Outcome != models.OutcomeAlreadyActive || len(applier.applications) != 1 {
		t.Fatalf("duplicate reload = %#v, err=%v, applications=%d", acknowledgement, err, len(applier.applications))
	}

	previousRequest := models.Reload{Generation: previous.Generation, SHA256: previous.SHA256, SchemaVersion: previous.SchemaVersion}
	acknowledgement, err = lifecycle.Reload(context.Background(), previousRequest)
	if err == nil || acknowledgement.Outcome != models.OutcomeStaleGeneration || len(applier.applications) != 1 {
		t.Fatalf("previous reload = %#v, err=%v, applications=%d", acknowledgement, err, len(applier.applications))
	}
	if len(observer.outcomes) != 3 {
		t.Fatalf("observer outcomes = %d, want 3", len(observer.outcomes))
	}
}

func TestInProcessReplicaPublishesAndReloadsThroughPublicAdapter(t *testing.T) {
	source, err := NewInProcessConfigurationSourceForInstance("server")
	if err != nil {
		t.Fatal(err)
	}
	applier := &inProcessLifecycleApplier{}
	observer := &inProcessLifecycleObserver{}
	identity, err := models.NewReplicaIdentity("server", "replica-1")
	if err != nil {
		t.Fatal(err)
	}
	replica, err := application.NewInProcessReplica(application.InProcessReplicaConfig{
		Source: source, Applier: applier, Identity: identity, Observer: observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	active := testLifecycleConfiguration(t, "generation-active", `{"enabled":true}`)
	if err := replica.Publish(InProcessConfigurationSnapshot{InstanceID: "server", Active: active}); err != nil {
		t.Fatal(err)
	}
	acknowledgement, err := replica.Reload(context.Background(), models.Reload{
		Generation: active.Generation, SHA256: active.SHA256, SchemaVersion: active.SchemaVersion,
	})
	if err != nil || !acknowledgement.Applied || replica.InstanceID() != "server" || replica.ReplicaID() != "replica-1" {
		t.Fatalf("reload = %#v, err=%v, identity=%s/%s", acknowledgement, err, replica.InstanceID(), replica.ReplicaID())
	}
	registration, err := replica.Registration("plugin-sdk/v2")
	if err != nil || registration.AppliedGeneration != active.Generation || !registration.Ready {
		t.Fatalf("registration = %#v, err=%v", registration, err)
	}
}

func testLifecycleConfiguration(t *testing.T, generation, raw string) models.Configuration {
	t.Helper()
	configuration, err := models.NewConfiguration(generation, "schema-v1", models.Digest([]byte(raw)), []byte(raw))
	if err != nil {
		t.Fatalf("create configuration %s: %v", generation, err)
	}
	return configuration
}
