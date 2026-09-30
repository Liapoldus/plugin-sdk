import { createHash } from "node:crypto";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { fixture } from "../support/harness";
import { Runtime } from "../support/runtime";

let runtime: Runtime;

beforeAll(async () => {
  runtime = await Runtime.start();
}, 120_000);

afterAll(async () => {
  await runtime.stop();
});

describe("a loaded surface asked to drain", () => {
  it("delivers the response a request in flight was promised, then stops listening", async () => {
    const started = await runtime.load("start");

    expect(started.status).toBe(200);
    expect(started.body.url).toBeTruthy();
    const address = new URL(String(started.body.url));
    expect(
      address.protocol,
      "the surface that drains has to be a mutual-TLS surface, not a plaintext one",
    ).toBe("https:");

    const drained = await runtime.load("shutdown");

    expect(drained.status).toBe(200);
    expect(
      drained.body.failure ?? "",
      "a drain that runs out of grace is a failure, not a slow success",
    ).toBe("");
    expect(
      drained.body.inFlight,
      "the request has to be in flight when the drain starts, or nothing was drained",
    ).toBeGreaterThanOrEqual(1);
    expect(drained.body.served).toBeGreaterThanOrEqual(1);
    expect(
      drained.body.status,
      "the in-flight request was answered, not cut off",
    ).toBe(200);
    expect(drained.body.bodyBytes, "the response was truncated").toBe(
      Buffer.byteLength(fixture.load.document, "utf8"),
    );
    expect(
      drained.body.bodySHA256,
      "the bytes the client received are not the bytes the server promised",
    ).toBe(createHash("sha256").update(fixture.load.document, "utf8").digest("hex"));
    expect(
      drained.body.elapsedMilliseconds ?? Number.POSITIVE_INFINITY,
      "the drain has to finish on its own, not by waiting out the contract grace",
    ).toBeLessThan(fixture.load.boundedMilliseconds);
    expect(
      drained.body.afterShutdown,
      "a drained surface must stop answering on the address it was serving",
    ).toBe("refused");
    expect(runtime.stderr, "a clean drain must not panic").not.toMatch(/panic:/);
  });
});
