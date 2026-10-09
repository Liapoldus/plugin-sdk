import { createHash } from "node:crypto";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import {
  codeOfOutcome,
  error,
  health,
  isSuccessOutcome,
  maximumBytes,
  mediaType,
  metrics as metricsContract,
  path,
  registrationRequired,
  requiredFields,
  statusOfOutcome,
  transportProblem,
} from "../support/contract";
import { fixture } from "../support/harness";
import {
  Runtime,
  bodyOf,
  type ReloadAcknowledgement,
} from "../support/runtime";

const okStatus = 200;
const metricsContractMediaType = mediaType.metrics;
const identicalDescriptorRepeat = "alreadyActive";
const contradictionOfActiveDescriptor = "generationConflict";

function digestOf(contents: string): string {
  return createHash("sha256").update(contents).digest("hex");
}

let runtime: Runtime;
let active: { generation: string; sha256: string; schemaVersion: string };

beforeAll(async () => {
  runtime = await Runtime.start();
  active = {
    generation: runtime.info.generation,
    sha256: runtime.info.digest,
    schemaVersion: fixture.schemaVersion,
  };
}, 120_000);

afterAll(async () => {
  await runtime.stop();
});

describe("a replica Core has not reloaded yet", () => {
  it("publishes readiness under the contract not-ready status before any apply", async () => {
    const ready = await runtime.get(path("ready"), mediaType.readiness);

    expect(ready.status).toBe(error("notReady").status);
    expect(ready.mediaType).toContain(mediaType.readiness);
    const document = JSON.parse(ready.text) as Record<string, unknown>;
    expect(Object.keys(document).sort()).toEqual([...requiredFields("readiness")].sort());
    expect(document[fixture.keys.ready]).toBe(false);
    expect(document[fixture.keys.generation]).toBe("");
    expect(document[fixture.keys.sha256]).toBe("");
    expect(document[fixture.keys.schemaVersion]).toBe("");
    expect(document[fixture.keys.pendingGeneration]).toBe("");
    expect(document[fixture.keys.instanceId]).toBe(fixture.instanceId);
    expect(document[fixture.keys.replicaId]).toBe(fixture.replicaId);
  });

  it("publishes a registration that claims no applied generation", async () => {
    const identity = await runtime.get(path("identity"), mediaType.registration);
    const document = JSON.parse(identity.text) as Record<string, unknown>;

    expect(identity.status).toBe(okStatus);
    expect(identity.mediaType).toContain(mediaType.registration);
    expect(Object.keys(document).sort()).toEqual([...registrationRequired].sort());
    expect(document[fixture.keys.appliedGeneration]).toBe("");
    expect(document[fixture.keys.ready]).toBe(false);
  });

  it("refuses a descriptor that tries to escape the generation namespace", async () => {
    const refused = await runtime.post(
      path("reload"),
      JSON.stringify({ ...active, generation: "../other-instance" }),
      mediaType.reloadRequest,
    );

    expect(refused.status).toBe(statusOfOutcome("invalidRequest"));
    expect(bodyOf(refused)).toEqual({
      [fixture.keys.outcome]: "invalidRequest",
      [fixture.keys.code]: codeOfOutcome("invalidRequest"),
    });
  });

  it("refuses a reload whose own document repeats a member", async () => {
    const body = [
      `{${JSON.stringify(fixture.keys.generation)}:${JSON.stringify(active.generation)}`,
      `,${JSON.stringify(fixture.keys.generation)}:${JSON.stringify(active.generation)}`,
      `,${JSON.stringify(fixture.keys.sha256)}:${JSON.stringify(active.sha256)}`,
      `,${JSON.stringify(fixture.keys.schemaVersion)}:${JSON.stringify(active.schemaVersion)}}`,
    ].join("");
    const refused = await runtime.post(path("reload"), body, mediaType.reloadRequest);

    expect(refused.status).toBe(statusOfOutcome("invalidRequest"));
    expect(bodyOf(refused)).toEqual({
      [fixture.keys.outcome]: "invalidRequest",
      [fixture.keys.code]: codeOfOutcome("invalidRequest"),
    });
  });

  it("refuses a reload whose document is larger than the contract allows", async () => {
    const refused = await runtime.post(
      path("reload"),
      JSON.stringify({
        ...active,
        generation: "g".repeat(maximumBytes.reloadRequest),
      }),
      mediaType.reloadRequest,
    );

    expect(refused.status).toBe(transportProblem("payloadOversized").status);
    expect(bodyOf(refused)).toEqual({
      [fixture.keys.code]: transportProblem("payloadOversized").code,
    });
  });
});

