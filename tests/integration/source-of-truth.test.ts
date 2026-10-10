import { required } from "../support/value";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { relative, resolve } from "node:path";
import { describe, expect, it } from "vitest";

import { contract, projectRoot } from "../support/contract";

const layers = ["domain", "application", "infrastructure", "presentation"] as const;
const productionRoots = layers.map((layer) => resolve(projectRoot, layer));
const assetsRoot = resolve(projectRoot, "infrastructure/assets");

function goFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) {
      return entry.isSymbolicLink() ? [] : goFiles(path);
    }
    return entry.isFile() && entry.name.endsWith(".go") && !entry.name.endsWith("_test.go") ? [path] : [];
  });
}

function productionSources(): { path: string; source: string }[] {
  return productionRoots.flatMap(goFiles).map((path) => ({
    path: relative(projectRoot, path),
    source: readFileSync(path, "utf8"),
  }));
}

function collectStrings(value: unknown, found: string[] = []): string[] {
  if (typeof value === "string") {
    found.push(value);
  } else if (Array.isArray(value)) {
    for (const entry of value) {
      collectStrings(entry, found);
    }
  } else if (value !== null && typeof value === "object") {
    for (const entry of Object.values(value)) {
      collectStrings(entry, found);
    }
  }
  return found;
}

// The values a second copy of the contract would most likely reintroduce: the
// route paths, the outcome and problem codes that go on the wire, the media
// types, the metric and label names, and the trust prefix.
//
// The outcome vocabulary itself is deliberately excluded. domain/models/outcome.go
// owns the closed set of names the whole SDK agrees on, so those names are
// expected to appear in production Go; what must never be duplicated is the wire
// encoding of a refusal. Help text, the version string and the bare HTTP methods
// are excluded for the same reason: they are not a per-endpoint value.
function guardedValues(): string[] {
  const metrics = contract.plugin.responses.metrics;
  const vocabulary = new Set([
    ...collectStrings(contract.outcomeProblems),
    ...collectStrings(contract.outcomes),
  ]);
  const paths = collectStrings(contract.plugin.endpoints).filter((value) =>
    value.startsWith("/"),
  );
  // A code that is spelled exactly like an outcome belongs to the domain-owned
  // vocabulary rather than to the wire encoding, so it is left out with it.
  const codes = [
    ...collectStrings(contract.errors),
    ...collectStrings(contract.problems),
  ].filter((value) => /^[a-z][a-z0-9_]*$/.test(value) && !vocabulary.has(value));
  const metricNames = [metrics.readyMetricName, metrics.lifecycleCounterName];
  const metricFailures = [metrics.pullFailureCounterName];
  // The counter label names are ordinary identifiers ("kind", "outcome") that
  // production Go uses for its own fields, so they are asserted to be published
  // rather than guarded against duplication. The fully qualified metric names are
  // the values a second copy of the contract would reintroduce.

  return [
    ...new Set([
      ...paths,
      ...codes,
      ...collectStrings(contract.plugin.responses.contentTypes),
      ...metricNames,
      ...metricFailures,
      contract.transportSecurity.peerIdentity.uniformResourceIdentifierPrefix,
    ]),
  ].filter((value) => value.length > 2);
}

