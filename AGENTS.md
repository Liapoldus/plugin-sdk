# Plugin SDK agent instructions

This directory is a standalone Go module for generic Core↔plugin REST lifecycle.
Its module path is temporary (`liapoldus.local/plugin-sdk`); do not publish it,
assign a canonical path, or invent a Git remote.

## Architecture

- Keep exactly four production layers: `domain/`, `application/`,
  `infrastructure/`, and `presentation/`.
- `domain/` contains only `models/` and `interfaces/`. Keep business policy in
  `application/`; network, TLS, HTTP, and embedded static contract assets belong
  in `infrastructure/`; HTTP handlers belong in `presentation/`.
- Dependencies point inward: domain imports no other layer; application imports
  domain; infrastructure imports domain; presentation imports domain and
  application. Do not add a fifth layer or product-specific packages.
- `presentation/` must not import `infrastructure/`. Composition — contract
  loading, mutual-TLS client and server, listeners, process lifecycle and
  shutdown — belongs in the plugin's own `main` or another package outside
  these four layers; `tests/fixtures/reload-runtime/main.go` is the worked
  example.
- The SDK owns generic Core↔plugin lifecycle only. It must not depend on Core,
  `pluginprotocol`, Caddy, or any product plugin. `pluginprotocol` owns generic
  plugin↔plugin communication only.
- Product configuration remains an opaque JSON object to the SDK. Preserve the
  exact bytes returned by Core, verify generation/schema-version/digest, reject
  duplicate JSON object keys, and call the plugin-owned validator/applier before
  acknowledging `Reload`. Never interpret product fields.
- `Rollback` is a Core Management API operation; do not add a plugin-side
  rollback endpoint. Plugins pull exact generations only after Core invokes
  `Reload`.
- Follow the versioned HTTP contract in
  `infrastructure/assets/plugin-sdk/v1/http-contract.json`. Do not duplicate
  endpoint paths, limits, response codes, or product contracts elsewhere as a
  second source of truth.
- Require per-replica mTLS on Core↔plugin HTTP. Do not add plaintext or
  bearer-only downgrade paths. Do not place application settings or secrets in
  environment variables, process arguments, logs, errors, metrics, or ACKs.

## Tests and checks

- Keep TypeScript conformance tests under `tests/`; Go production packages must
  not contain `*_test.go` files.
- Add/update the failing TypeScript test before implementing behavior, then keep
  the completed slice green.
- Run `make check` before handing off a completed SDK slice.
- The child-process fixture is test-only. Its plaintext listener and any
  insecure TLS options must never enter production packages.
- When an owner has approved a target contract, remove superseded lifecycle
  behavior as part of the coordinated replacement. Do not ship permanent
  compatibility aliases, old-endpoint fallbacks, or two concurrent lifecycle
  models. Keep consumer migration, tests, and the new SDK contract aligned in
  the final integrated change; never hand off a state that still depends on the
  retired protocol lifecycle.
