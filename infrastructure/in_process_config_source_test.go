package infrastructure

import (
	"context"
	"errors"
	"testing"

	"github.com/Liapoldus/plugin-sdk/v2/domain/interfaces"
	"github.com/Liapoldus/plugin-sdk/v2/domain/models"
)

func TestInProcessConfigurationSourceExposesOnlyTwoExactSlots(t *testing.T) {
	active := testInProcessConfiguration(t, "generation-active", `{"version":2}`)
	previous := testInProcessConfiguration(t, "generation-previous", `{"version":1}`)
	source, err := NewInProcessConfigurationSource(InProcessConfigurationSnapshot{
		InstanceID: "forms-db",
		Active:     active,
		Previous:   &previous,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := source.PullExact(context.Background(), active.Generation)
	if err != nil || got.State != interfaces.GenerationStateActive || string(got.Configuration.Bytes()) != `{"version":2}` {
		t.Fatalf("active pull = %#v, %v", got, err)
	}
	got, err = source.PullExact(context.Background(), previous.Generation)
	if err != nil || got.State != interfaces.GenerationStatePrevious || string(got.Configuration.Bytes()) != `{"version":1}` {
		t.Fatalf("previous pull = %#v, %v", got, err)
	}
	if _, err := source.PullExact(context.Background(), "generation-staging"); !errors.Is(err, ErrInProcessGenerationUnavailable) {
		t.Fatalf("staging pull error = %v", err)
	}
}

func TestInProcessConfigurationSourcePublishesAtomicallyAndKeepsScope(t *testing.T) {
	active := testInProcessConfiguration(t, "generation-a", `{"version":1}`)
	source, err := NewInProcessConfigurationSource(InProcessConfigurationSnapshot{InstanceID: "server", Active: active})
	if err != nil {
		t.Fatal(err)
	}
	newActive := testInProcessConfiguration(t, "generation-b", `{"version":2}`)
	if err := source.Publish(InProcessConfigurationSnapshot{InstanceID: "other", Active: newActive}); !errors.Is(err, ErrInvalidInProcessSnapshot) {
		t.Fatalf("scope error = %v", err)
	}
	if err := source.Publish(InProcessConfigurationSnapshot{InstanceID: "server", Active: newActive}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.PullExact(context.Background(), active.Generation); !errors.Is(err, ErrInProcessGenerationUnavailable) {
		t.Fatalf("old generation error = %v", err)
	}
	got, err := source.PullExact(context.Background(), newActive.Generation)
	if err != nil || got.Configuration.Generation != newActive.Generation {
		t.Fatalf("published generation = %#v, %v", got, err)
	}
}

func TestInProcessConfigurationSourceRejectsInvalidSnapshots(t *testing.T) {
	active := testInProcessConfiguration(t, "generation-a", `{"version":1}`)
	active.SHA256 = models.Digest([]byte(`{"version":2}`))
	if _, err := NewInProcessConfigurationSource(InProcessConfigurationSnapshot{InstanceID: "server", Active: active}); !errors.Is(err, ErrInvalidInProcessSnapshot) {
		t.Fatalf("digest error = %v", err)
	}
	active = testInProcessConfiguration(t, "generation-a", `{"version":1}`)
	previous := active
	if _, err := NewInProcessConfigurationSource(InProcessConfigurationSnapshot{InstanceID: "server", Active: active, Previous: &previous}); !errors.Is(err, ErrInvalidInProcessSnapshot) {
		t.Fatalf("duplicate generation error = %v", err)
	}
}

func TestInProcessConfigurationSourceHonoursCancellation(t *testing.T) {
	active := testInProcessConfiguration(t, "generation-a", `{"version":1}`)
	source, err := NewInProcessConfigurationSource(InProcessConfigurationSnapshot{InstanceID: "server", Active: active})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.PullExact(ctx, active.Generation); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func testInProcessConfiguration(t *testing.T, generation, raw string) models.Configuration {
	t.Helper()
	configuration, err := models.NewConfiguration(generation, "plugin-settings/v3", models.Digest([]byte(raw)), []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return configuration
}