describe("typed definitions own constants and JSON assets publish them", () => {
  const contractValues = guardedValues();

  it("publishes a value set large enough to be worth guarding", () => {
    expect(contractValues.length).toBeGreaterThan(15);
    expect(new Set(contractValues).size).toBe(contractValues.length);
    expect(contractValues.some((value) => value.startsWith("/")), "a route").toBe(true);
    expect(contractValues.some((value) => /^[a-z][a-z0-9_]*$/.test(value)), "a code").toBe(
      true,
    );
  });

  it("keeps routes, codes, media types, metric names and trust prefixes in the typed owner", () => {
    const offenders: string[] = [];

    for (const { path, source } of productionSources()) {
      if (path === "infrastructure/contract_definitions.go") continue;
      for (const value of contractValues) {
        if (source.includes(value)) {
          offenders.push(`${path} repeats ${value}`);
        }
      }
    }

    expect(offenders).toEqual([]);
  });

  it("publishes a metric name, help text and label for every counter it declares", () => {
    const metrics = contract.plugin.responses.metrics;

    for (const name of [
      metrics.readyMetricName,
      metrics.lifecycleCounterName,
      metrics.pullFailureCounterName,
    ]) {
      expect(name, "a metric name").toMatch(/^liapoldus_[a-z0-9_]+$/);
    }
    for (const label of [metrics.lifecycleCounterKindLabel, metrics.lifecycleCounterOutcomeLabel]) {
      expect(label, "a counter label").toMatch(/^[a-z][a-z0-9_]*$/);
    }
    for (const [field, help] of [
      ["readyMetricHelp", metrics.readyMetricHelp],
      ["lifecycleCounterHelp", metrics.lifecycleCounterHelp],
      ["pullFailureCounterHelp", metrics.pullFailureCounterHelp],
    ]) {
      expect(help, `the help of ${String(field)}`).toBeTruthy();
      expect(required(help).length).toBeLessThanOrEqual(contract.logging.maximumValueLength);
    }
  });

  it("keeps the contract version itself in one place and pins the loader to it", () => {
    const version = contract.contractVersion;
    const pinning = productionSources().filter((file) => file.source.includes(version));

    expect(version).toBeTruthy();
    expect(pinning.map((file) => file.path)).toEqual(["infrastructure/contract.go"]);
    expect(
      required(pinning.find((file) => file.path === "infrastructure/contract.go")).source,
      "the loader must refuse a contract it does not own",
    ).toMatch(new RegExp(`expectedContractVersion\\s*=\\s*"${version.replace(/\./g, "\\.")}"`));
  });

  it("does not load generated contracts or schemas as runtime configuration", () => {
    const embeds = productionSources().flatMap((file) =>
      [...file.source.matchAll(/go:embed\s+(\S+)/g)].map((match) => `${file.path}: ${String(match[1])}`),
    );

    expect(embeds).toEqual([]);
  });

  it("loads static constants from typed definitions without JSON decoders", () => {
    for (const path of ["contract.go", "replica_lifecycle_contract.go", "peer_directory_poll_contract.go", "loopback_plaintext_profile.go"]) {
      const source = readFileSync(resolve(projectRoot, "infrastructure", path), "utf8");
      expect(source).not.toMatch(/json\.(NewDecoder|Unmarshal)/);
      // Schemas and constants are code-owned; public JSON is generated.
    }
  });

  it("keeps only versioned SDK contracts and schemas in the asset tree", () => {
    const assets = readdirSync(assetsRoot, { recursive: true, withFileTypes: true })
      .filter((entry) => entry.isFile())
      .map((entry) => relative(projectRoot, resolve(assetsRoot, entry.parentPath, entry.name)));

    expect(assets.sort()).toEqual([
      "infrastructure/assets/plugin-sdk/v2/http-contract.json",
      "infrastructure/assets/plugin-sdk/v2/loopback-plaintext-profile.json",
      "infrastructure/assets/plugin-sdk/v2/peer-directory-poll.json",
      "infrastructure/assets/plugin-sdk/v2/peer-directory.schema.json",
      "infrastructure/assets/plugin-sdk/v2/replica-lifecycle.json",
      "infrastructure/assets/plugin-sdk/v2/replica-lifecycle.schema.json",
    ]);
    expect(statSync(resolve(assetsRoot)).isDirectory()).toBe(true);
  });
});