describe("the exact-generation reload lifecycle", () => {
  it("applies the announced generation and only then acknowledges it", async () => {
    const before = (await runtime.state()).body;
    const reload = await runtime.reloadViaCore(active);
    const after = (await runtime.state()).body;

    expect(reload.status).toBe(okStatus);
    expect(fieldsOf(reload.body)).toEqual([...requiredFields("reloadAcknowledgement")].sort());
    expect(reload.body[fixture.keys.generation]).toBe(active.generation);
    expect(reload.body[fixture.keys.sha256]).toBe(active.sha256);
    expect(reload.body[fixture.keys.schemaVersion]).toBe(active.schemaVersion);
    expect(reload.body[fixture.keys.applied]).toBe(true);
    expect(reload.body[fixture.keys.outcome]).toBe("applied");
    expect(after.applyApplied).toBe(before.applyApplied + 1);
    expect(after.activeGeneration).toBe(active.generation);
    expect(after.activeDigest).toBe(active.sha256);
  });

  it("publishes readiness for the generation it actually applied", async () => {
    const ready = await runtime.get(path("ready"), mediaType.readiness);
    const document = JSON.parse(ready.text) as Record<string, unknown>;

    expect(ready.status).toBe(okStatus);
    expect(document[fixture.keys.ready]).toBe(true);
    expect(document[fixture.keys.generation]).toBe(active.generation);
    expect(document[fixture.keys.sha256]).toBe(active.sha256);
    expect(document[fixture.keys.schemaVersion]).toBe(active.schemaVersion);
    expect(document[fixture.keys.pendingGeneration]).toBe("");
  });

  it("acknowledges a byte-for-byte repeat of the active descriptor without pulling", async () => {
    const before = (await runtime.state()).body;
    const repeat = await runtime.reloadViaCore(active);
    const after = (await runtime.state()).body;

    expect(repeat.status).toBe(okStatus);
    expect(repeat.body[fixture.keys.outcome]).toBe(identicalDescriptorRepeat);
    expect(repeat.body[fixture.keys.applied]).toBe(true);
    expect(repeat.body[fixture.keys.generation]).toBe(active.generation);
    expect(after.applyApplied).toBe(before.applyApplied);
  });

  it("clears a refused pending generation when rollback reannounces the active descriptor", async () => {
    const refusedGeneration = "generation-refused-before-rollback";
    await runtime.publish({
      generation: refusedGeneration,
      rawJSON: fixture.document.wellFormed,
      state: fixture.states.active,
    });
    await runtime.armPullFault(refusedGeneration, fixture.pullFaults.unavailable);
    const refusal = await runtime.reloadViaCore({ ...active, generation: refusedGeneration });
    expect(refusal.body[fixture.keys.outcome]).toBe("coreUnavailable");

    const rollback = await runtime.reloadViaCore(active);
    const readiness = bodyOf(await runtime.get(path("ready"), mediaType.readiness));

    expect(rollback.body[fixture.keys.outcome]).toBe(identicalDescriptorRepeat);
    expect(readiness[fixture.keys.generation]).toBe(active.generation);
    expect(readiness[fixture.keys.pendingGeneration]).toBe("");
  });

  it("refuses a descriptor that contradicts the active generation under the same name", async () => {
    const contradicted = { ...active, sha256: digestOf(fixture.document.corrupt) };
    const conflict = await runtime.reloadViaCore(contradicted);
    const after = (await runtime.state()).body;

    expect(conflict.status).toBe(statusOfOutcome(contradictionOfActiveDescriptor));
    expect(conflict.body[fixture.keys.outcome]).toBe(contradictionOfActiveDescriptor);
    expect(conflict.body[fixture.keys.applied]).toBe(false);
    expect(after.activeGeneration).toBe(active.generation);
    expect(after.activeDigest).toBe(active.sha256);
  });

  it("counts the repeat and the contradiction as bounded lifecycle outcomes", async () => {
    const metrics = await runtime.get(path("metrics"));
    const text = metrics.text;

    expect(metrics.status).toBe(okStatus);
    expect(metrics.mediaType).toBe(metricsContractMediaType);
    expect(text).toContain(`${metricsContract.readyMetricName} 0`);
    expect(text).toContain(`${metricsContract.lifecycleCounterKindLabel}="reload"`);
    for (const outcome of [identicalDescriptorRepeat, contradictionOfActiveDescriptor]) {
      expect(text, `outcome ${outcome}`).toContain(
        `${metricsContract.lifecycleCounterOutcomeLabel}="${outcome}"`,
      );
    }
  });

  it("serves the same counters to Core and to the plugin's own surface", async () => {
    const direct = await runtime.get(path("metrics"));
    const throughCore = await runtime.document<{ text?: string; failure?: string }>(
      fixture.documents.metrics,
    );

    expect(direct.status).toBe(okStatus);
    expect(direct.mediaType).toBe(metricsContractMediaType);
    expect(direct.text).toContain(
      `${metricsContract.lifecycleCounterName}{${metricsContract.lifecycleCounterKindLabel}="reload",${metricsContract.lifecycleCounterOutcomeLabel}="${identicalDescriptorRepeat}"}`,
    );
    expect(throughCore.body.failure).toBe("");
    expect(throughCore.body.text).toBe(direct.text);
  });
});

