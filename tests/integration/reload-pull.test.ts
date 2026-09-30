import { createHash } from "node:crypto";
import https from "node:https";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import {
  codeOfOutcome,
  contract,
  endpointNames,
  error,
  health,
  mediaType,
  maximumBytes,
  method,
  metrics as metricsContract,
  path,
  pullPath,
  registrationRequired,
  responseHeader,
  statusOfOutcome,
  transportProblem,
} from "../support/contract";
import { fixture } from "../support/harness";
import { Runtime, bodyOf, type Answer } from "../support/runtime";

const okStatus = 200;
const applied = "applied";
const invalidRequest = "invalidRequest";
const unregisteredMethods = ["PUT", "PATCH", "DELETE"] as const;

// The fixture publishes the private control-plane CA and the one Core client
// certificate and key it generated for this replica, so the tests below can speak
// to Core exactly the way the SDK does: with a real mutual-TLS handshake and
// full certificate verification. They are per-run test material and never appear
// in an assertion, a log line or a failure message.
type CoreCredentials = {
  caBundlePEM: string;
  corePlaneClientCertificatePEM: string;
  corePlaneClientKeyPEM: string;
};

let runtime: Runtime;
let credentials: CoreCredentials;
let active: { generation: string; sha256: string; schemaVersion: string };

beforeAll(async () => {
  runtime = await Runtime.start();
  credentials = runtime.info as unknown as CoreCredentials;
  active = {
    generation: runtime.info.generation,
    sha256: runtime.info.digest,
    schemaVersion: fixture.schemaVersion,
  };
}, 120_000);

afterAll(async () => {
  await runtime.stop();
});

function reloadRequest(body: string, contentType?: string): Promise<Answer> {
  return runtime.post(path("reload"), body, contentType);
}

function reloadAt(generation: string, sha256: string, schemaVersion: string): string {
  return JSON.stringify({ generation, sha256, schemaVersion });
}

// digestOf is the digest Core announces for a document, computed the same way the
// SDK computes it, so a scenario can publish a document and then announce it.
function digestOf(document: string): string {
  return createHash("sha256").update(document).digest("hex");
}

// A reload notification carrying one field the contract does not declare, so the
// transport turns it away as an invalid request without a well-formed generation
// ever being involved.
function undeclaredFieldRequest(schemaVersion: string): string {
  return JSON.stringify({
    generation: "generation-undeclared-field",
    sha256: "0".repeat(64),
    schemaVersion,
    undeclared: true,
  });
}

// A well-formed JSON object of exactly the requested serialized length, used to
// reach a byte boundary on the pull response without guessing its size from a
// prefix and a literal label.
function documentOfLength(target: number): string {
  const prefix = '{"padding":"';
  const suffix = '"}';

  return prefix + "d".repeat(Math.max(0, target - prefix.length - suffix.length)) + suffix;
}

// A control-plane call is made with the credential material named the way the
// fixture's ready line names it, so a scenario can withhold one part of it.
// An omitted field means "do not offer that part to the handshake".
type CoreMaterial = {
  caBundlePEM?: string;
  corePlaneClientCertificatePEM?: string;
  corePlaneClientKeyPEM?: string;
};

type CoreRequest = {
  method?: string;
  headers?: Record<string, string>;
  body?: string;
  // omitted: the full fixture credential. null: no authority and no client
  // certificate. { caBundlePEM }: the trusted authority but no client
  // certificate. { corePlaneClientCertificatePEM, corePlaneClientKeyPEM }: a
  // client certificate the caller does not trust the authority of.
  credentials?: CoreMaterial | null;
};

// A pull answer also carries the response header set, because the contract names
// headers the plugin surface never publishes.
type CoreAnswer = Answer & { headers: Record<string, string> };

// coreRequest performs one control-plane call over mutual TLS with the default
// Node verification left in place: the handshake succeeds only when the fixture
// certificate chains to the authority in the CA bundle, and the call is answered
// only when this replica presents a certificate the server requires.
function coreRequest(route: string, request: CoreRequest = {}): Promise<CoreAnswer> {
  const url = new URL(route, runtime.info.coreURL);
  const material = request.credentials === undefined ? credentials : request.credentials;
  return new Promise<CoreAnswer>((resolve, reject) => {
    const call = https.request(
      {
        host: url.hostname,
        port: Number(url.port),
        path: `${url.pathname}${url.search}`,
        method: request.method ?? "GET",
        headers: request.headers,
        // The Core leaf carries a DNS SAN for localhost and an IP SAN for the
        // loopback address the fixture listens on, so the name below is verified
        // against the certificate rather than skipped.
        servername: "localhost",
        ca: material?.caBundlePEM,
        cert: material?.corePlaneClientCertificatePEM,
        key: material?.corePlaneClientKeyPEM,
        agent: false,
        timeout: 10_000,
      },
      (response) => {
        const chunks: Buffer[] = [];
        response.on("data", (chunk: Buffer) => chunks.push(chunk));
        response.once("end", () => {
          const headers = response.headers as Record<string, string | undefined>;
          resolve({
            status: response.statusCode ?? 0,
            mediaType: headers["content-type"] ?? "",
            allow: headers.allow ?? null,
            text: Buffer.concat(chunks).toString("utf8"),
            headers: Object.fromEntries(
              Object.entries(headers).map(([name, value]) => [name, String(value)]),
            ),
          });
        });
        response.once("error", reject);
      },
    );
    call.once("error", reject);
    call.once("timeout", () => call.destroy(new Error("the Core call timed out")));
    if (request.body !== undefined) {
      call.write(request.body);
    }
    call.end();
  });
}

