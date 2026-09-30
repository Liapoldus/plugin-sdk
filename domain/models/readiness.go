package models

// Readiness reports the configuration generation this replica has actually
// applied. It never claims a generation that failed validation or application.
// PendingGeneration names the most recent generation this replica refused, so
// Core can fence a degraded replica without the plugin guessing desired state.
type Readiness struct {
	Ready             bool   `json:"ready"`
	Generation        string `json:"generation"`
	SHA256            string `json:"sha256"`
	SchemaVersion     string `json:"schemaVersion"`
	PendingGeneration string `json:"pendingGeneration"`
	InstanceID        string `json:"instanceId"`
	ReplicaID         string `json:"replicaId"`
}
