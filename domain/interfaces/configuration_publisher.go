package interfaces

import "github.com/Liapoldus/plugin-sdk/v2/domain/models"

// ConfigurationPublisher is the Core-owned publication side of an explicit
// in-process composition. Plugins receive immutable active/previous slots and
// never obtain a durable store handle or a persistence reference.
type ConfigurationPublisher interface {
	Publish(models.InProcessConfigurationSnapshot) error
}