// controlHeader reads a pull response header case-insensitively, because the
// contract names headers in mixed case and the transport lowercases them.
function controlHeader(answer: CoreAnswer, name: string): string {
  for (const [header, value] of Object.entries(answer.headers)) {
    if (header.toLowerCase() === name.toLowerCase()) {
      return value;
    }
  }

  return "";
}

describe("the routes the plugin surface publishes", () => {
  it("registers exactly the endpoints the contract names, with no others", () => {
    const registered = endpointNames.map((name) => `${method(name)} ${path(name)}`);

    expect(new Set(registered).size).toBe(registered.length);
    expect(registered).toEqual(
      Object.entries(contract.plugin.endpoints).map(
        ([, entry]) => `${entry.method} ${entry.path}`,
      ),
    );
  });

  it("serves every read-only endpoint at its registered path", async () => {
    for (const name of endpointNames.filter((each) => each !== "reload" && each !== "ready")) {
      const answer = await runtime.get(path(name));
      expect(answer.status, `${method(name)} ${path(name)}`).toBe(okStatus);
    }
  });

  it("serves the reload endpoint at its registered path", async () => {
    const answer = await reloadRequest(
      reloadAt("generation-absent", "0".repeat(64), active.schemaVersion),
      mediaType.reloadRequest,
    );

    expect(path("reload")).toBe(contract.plugin.endpoints.reload.path);
    expect(answer.status).toBe(statusOfOutcome("unknownGeneration"));
  });

  for (const name of endpointNames.filter((each) => each !== "reload")) {
    it(`refuses a write to the read-only ${name} endpoint`, async () => {
      const answer = await runtime.post(path(name), "{}", mediaType.json);

      expect(answer.status).toBe(transportProblem("methodNotAllowed").status);
      expect(answer.allow).toBe(method(name));
      expect(bodyOf(answer)).toEqual({
        [fixture.keys.code]: transportProblem("methodNotAllowed").code,
      });
    });
  }

  it("refuses a read of the reload endpoint, which accepts only a write", async () => {
    const answer = await runtime.get(path("reload"));

    expect(answer.status).toBe(transportProblem("methodNotAllowed").status);
    expect(answer.allow).toBe(method("reload"));
    expect(bodyOf(answer)).toEqual({
      [fixture.keys.code]: transportProblem("methodNotAllowed").code,
    });
  });

  for (const candidate of unregisteredMethods) {
    it(`refuses ${candidate} on the reload endpoint`, async () => {
      const answer = await runtime.send(runtime.pluginURL, path("reload"), {
        method: candidate,
        headers: { "content-type": mediaType.reloadRequest },
        body: JSON.stringify(active),
      });

      expect(answer.status).toBe(transportProblem("methodNotAllowed").status);
      expect(answer.allow).toBe(method("reload"));
    });
  }

  it("answers every path outside the contract with the transport not-found refusal", async () => {
    const outside = [
      "/",
      "/_liapoldus",
      "/_liapoldus/v1",
      "/_liapoldus/v2/ready",
      `${path("ready")}/`,
      `${path("ready")}/extra`,
      `${path("reload")}/generation-active`,
      path("ready").toUpperCase(),
    ];

    for (const route of outside) {
      const answer = await runtime.get(route);
      expect(answer.status, `GET ${route}`).toBe(transportProblem("notFound").status);
      expect(bodyOf(answer), `GET ${route}`).toEqual({
        [fixture.keys.code]: transportProblem("notFound").code,
      });
    }
  });

  it("publishes no process control, no plugin-side rollback and no test control route", async () => {
    const absent = [
      "/_liapoldus/v1/shutdown",
      "/_liapoldus/v1/restart",
      "/_liapoldus/v1/rollback",
      "/_liapoldus/v1/apply",
      "/_liapoldus/v1/activate",
      "/_liapoldus/v1/config",
      "/_liapoldus/v1/previous",
      "/_liapoldus/v1/secret",
      "/_liapoldus/v1/secrets",
      "/_liapoldus/v1/registration",
      ...Object.values(fixture.control),
    ];

    for (const route of new Set(absent)) {
      const read = await runtime.get(route);
      expect(read.status, `GET ${route}`).toBe(transportProblem("notFound").status);
      const write = await runtime.post(route, "{}", mediaType.json);
      expect(write.status, `POST ${route}`).toBe(transportProblem("notFound").status);
    }
  });
});

