// Package presentation is the versioned Plugin SDK REST surface: the only layer
// that turns an HTTP request into an application use case and an application
// result back into an HTTP response.
//
// The layer owns transport, and nothing else. It reads the route, the media
// type, the status, the code, the document limit and the deadline of every
// answer from a contract value the plugin's composition root injected from the
// versioned contract asset, so this package spells no path, status, code, media
// type, limit or problem key that a second copy could disagree with. It parses
// contract bytes, embeds an asset, listens on a port, or terminates TLS, and it
// never depends on the infrastructure layer; it is handed already-parsed
// contracts and narrow ports and reaches the application layer only through
// those.
//
// Product configuration stays opaque to it. A Reload notification carries a
// generation descriptor and never a document; the manifest and the
// configuration schema are published as the exact bytes the plugin produced and
// are only refused when they are not one JSON object or exceed the contract
// limit; and the SDK calls the plugin's own validator and applier through the
// injected lifecycle and acknowledges nothing it did not observe.
//
// Nothing it writes can disclose configuration, a secret, a grant handle, a
// certificate, a key, an address or a cause. A refusal is a contract status, a
// contract code and, for a lifecycle refusal, a contract outcome, and it is
// resolved from the contract's own problem and outcome maps.
//
// A HandlerSet is safe for concurrent use and carries no listener: the
// composition root owns the mutual-TLS transport that carries requests here.
package presentation