describe("a refusal of the exact-generation pull", () => {
  const cases: { name: string; mode: string; outcome: string }[] = [
    { name: "a generation Core no longer publishes", mode: fixture.pullFaults.unknown, outcome: "unknownGeneration" },
    { name: "a generation Core no longer desires", mode: fixture.pullFaults.stale, outcome: "staleGeneration" },
    { name: "a pull Core refuses", mode: fixture.pullFaults.denied, outcome: "notPermitted" },
    { name: "a pull Core cancels", mode: fixture.pullFaults.cancelled, outcome: "cancelled" },
    { name: "a pull Core answers too late", mode: fixture.pullFaults.expired, outcome: "deadlineExceeded" },
    { name: "a pull Core cannot be reached", mode: fixture.pullFaults.unavailable, outcome: "coreUnavailable" },
  ];

  for (const scenario of cases) {
    it(`reports ${scenario.outcome} when Core answers ${scenario.name}`, async () => {
      const generation = `generation-${scenario.outcome}`;
      await runtime.publish({
        generation,
        rawJSON: fixture.document.wellFormed,
        state: fixture.states.active,
      });
      await runtime.armPullFault(generation, scenario.mode);
      const before = (await runtime.state()).body;
      const refused = await runtime.reloadViaCore({ ...active, generation });
      const after = (await runtime.state()).body;

      expect(isSuccessOutcome(scenario.outcome)).toBe(false);
      expect(refused.status).toBe(statusOfOutcome(scenario.outcome));
      expect(refused.body[fixture.keys.outcome]).toBe(scenario.outcome);
      expect(refused.body[fixture.keys.applied]).toBe(false);
      expect(after.applyApplied).toBe(before.applyApplied);
      expect(after.activeGeneration).toBe(active.generation);
      expect(after.activeDigest).toBe(active.sha256);
    });
  }

  it("fences the replica with the generation it refused, without claiming it", async () => {
    const generation = "generation-fenced";
    await runtime.publish({
      generation,
      rawJSON: fixture.document.wellFormed,
      state: fixture.states.active,
    });
    await runtime.armPullFault(generation, fixture.pullFaults.unavailable);
    const refused = await runtime.reloadViaCore({ ...active, generation });
    const ready = await runtime.get(path("ready"), mediaType.readiness);
    const document = JSON.parse(ready.text) as Record<string, unknown>;

    expect(refused.status).toBe(statusOfOutcome("coreUnavailable"));
    expect(refused.body[fixture.keys.outcome]).toBe("coreUnavailable");
    expect(refused.body[fixture.keys.applied]).toBe(false);
    expect(ready.status).toBe(error("notReady").status);
    expect(document[fixture.keys.ready]).toBe(false);
    expect(document[fixture.keys.generation]).toBe(active.generation);
    expect(document[fixture.keys.sha256]).toBe(active.sha256);
    expect(document[fixture.keys.schemaVersion]).toBe(active.schemaVersion);
    expect(document[fixture.keys.pendingGeneration]).toBe(generation);
  });
});