describe("the reload request the SDK will accept", () => {
  // The bodies are built inside each test: the active descriptor only exists
  // once the replica has announced readiness, which happens after collection, so
  // a table built at collection time would read it as undefined.
  const invalidRequestNames = [
    "a generation that leaves the generation namespace",
    "a generation that is an absolute path",
    "a descriptor that repeats a member",
    "a descriptor that omits a required member",
    "a descriptor that carries a member the contract does not define",
    "a descriptor that is a JSON array rather than an object",
    "a descriptor with no document at all",
    "a descriptor whose members are the wrong JSON type",
    "a descriptor with a value after the object",
  ];

  const invalidRequestRefusals = (): { name: string; body: string }[] => [
    {
      name: "a generation that leaves the generation namespace",
      body: JSON.stringify({ ...active, generation: "../other-instance" }),
    },
    {
      name: "a generation that is an absolute path",
      body: JSON.stringify({ ...active, generation: "/etc/passwd" }),
    },
    {
      name: "a descriptor that repeats a member",
      body: [
        `{${JSON.stringify(fixture.keys.generation)}:${JSON.stringify(active.generation)}`,
        `,${JSON.stringify(fixture.keys.generation)}:${JSON.stringify(active.generation)}`,
        `,${JSON.stringify(fixture.keys.sha256)}:${JSON.stringify(active.sha256)}`,
        `,${JSON.stringify(fixture.keys.schemaVersion)}:${JSON.stringify(active.schemaVersion)}}`,
      ].join(""),
    },
    {
      name: "a descriptor that omits a required member",
      body: JSON.stringify({ generation: active.generation, sha256: active.sha256 }),
    },
    {
      name: "a descriptor that carries a member the contract does not define",
      body: JSON.stringify({ ...active, document: "anything" }),
    },
    {
      name: "a descriptor that is a JSON array rather than an object",
      body: JSON.stringify([active.generation, active.sha256, active.schemaVersion]),
    },
    {
      name: "a descriptor with no document at all",
      body: "",
    },
    {
      name: "a descriptor whose members are the wrong JSON type",
      body: JSON.stringify({ ...active, generation: 1 }),
    },
    {
      name: "a descriptor with a value after the object",
      body: `${JSON.stringify(active)}${JSON.stringify(active)}`,
    },
  ];

  for (const name of invalidRequestNames) {
    it(`refuses ${name} with the invalid-request outcome`, async () => {
      const body = invalidRequestRefusals().find((entry) => entry.name === name)?.body;
      expect(body, `a body for ${name}`).toBeDefined();
      const answer = await reloadRequest(body as string, mediaType.reloadRequest);

      expect(answer.status).toBe(statusOfOutcome(invalidRequest));
      expect(answer.mediaType).toContain(mediaType.json);
      expect(bodyOf(answer)).toEqual({
        [fixture.keys.outcome]: invalidRequest,
        [fixture.keys.code]: codeOfOutcome(invalidRequest),
      });
    });
  }

  it("refuses a descriptor past the contract request limit, at exactly one byte over", async () => {
    // The limit is on the request bytes themselves, so the body is built to the
    // measured length and its size is asserted before it is sent.
    const probe = (fill: number) =>
      reloadAt("g".repeat(fill), active.sha256, active.schemaVersion);
    const overhead = probe(0).length;
    const body = probe(maximumBytes.reloadRequest + 1 - overhead);

    expect(new TextEncoder().encode(body).length).toBe(maximumBytes.reloadRequest + 1);

    const answer = await reloadRequest(body, mediaType.reloadRequest);

    expect(answer.status).toBe(transportProblem("payloadOversized").status);
    expect(answer.mediaType).toContain(mediaType.json);
    expect(bodyOf(answer)).toEqual({
      [fixture.keys.code]: transportProblem("payloadOversized").code,
    });
  });

  it("accepts the longest generation the contract's identifier bound allows", async () => {
    // The request limit is a transport ceiling, not a reachable size: a generation
    // is an identifier, so a well-formed descriptor can never reach
    // reloadRequest.maximumBytes. The boundary that is reachable is the
    // identifier bound, and one byte past it must be refused as invalid.
    const identifierLimit = contract.identity.replica.maximumBytes as number;
    const longest = (fill: number) =>
      reloadAt("g".repeat(fill), active.sha256, active.schemaVersion);

    expect(identifierLimit).toBeLessThan(maximumBytes.reloadRequest);
    expect(longest(identifierLimit).length).toBeLessThan(maximumBytes.reloadRequest);
    const generation = (JSON.parse(longest(identifierLimit)) as { generation: string })
      .generation;
    await runtime.publish({ generation, rawJSON: fixture.document.wellFormed });
    const accepted = await reloadRequest(longest(identifierLimit), mediaType.reloadRequest);

    expect(accepted.status).toBe(okStatus);

    const tooLong = reloadAt(
      "g".repeat(identifierLimit + 1),
      active.sha256,
      active.schemaVersion,
    );
    const refused = await reloadRequest(tooLong, mediaType.reloadRequest);

    expect(refused.status).toBe(statusOfOutcome(invalidRequest));
    expect(bodyOf(refused)).toEqual({
      [fixture.keys.outcome]: invalidRequest,
      [fixture.keys.code]: codeOfOutcome(invalidRequest),
    });
  });

  for (const contentType of [mediaType.metrics, "text/plain", "application/xml"]) {
    it(`refuses a reload announced as ${contentType}`, async () => {
      const answer = await reloadRequest(JSON.stringify(active), contentType);

      expect(answer.status).toBe(transportProblem("unsupportedMediaType").status);
      expect(bodyOf(answer)).toEqual({
        [fixture.keys.code]: transportProblem("unsupportedMediaType").code,
      });
    });
  }

  it("accepts a reload announced with media type parameters", async () => {
    const answer = await reloadRequest(
      reloadAt("generation-absent", "0".repeat(64), active.schemaVersion),
      `${mediaType.reloadRequest}; charset=utf-8`,
    );

    expect(answer.status).toBe(statusOfOutcome("unknownGeneration"));
  });

  it("refuses a reload that announces no media type at all", async () => {
    const answer = await runtime.send(runtime.pluginURL, path("reload"), {
      method: "POST",
      body: JSON.stringify(active),
    });

    expect(answer.status).toBe(transportProblem("unsupportedMediaType").status);
  });

  it("never applies a generation a refused request announced", async () => {
    const before = (await runtime.state()).body;
    const answer = await reloadRequest(
      JSON.stringify({ ...active, generation: "../other-instance" }),
      mediaType.reloadRequest,
    );
    const after = (await runtime.state()).body;

    expect(answer.status).toBe(statusOfOutcome(invalidRequest));
    expect(after.applyApplied).toBe(before.applyApplied);
    expect(after.activeGeneration).toBe(before.activeGeneration);
    expect(after.activeDigest).toBe(before.activeDigest);
  });
});

