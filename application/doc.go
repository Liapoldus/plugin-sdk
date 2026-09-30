// Package application coordinates the generic Core-to-plugin REST lifecycle use
// cases of a plugin replica. It is the only layer that owns business policy, and
// it depends only on domain/models, domain/interfaces and the standard library:
// it never imports HTTP, TLS, an adapter, presentation, a specific plugin,
// `pluginprotocol` or Core.
//
// The invariants of this layer are:
//
//   - Opaque product configuration. A configuration document is a single
//     immutable JSON object that the SDK never decodes, canonicalises or
//     remarshals. The SDK validates generic syntax only, hands the exact bytes
//     Core stored to the plugin-owned applier, and never interprets a product
//     field, capability, setting or route.
//   - Verify, then apply. A document is applied only after its generation,
//     digest and schema version have been verified against both the exact bytes
//     and the Reload announcement, and only when Core still reports the
//     generation as desired. Verification is never delegated to the pull.
//   - Acknowledge only what was activated. A positive acknowledgement means the
//     plugin-owned validator and applier returned success for the exact pulled
//     document. Every refusal carries a distinct outcome from the closed
//     vocabulary of domain/models and leaves the previously active
//     configuration fully active and reported by Readiness.
//   - No internal retry and no replay. The SDK never repeats a pull or an apply
//     on its own initiative, and it never replays a call whose outcome is
//     unknown. Repeated progress is made by Core re-announcing a generation,
//     where idempotency decides.
//   - No secrets in output. A configuration document, a secret value, a grant
//     handle, a certificate, a peer address and a transport cause never reach an
//     error, an acknowledgement, a metric label or a log field. Redaction,
//     bounded label vocabularies and destroyable secret buffers are the only
//     ways value leaves this layer.
//   - Operator-managed process. This layer starts, stops, installs and supervises
//     nothing. A replica is a manually started process that serves the generic
//     lifecycle surface over per-replica mutual TLS and shuts down when the
//     operating system stops it.
//   - No product knowledge. Nothing here names a product field, a provider, a
//     database driver, a public route or a product contract. Endpoint paths,
//     status codes, limits, error codes, label names and redaction keys are
//     versioned contract assets read by the outer layers and passed in here as
//     parameters.
//
// The use cases are: Lifecycle for Reload, Readiness and Registration for the
// replica's own view of itself, SecretManager for scoped one-use secret grants,
// Recorder and LoggingObserver for bounded observability, and RedactFields for
// the record redaction policy.
package application
