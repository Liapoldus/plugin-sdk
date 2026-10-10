package models

// InProcessConfigurationSnapshot is the only configuration state visible to a
// trusted in-process plugin. It contains the same two durable slots as the REST
// pull contract; staging and arbitrary generation lookup are not representable.
type InProcessConfigurationSnapshot struct {
	InstanceID string
	Active     Configuration
	Previous   *Configuration
}