describe("the control plane the SDK pulls from", () => {
  it("names the pull path the contract declares and percent-encodes the generation", () => {
    expect(contract.core.configPull.pathTemplate).toContain("{generation}");
    expect(pullPath(active.generation)).toBe(
      contract.core.configPull.pathTemplate.replace("{generation}", active.generation),
    );
    expect(pullPath("a b/c")).not.toContain("a b/c");
  });

  it("publishes only the two durable slots for a generation", () => {
    const state = contract.core.configPull.generationStates as string[];

    expect([...state].sort()).toEqual(
      [fixture.states.active, fixture.states.previous].sort(),
    );
  });

  it("answers a mutual-TLS caller the exact bytes, under every header the contract names", async () => {
    const answer = await coreRequest(pullPath(active.generation), {
      headers: { accept: mediaType.configPull },
    });
    const header = (key: string) => controlHeader(answer, responseHeader[key] as string);

    expect(answer.status).toBe(okStatus);
    expect(answer.mediaType).toContain(mediaType.configPull);
    for (const key of ["generation", "sha256", "schemaVersion", "generationState"]) {
      expect(header(key), `header ${key}`).toBeTruthy();
    }
    expect(header("generation")).toBe(active.generation);
    expect(header("sha256")).toBe(active.sha256);
    expect(header("schemaVersion")).toBe(active.schemaVersion);
    expect(header("generationState")).toBe(fixture.states.active);
    expect(answer.text).toBe(runtime.info.expectedRawJSON);
    expect(answer.text.length).toBeLessThanOrEqual(maximumBytes.configPull);
  });

  it("serves the exact bytes Core published, digest included", () => {
    const digest = createHash("sha256")
      .update(runtime.info.expectedRawJSON)
      .digest("hex");

    expect(digest).toBe(runtime.info.digest);
    expect(runtime.info.expectedRawJSON.endsWith("\n")).toBe(true);
  });

  it("refuses a client that presents no certificate on the control plane", async () => {
    await expect(
      coreRequest(pullPath(active.generation), { credentials: null }),
    ).rejects.toThrow();
  });

  it("refuses a client that presents no certificate even when the authority is trusted", async () => {
    await expect(
      coreRequest(pullPath(active.generation), {
        credentials: { caBundlePEM: credentials.caBundlePEM },
      }),
    ).rejects.toThrow();
  });

  it("refuses a bearer token in place of a client certificate", async () => {
    // The contract authenticates a replica by its certificate, so a header is
    // not a way in: a caller that trusts the authority and offers a token still
    // has nothing to present at the handshake, and the token never reaches the
    // point where a server could read it.
    await expect(
      coreRequest(pullPath(active.generation), {
        credentials: { caBundlePEM: credentials.caBundlePEM },
        headers: { authorization: "Bearer not-a-credential" },
      }),
    ).rejects.toThrow();
  });

  it("does not answer a caller that does not trust the control-plane authority", async () => {
    // Without the published authority the fixture certificate is untrusted, which
    // is what proves the call above was verified rather than waved through.
    await expect(
      coreRequest(pullPath(active.generation), {
        credentials: {
          corePlaneClientCertificatePEM: credentials.corePlaneClientCertificatePEM,
          corePlaneClientKeyPEM: credentials.corePlaneClientKeyPEM,
        },
      }),
    ).rejects.toThrow();
  });

  it("answers a generation Core no longer publishes with a not-found refusal", async () => {
    const answer = await coreRequest(pullPath("generation-never-published"));

    // Core is a peer of this contract, so its own refusal is an outcome problem
    // rather than a bare transport problem: the outcome names what happened and
    // the code is the one that outcome owns.
    expect(answer.status).toBe(statusOfOutcome("unknownGeneration"));
    expect(answer.status).toBe(transportProblem("notFound").status);
    expect(answer.mediaType).toContain(mediaType.json);
    expect(bodyOf(answer)).toEqual({
      [fixture.keys.outcome]: "unknownGeneration",
      [fixture.keys.code]: codeOfOutcome("unknownGeneration"),
    });
  });

  it("applies the announced generation and acknowledges only what it activated", async () => {
    const before = (await runtime.state()).body;
    const reload = await runtime.reloadViaCore(active);
    const after = (await runtime.state()).body;

    expect(reload.status).toBe(okStatus);
    expect(Object.keys(reload.body).sort()).toEqual(
      [...contract.plugin.reloadAcknowledgement.required].sort(),
    );
    expect(reload.body[fixture.keys.outcome]).toBe(applied);
    expect(reload.body[fixture.keys.applied]).toBe(true);
    expect(reload.body[fixture.keys.generation]).toBe(active.generation);
    expect(reload.body[fixture.keys.sha256]).toBe(active.sha256);
    expect(reload.body[fixture.keys.schemaVersion]).toBe(active.schemaVersion);
    expect(after.applyApplied).toBe(before.applyApplied + 1);
    expect(after.activeGeneration).toBe(active.generation);
    expect(after.activeDigest).toBe(active.sha256);
  });

  it("refuses a generation Core publishes as the previous slot", async () => {
    const generation = "generation-previous-slot";
    await runtime.publish({
      generation,
      rawJSON: fixture.document.wellFormed,
      state: fixture.states.previous,
    });
    const before = (await runtime.state()).body;
    const refused = await runtime.reloadViaCore({ ...active, generation });
    const after = (await runtime.state()).body;

    expect(refused.status).toBe(statusOfOutcome("staleGeneration"));
    expect(refused.body[fixture.keys.outcome]).toBe("staleGeneration");
    expect(refused.body[fixture.keys.applied]).toBe(false);
    expect(after.applyApplied).toBe(before.applyApplied);
    expect(after.activeGeneration).toBe(before.activeGeneration);
  });

  it("refuses bytes whose digest is not the digest Core announced", async () => {
    const generation = "generation-announced-digest";
    const announced = "1".repeat(64);
    await runtime.publish({
      generation,
      rawJSON: fixture.document.wellFormed,
      announcedDigest: announced,
    });
    const refused = await runtime.reloadViaCore({
      ...active,
      generation,
      sha256: announced,
    });

    expect(refused.status).toBe(statusOfOutcome("digestMismatch"));
    expect(refused.body[fixture.keys.outcome]).toBe("digestMismatch");
    expect(refused.body[fixture.keys.applied]).toBe(false);
  });

  it("refuses a document Core announced under a schema version the plugin does not own", async () => {
    const generation = "generation-announced-schema";
    await runtime.publish({
      generation,
      rawJSON: fixture.document.wellFormed,
      announcedSchemaVersion: fixture.otherSchemaVersion,
    });
    const refused = await runtime.reloadViaCore({ ...active, generation });

    expect(refused.status).toBe(statusOfOutcome("schemaVersionMismatch"));
    expect(refused.body[fixture.keys.outcome]).toBe("schemaVersionMismatch");
  });

  it("refuses a document Core published with a repeated member", async () => {
    const refused = await runtime.reloadViaCore({
      ...active,
      generation: fixture.generations.duplicateKeys,
      sha256: runtime.info.duplicateDigest,
    });

    expect(refused.status).toBe(statusOfOutcome("documentMalformed"));
    expect(refused.body[fixture.keys.outcome]).toBe("documentMalformed");
  });

  it("applies a pull response at exactly the contract document limit, byte for byte", async () => {
    const atLimit = "generation-pull-at-limit";
    const document = documentOfLength(maximumBytes.configPull);

    expect(new TextEncoder().encode(document).length).toBe(maximumBytes.configPull);
    const publishedAtLimit = await runtime.publish({ generation: atLimit, rawJSON: document });

    // The contract limit is the size of the document Core holds, so the scenario
    // is only meaningful if the fixture Core actually accepted one that large.
    expect(
      publishedAtLimit.status,
      `the fixture refused a ${document.length}-byte generation: ${publishedAtLimit.text}`,
    ).toBe(okStatus);
    const appliedAtLimit = await runtime.reloadViaCore({
      ...active,
      generation: atLimit,
      sha256: digestOf(document),
    });

    expect(appliedAtLimit.status).toBe(okStatus);
    expect(appliedAtLimit.body[fixture.keys.outcome]).toBe(applied);
    expect(appliedAtLimit.body[fixture.keys.applied]).toBe(true);

    // The largest document is still only a document: the exact bytes and the
    // announced digest survive the round trip unchanged, and nothing about
    // applying it is special-cased.
    const pulled = await coreRequest(pullPath(atLimit));

    expect(pulled.status).toBe(okStatus);
    expect(pulled.text).toBe(document);
    expect(controlHeader(pulled, responseHeader.sha256)).toBe(digestOf(document));
    expect(digestOf(pulled.text)).toBe(digestOf(document));
  });

  // The one-byte-past boundary is asserted once, in the outcome matrix below,
  // where the status and the code it owns are read off the wire. It is kept there
  // rather than repeated here so the contract boundary has exactly one owner.

  it("keeps the applied generation usable through every pull refusal", async () => {
    const ready = await runtime.get(path("ready"), mediaType.readiness);
    const identity = await runtime.get(path("identity"), mediaType.registration);

    expect(ready.status).toBe(okStatus);
    expect(bodyOf(ready)[fixture.keys.ready]).toBe(true);
    expect(bodyOf(ready)[fixture.keys.generation]).toBeTruthy();
    expect(identity.status).toBe(okStatus);
    expect(Object.keys(bodyOf(identity)).sort()).toEqual([...registrationRequired].sort());
    expect(bodyOf(identity)[fixture.keys.appliedGeneration]).toBeTruthy();
  });
});

