import { describe, expect, it } from "vitest";
import { Runtime } from "../support/runtime";

describe("Core-to-plugin artifact streaming", () => {
  it("streams bounded multipart through the SDK client to the mTLS SDK handler", async () => {
    const runtime = await Runtime.start();
    try {
      const response = await fetch(new URL("/control/artifact-stream", runtime.info.controlURL), {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ bytes: 2 * 1024 * 1024 }),
      });
      const result = (await response.json()) as {
      status: number;
      bytes: number;
      sha256: string;
      metadata: string;
      anonymousRejected: boolean;
      receipt: Record<string, unknown>;
      generatedBytes: number;
      invocation: Record<string, unknown>;
      };

      expect(response.status).toBe(200);
      expect(result.status).toBe(202);
      expect(result.bytes).toBe(result.generatedBytes);
      expect(result.sha256).toMatch(/^sha256:[a-f0-9]{64}$/);
      expect(result.metadata).toBe('{"operation":"fixture"}');
      expect(result.anonymousRejected).toBe(true);
      expect(result.receipt).toMatchObject({ state: "accepted" });
      expect(result.invocation).toMatchObject({
        callerId: "fixture-operator",
        instanceId: "fixture-instance",
        pageId: "sites",
        actionId: "publish",
        surfaceDigest: "sha256:fixture-surface",
        idempotencyKey: "fixture-idempotency",
        requestId: "fixture-request",
        ifMatch: '"fixture-revision"',
      });
    } finally {
      await runtime.stop();
    }
  }, 60_000);

  it("rejects early callback completion, malformed multipart, invalid receipt status, and propagates cancellation", async () => {
    const runtime = await Runtime.start();
    try {
      const response = await fetch(new URL("/control/artifact-probes", runtime.info.controlURL), {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: "{}",
      });
      const result = (await response.json()) as {
        wrongOrderStatus: number;
        filenameStatus: number;
        extraPartStatus: number;
        metadataOverLimitStatus: number;
        metadataBoundaryStatus: number;
        callbackCallsAfterMalformed: number;
        earlyCallbackRejected: boolean;
        cancellationObserved: boolean;
        artifactStatusRejected: boolean;
        oversizedArtifactRejected: boolean;
      };

      expect(response.status).toBe(200);
      expect(result.wrongOrderStatus).toBeGreaterThanOrEqual(400);
      expect(result.filenameStatus).toBeGreaterThanOrEqual(400);
      expect(result.extraPartStatus).toBeGreaterThanOrEqual(400);
      expect(result.metadataOverLimitStatus).toBe(413);
      expect(result.metadataBoundaryStatus).toBe(202);
      expect(result.callbackCallsAfterMalformed).toBe(2);
      expect(result.earlyCallbackRejected).toBe(true);
      expect(result.cancellationObserved).toBe(true);
      expect(result.artifactStatusRejected).toBe(true);
      expect(result.oversizedArtifactRejected).toBe(true);
    } finally {
      await runtime.stop();
    }
  }, 60_000);
});