describe("a refusal of the document Core announced", () => {
  it("refuses bytes that are not the generation Core announced", async () => {
    const before = (await runtime.state()).body;
    const refused = await runtime.reloadViaCore({
      ...active,
      generation: fixture.generations.corrupt,
    });
    const after = (await runtime.state()).body;

    expect(refused.status).toBe(statusOfOutcome("digestMismatch"));
    expect(refused.body[fixture.keys.outcome]).toBe("digestMismatch");
    expect(refused.body[fixture.keys.applied]).toBe(false);
    expect(after.applyApplied).toBe(before.applyApplied);
    expect(after.activeGeneration).toBe(active.generation);
  });

  it("refuses a document whose members are repeated", async () => {
    const refused = await runtime.reloadViaCore({
      ...active,
      generation: fixture.generations.duplicateKeys,
      sha256: runtime.info.duplicateDigest,
    });

    expect(refused.status).toBe(statusOfOutcome("documentMalformed"));
    expect(refused.body[fixture.keys.outcome]).toBe("documentMalformed");
  });

  it("refuses a schema version the plugin does not own", async () => {
    const generation = "generation-other-schema";
    await runtime.publish({
      generation,
      rawJSON: fixture.document.wellFormed,
      state: fixture.states.active,
    });
    const refused = await runtime.reloadViaCore({
      ...active,
      generation,
      schemaVersion: fixture.otherSchemaVersion,
    });

    expect(refused.status).toBe(statusOfOutcome("schemaVersionMismatch"));
    expect(refused.body[fixture.keys.outcome]).toBe("schemaVersionMismatch");
  });

  it("refuses a document the plugin's own applier rejects, leaving the last one active", async () => {
    const before = (await runtime.state()).body;
    const refused = await runtime.reloadViaCore({
      ...active,
      generation: fixture.generations.applyFailure,
    });
    const after = (await runtime.state()).body;

    expect(refused.status).toBe(statusOfOutcome("applyRejected"));
    expect(refused.body[fixture.keys.outcome]).toBe("applyRejected");
    expect(after.applyRefused).toBe(before.applyRefused + 1);
    expect(after.applyApplied).toBe(before.applyApplied);
    expect(after.activeGeneration).toBe(active.generation);
    expect(after.activeDigest).toBe(active.sha256);
  });
});

describe("the plugin-owned documents a replica serves", () => {
  it("serves the manifest and the configuration schema verbatim", async () => {
    const manifest = await runtime.document<Record<string, unknown>>(fixture.documents.manifest);
    const schema = await runtime.document<Record<string, unknown>>(
      fixture.documents.configurationSchema,
    );

    expect(manifest.body.failure).toBe("");
    expect(manifest.body.document).toEqual({ schemaVersion: fixture.schemaVersion, capabilities: [] });
    expect(schema.body.failure).toBe("");
    expect(schema.body.document).toEqual({ type: "object", additionalProperties: true });
  });

  it("serves the health answer the contract publishes", async () => {
    const served = await runtime.get(path("health"), mediaType.json);

    expect(served.status).toBe(health.status);
    expect(JSON.parse(served.text)).toEqual(health.body);
  });

  it("serves the exact bytes Core published, digest included", () => {
    expect(digestOf(runtime.info.expectedRawJSON)).toBe(runtime.info.digest);
    expect(runtime.info.expectedRawJSON.endsWith("\n")).toBe(true);
  });
});

function fieldsOf(acknowledgement: ReloadAcknowledgement): string[] {
  return Object.keys(acknowledgement).sort();
}