describe("the reload status and code each contract outcome owns", () => {
  // A generation the fixture is allowed to serve without being a JSON object, so
  // the outcome a well-formed request against malformed bytes produces is
  // reachable at all.
  const malformedDocument = '{"replica":"fixture","enabled":true';

  // Every case below is observed on the plugin's own mutual-TLS surface, which is
  // where the contract publishes an outcome: a success answers the
  // acknowledgement document, and a refusal answers the problem document carrying
  // the outcome name and the code that outcome owns. A refusal never claims a
  // generation, so it carries no acknowledgement fields at all.
  // The replica publishes its own surface on a plaintext test mirror so a harness
  // can read the documents the contract defines without holding a credential the
  // fixture keeps to itself. The mutual-TLS boundary itself is proved separately,
  // in both directions: against Core by the cases below, and against the replica
  // by the anonymous, untrusted-authority, wrong-peer and plaintext cases.
  async function reloadOnTheSurface(generation: string, sha256: string, body?: string): Promise<Answer> {
    return reloadRequest(
      body ?? reloadAt(generation, sha256, active.schemaVersion),
      mediaType.reloadRequest,
    );
  }
  // stage publishes one active generation and optionally arms the pull failure
  // that stands in for the outcome under test.
  async function stage(generation: string, mode?: string): Promise<void> {
    await runtime.publish({
      generation,
      rawJSON: fixture.document.wellFormed,
      state: fixture.states.active,
    });

    if (mode !== undefined) {
      await runtime.armPullFault(generation, mode);
    }
  }

  type Refusal = {
    outcome: string;
    reach: () => Promise<void>;
    reload: () => Promise<Answer>;
  };

  const refusals: Refusal[] = [
    {
      outcome: "invalidRequest",
      reach: async () => undefined,
      reload: () =>
        reloadOnTheSurface("", "", undeclaredFieldRequest(active.schemaVersion)),
    },
    {
      outcome: "unknownGeneration",
      reach: () => stage("generation-unknown", fixture.pullFaults.unknown),
      reload: () => reloadOnTheSurface("generation-unknown", digestOf(fixture.document.wellFormed)),
    },
    {
      outcome: "staleGeneration",
      reach: () => stage("generation-stale", fixture.pullFaults.stale),
      reload: () => reloadOnTheSurface("generation-stale", digestOf(fixture.document.wellFormed)),
    },
    {
      outcome: "notPermitted",
      reach: () => stage("generation-denied", fixture.pullFaults.denied),
      reload: () => reloadOnTheSurface("generation-denied", digestOf(fixture.document.wellFormed)),
    },
    {
      outcome: "cancelled",
      reach: () => stage("generation-cancelled", fixture.pullFaults.cancelled),
      reload: () => reloadOnTheSurface("generation-cancelled", digestOf(fixture.document.wellFormed)),
    },
    {
      outcome: "deadlineExceeded",
      reach: () => stage("generation-deadline", fixture.pullFaults.expired),
      reload: () => reloadOnTheSurface("generation-deadline", digestOf(fixture.document.wellFormed)),
    },
    {
      outcome: "coreUnavailable",
      reach: () => stage("generation-core-down", fixture.pullFaults.unavailable),
      reload: () => reloadOnTheSurface("generation-core-down", digestOf(fixture.document.wellFormed)),
    },
    {
      outcome: "digestMismatch",
      reach: () =>
        runtime.publish({
          generation: "generation-digest",
          rawJSON: fixture.document.wellFormed,
          state: fixture.states.active,
          announcedDigest: "1".repeat(64),
        }),
      reload: () => reloadOnTheSurface("generation-digest", digestOf(fixture.document.wellFormed)),
    },
    {
      outcome: "schemaVersionMismatch",
      reach: () =>
        runtime.publish({
          generation: "generation-schema",
          rawJSON: fixture.document.wellFormed,
          state: fixture.states.active,
          announcedSchemaVersion: `${fixture.schemaVersion}-other`,
        }),
      reload: () => reloadOnTheSurface("generation-schema", digestOf(fixture.document.wellFormed)),
    },
    {
      outcome: "documentMalformed",
      reach: () =>
        runtime.publish({
          generation: "generation-malformed",
          rawJSON: malformedDocument,
          state: fixture.states.active,
          malformed: true,
        }),
      reload: () => reloadOnTheSurface("generation-malformed", digestOf(malformedDocument)),
    },
    {
      // The fixture seeds one generation its own applier refuses, and it is the
      // only refusal the plugin-owned applier can produce.
      outcome: "applyRejected",
      reach: async () => undefined,
      reload: () =>
        reloadOnTheSurface(fixture.generations.applyFailure, digestOf(fixture.document.wellFormed)),
    },
    {
      outcome: "generationConflict",
      reach: async () => {
        // Applying it once makes the replica hold it, so announcing the same
        // generation with a different digest contradicts what it already applied.
        await stage("generation-conflict");
        await reloadOnTheSurface("generation-conflict", digestOf(fixture.document.wellFormed));
      },
      reload: () => reloadOnTheSurface("generation-conflict", "2".repeat(64)),
    },
    {
      outcome: "documentOversized",
      // One byte past the contract's own document bound. The control surface
      // accepts a body larger than the contract limit on purpose, so this
      // generation exists in the fixture Core and the SDK sees the exact bytes
      // Core published rather than a control refusal standing in for them.
      reach: async () => {
        const oversized = documentOfLength(maximumBytes.configPull + 1);

        expect(new TextEncoder().encode(oversized).length).toBe(maximumBytes.configPull + 1);
        // The store refuses to hold a document this large on its own, because a
        // Core that could would not be a Core the contract describes. The
        // harness has to say the size is deliberate, exactly as it has to say
        // that malformed bytes are, or the oversized-document outcome is a
        // state no generation can ever reach.
        const published = await runtime.publish({
          generation: "generation-oversized",
          rawJSON: oversized,
          state: fixture.states.active,
          oversized: true,
        });

        expect(
          published.status,
          `the control plane refused a ${oversized.length}-byte generation: ${published.text}`,
        ).toBe(okStatus);
      },
      reload: () => reloadOnTheSurface("generation-oversized", digestOf(documentOfLength(maximumBytes.configPull + 1))),
    },
  ];

  for (const scenario of refusals) {
    it(`answers ${scenario.outcome} with ${codeOfOutcome(scenario.outcome)} and nothing else`, async () => {
      await scenario.reach();
      const refused = await scenario.reload();

      expect(refused.status).toBe(statusOfOutcome(scenario.outcome));
      expect(Object.keys(bodyOf(refused)).sort()).toEqual(
        [fixture.keys.outcome, fixture.keys.code].sort(),
      );
      expect(bodyOf(refused)[fixture.keys.outcome]).toBe(scenario.outcome);
      expect(bodyOf(refused)[fixture.keys.code]).toBe(codeOfOutcome(scenario.outcome));
    });
  }

  it("observes every outcome the contract names for reload", () => {
    // The cases above plus the two success outcomes must be the contract's own
    // closed vocabulary, so a new outcome cannot ship unobserved and a renamed
    // one cannot leave a stale case behind.
    const covered = refusals
      .map((scenario) => scenario.outcome)
      .concat(["applied", "alreadyActive"])
      .sort();

    expect(covered).toEqual([...contract.outcomes.reload].sort());
  });

  it("answers applied with the acknowledgement and no code", async () => {
    const document = fixture.document.wellFormed;
    const generation = "generation-applied-over-tls";
    await runtime.publish({ generation, rawJSON: document, state: fixture.states.active });
    const answer = await reloadOnTheSurface(generation, digestOf(document));
    const acknowledgement = bodyOf(answer);

    expect(answer.status).toBe(okStatus);
    expect(answer.mediaType).toContain(mediaType.reloadAcknowledgement);
    expect(Object.keys(acknowledgement).sort()).toEqual(
      [...(contract.plugin.reloadAcknowledgement.required as string[])].sort(),
    );
    expect(acknowledgement[fixture.keys.outcome]).toBe(applied);
    expect(acknowledgement[fixture.keys.applied]).toBe(true);
    expect(acknowledgement).not.toHaveProperty(fixture.keys.code);
  });

  it("answers alreadyActive with the acknowledgement and no code", async () => {
    const document = fixture.document.wellFormed;
    const generation = "generation-already-active-over-tls";
    await runtime.publish({ generation, rawJSON: document, state: fixture.states.active });
    await reloadOnTheSurface(generation, digestOf(document));
    const repeated = await reloadOnTheSurface(generation, digestOf(document));
    const acknowledgement = bodyOf(repeated);

    expect(repeated.status).toBe(okStatus);
    expect(repeated.mediaType).toContain(mediaType.reloadAcknowledgement);
    expect(Object.keys(acknowledgement).sort()).toEqual(
      [...(contract.plugin.reloadAcknowledgement.required as string[])].sort(),
    );
    expect(acknowledgement[fixture.keys.outcome]).toBe("alreadyActive");
    expect(acknowledgement[fixture.keys.applied]).toBe(true);
    expect(acknowledgement).not.toHaveProperty(fixture.keys.code);
  });
});