describe("no second lifecycle model exists behind the contract", () => {
  it("keeps every production layer free of a plugin-side rollback", () => {
    const offenders = productionSources()
      .filter((file) => /rollback/i.test(file.source))
      .map((file) => file.path);

    expect(offenders).toEqual([]);
  });

  it("defines exactly one Reload use case in the application layer", () => {
    const declarations = productionSources()
      .filter((file) => file.path.startsWith("application/"))
      .flatMap((file) =>
        [...file.source.matchAll(/^func\s+\([^)]*\)\s+Reload\(/gm)].map(
          (match) => `${file.path}: ${match[0].trim()}`,
        ),
      );

    expect(declarations).toEqual([
      "application/in_process_replica.go: func (replica *InProcessReplica) Reload(",
      "application/lifecycle.go: func (lifecycle *Lifecycle) Reload(",
    ]);
  });

  it("defines exactly one reload handler on the plugin surface", () => {
    const handlers = productionSources().flatMap((file) =>
      [...file.source.matchAll(/^func\s+\([^)]*\)\s+handleReload\(/gm)].map(
        (match) => `${file.path}: ${match[0].trim()}`,
      ),
    );

    expect(handlers).toEqual(["presentation/handlers.go: func (set *HandlerSet) handleReload("]);
  });

  it("serves the metadata documents verbatim instead of re-encoding them", () => {
    const handler = readFileSync(resolve(projectRoot, "presentation/handlers.go"), "utf8");
    const serveMetadata = /func \(set \*HandlerSet\) serveMetadata\([\s\S]*?\n}\n/.exec(handler)?.[0];

    expect(serveMetadata, "the plugin surface has one metadata writer").toBeTruthy();
    expect(serveMetadata).toContain("set.writeDocument(writer, document.MediaType");
    expect(serveMetadata).not.toMatch("writeJSON");
    expect(serveMetadata).not.toMatch(/json\.Unmarsh/);
  });

  it("keeps the SDK free of a product setting, a product capability and a product route", () => {
    const forbidden = [
      /"database"\s*:/,
      /"smtp"\s*:/,
      /"upstream"\s*:/,
      /"captcha"/i,
      /"identity"\s*:\s*\{\s*"/,
      /\bcaddy\b/i,
      /sqlite/i,
      /docker/i,
      /kubernetes/i,
    ];
    const offenders: string[] = [];

    for (const { path, source } of productionSources()) {
      for (const pattern of forbidden) {
        if (pattern.test(source)) {
          offenders.push(`${path} matches ${String(pattern)}`);
        }
      }
    }

    expect(offenders).toEqual([]);
  });

  it("keeps every product string the tests own out of the production tree", () => {
    const fixtureOnly = ["fixture/v1", "generation-active", "fixture-instance", "other/v1"];
    const offenders: string[] = [];

    for (const { path, source } of productionSources()) {
      for (const value of fixtureOnly) {
        if (source.includes(value)) {
          offenders.push(`${path} contains ${value}`);
        }
      }
    }

    expect(offenders).toEqual([]);
  });
});

describe("the SDK depends on nothing but its own domain", () => {
  it("imports no Core, pluginprotocol, Caddy or third-party module", () => {
    const module = readFileSync(resolve(projectRoot, "go.mod"), "utf8");
    const allowed = /^module\s+github\.com\/Liapoldus\/plugin-sdk\/v2$/m;
    const requirements = [...module.matchAll(/^require\s+(.*)$/gm)].flatMap((match) =>
      required(match[1]).trim().split(/\s+/),
    );

    expect(allowed.test(module), "the module path is the canonical SDK path").toBe(true);
    expect(requirements, "the SDK requires no module").toEqual([]);
  });

  it("imports only the four layers and the standard library from production code", () => {
    const offenders: string[] = [];
    const importBlock = /^import\s*\(([\s\S]*?)^\)/gm;
    const importLine = /^(?:import\s+)?(?:\w+\s+)?"([^"]+)"/gm;

    for (const { path, source } of productionSources()) {
      const specs: string[] = [];
      for (const block of source.matchAll(importBlock)) {
        specs.push(...[...required(block[1]).matchAll(importLine)].map((match) => required(match[1])));
      }
      for (const spec of [...source.matchAll(/^import\s+(?:\w+\s+)?"([^"]+)"/gm)]) {
        specs.push(required(spec[1]));
      }
      for (const imported of specs) {
        const internal = imported.startsWith("github.com/Liapoldus/plugin-sdk/v2/");
        const foreign = !internal && imported.includes(".");
        if (foreign) {
          offenders.push(`${path} imports ${imported}`);
        }
      }
    }

    expect(offenders).toEqual([]);
  });
});