describe("the plugin-owned documents a replica serves", () => {
  it("serves the manifest and the configuration schema verbatim", async () => {
    const manifest = await runtime.document<Record<string, unknown>>(
      fixture.documents.manifest,
    );
    const schema = await runtime.document<Record<string, unknown>>(
      fixture.documents.configurationSchema,
    );

    expect(manifest.body.failure).toBe("");
    expect(manifest.body.document).toEqual({
      schemaVersion: fixture.schemaVersion,
      capabilities: [],
    });
    expect(schema.body.failure).toBe("");
    expect(schema.body.document).toEqual({ type: "object", additionalProperties: true });
  });

  it("serves the health answer the contract publishes", async () => {
    const answer = await runtime.get(path("health"), mediaType.json);

    expect(answer.status).toBe(health.status);
    expect(answer.mediaType).toContain(mediaType.json);
    expect(bodyOf(answer)).toEqual(health.body);
  });

  it("serves the registration fields the contract requires, within its limit", async () => {
    const answer = await runtime.get(path("identity"), mediaType.registration);

    expect(answer.status).toBe(okStatus);
    expect(answer.mediaType).toContain(mediaType.registration);
    expect(Object.keys(bodyOf(answer)).sort()).toEqual([...registrationRequired].sort());
    expect(answer.text.length).toBeLessThanOrEqual(maximumBytes.registration);
  });

  it("serves Prometheus text under the media type the contract names", async () => {
    const answer = await runtime.get(path("metrics"));

    expect(answer.status).toBe(okStatus);
    expect(answer.mediaType).toBe(mediaType.metrics);
    expect(answer.text.length).toBeLessThanOrEqual(maximumBytes.metadata);
  });

  it("names every metric and every help line the contract publishes", async () => {
    const answer = await runtime.get(path("metrics"));

    for (const [name, help] of [
      [metricsContract.readyMetricName, metricsContract.readyMetricHelp],
      [metricsContract.lifecycleCounterName, metricsContract.lifecycleCounterHelp],
      [metricsContract.pullFailureCounterName, metricsContract.pullFailureCounterHelp],
    ] as [string, string][]) {
      expect(answer.text, `metric ${name}`).toContain(`# HELP ${name} ${help}`);
      expect(answer.text, `metric ${name}`).toContain(`# TYPE ${name} `);
    }
  });

  it("reports readiness and every bounded outcome it has recorded", async () => {
    const answer = await runtime.get(path("metrics"));
    const series = new Map<string, number>();
    for (const line of answer.text.split("\n")) {
      const match = /^([a-z_]+)(\{[^}]*\})? (-?\d+(?:\.\d+)?)$/.exec(line);
      if (match) {
        series.set(`${match[1]}${match[2] ?? ""}`, Number(match[3]));
      }
    }

    expect(series.get(metricsContract.readyMetricName as string)).toBe(1);
    const label = (outcome: string) =>
      `${metricsContract.lifecycleCounterName}{${metricsContract.lifecycleCounterKindLabel}="reload",${metricsContract.lifecycleCounterOutcomeLabel}="${outcome}"}`;
    for (const outcome of [
      applied,
      "unknownGeneration",
      "staleGeneration",
      "digestMismatch",
      "schemaVersionMismatch",
      "documentMalformed",
    ]) {
      expect(series.get(label(outcome)), `reload ${outcome}`).toBeGreaterThan(0);
    }
    expect(series.get(metricsContract.pullFailureCounterName as string)).toBeGreaterThan(0);
  });

  it("counts a refusal the presentation layer answers before any pull as no pull failure", async () => {
    const before = await runtime.get(path("metrics"));
    await reloadRequest(
      JSON.stringify({ ...active, generation: "../nope" }),
      mediaType.reloadRequest,
    );
    const after = await runtime.get(path("metrics"));
    const counter = metricsContract.pullFailureCounterName as string;
    const read = (answer: Answer) =>
      new RegExp(`^${counter} (\\d+)$`, "m").exec(answer.text)?.[1];

    expect(read(after)).toBe(read(before));
  });
});

describe("a replica Core has not reloaded yet", () => {
  // Its own replica: the shared runtime above has already been asked about
  // several generations, and pendingGeneration legitimately records the last one
  // this replica refused. Only a replica that has refused nothing can show the
  // empty pending value the contract describes.
  let fresh: Runtime;

  beforeAll(async () => {
    fresh = await Runtime.start();
  }, 120_000);

  afterAll(async () => {
    await fresh.stop();
  });

  it("answers readiness with the contract refusal", async () => {
    const answer = await fresh.get(path("ready"), mediaType.readiness);
    const document = bodyOf(answer);

    expect(answer.status).toBe(error("notReady").status);
    expect(answer.mediaType).toContain(mediaType.readiness);
    expect(Object.keys(document).sort()).toEqual(
      [...contract.plugin.readiness.required].sort(),
    );
    expect(document[fixture.keys.ready]).toBe(false);
    expect(document[fixture.keys.generation]).toBe("");
    expect(document[fixture.keys.sha256]).toBe("");
    expect(document[fixture.keys.schemaVersion]).toBe("");
    expect(document[fixture.keys.pendingGeneration]).toBe("");
  });

  it("records the generation it refuses, and never claims it", async () => {
    const refused = await fresh.post(
      path("reload"),
      reloadAt("generation-refused-here", "0".repeat(64), fixture.schemaVersion),
      mediaType.reloadRequest,
    );
    const after = bodyOf(await fresh.get(path("ready"), mediaType.readiness));

    expect(refused.status).toBe(statusOfOutcome("unknownGeneration"));
    expect(after[fixture.keys.ready]).toBe(false);
    expect(after[fixture.keys.generation]).toBe("");
    expect(after[fixture.keys.pendingGeneration]).toBe("generation-refused-here");
  });
});
