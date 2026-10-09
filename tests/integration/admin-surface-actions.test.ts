import { createHash } from "node:crypto";
import { describe, expect, it } from "vitest";
import { Runtime } from "../support/runtime";

type ControlResult = Record<string, unknown>;

async function control(runtime: Runtime, path: string, body: unknown = {}): Promise<ControlResult> {
  const response = await fetch(new URL(path, runtime.info.controlURL), {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  });
  const result: unknown = await response.json();
  if (result === null || typeof result !== "object" || Array.isArray(result)) {
    throw new Error("control response must be an object");
  }
  return { status: response.status, ...result };
}

describe("generic plugin Admin Surface REST", () => {
  it("reports a safe static diagnosis when the injected descriptor digest contract is incomplete", async () => {
    const runtime = await Runtime.start();
    try {
      const result = await control(runtime, "/control/contract-validation");

      expect(result.status).toBe(200);
      expect(result.rejected).toBe(true);
      expect(result.diagnostic).toBe("invalid Plugin SDK contract: admin surface digest algorithm");
      expect(String(result.diagnostic)).not.toContain("secret");
    } finally {
      await runtime.stop();
    }
  }, 60_000);

  it("returns the exact plugin-owned descriptor and a digest of its original bytes over mTLS", async () => {
    const runtime = await Runtime.start();
    try {
      const result = await control(runtime, "/control/admin-surface");
      const descriptor = String(result.descriptor);

      expect(result.status).toBe(200);
      expect(descriptor).toBe('{"version":1,"pages":[{"id":"overview"}]}');
      expect(result.sha256).toBe(`sha256:${createHash("sha256").update(descriptor).digest("hex")}`);
      expect(result.anonymousRejected).toBe(true);
    } finally {
      await runtime.stop();
    }
  }, 60_000);

  it("forwards typed context and exact JSON bytes once, preserving plugin status and body", async () => {
    const runtime = await Runtime.start();
    try {
      const result = await control(runtime, "/control/admin-action", { mode: "normal" });

      expect(result.status).toBe(200);
      expect(result.statusCode).toBe(207);
      expect(result.body).toBe('{"items":[{"id":"record-1"}]}');
      expect(result.input).toBe('{"filter":{"site":"site-a"}}');
      expect(result.calls).toBe(1);
      expect(result.invocation).toMatchObject({
        callerId: "fixture-operator",
        instanceId: "fixture-instance",
        pageId: "overview",
        actionId: "list",
        surfaceDigest: "sha256:fixture-surface",
        requestId: "fixture-request",
        idempotencyKey: "fixture-idempotency",
        ifMatch: '"fixture-surface"',
      });
    } finally {
      await runtime.stop();
    }
  }, 60_000);

  it("honors the Admin Action deadline beyond the shorter config-pull timeout", async () => {
    const runtime = await Runtime.start();
    try {
      const result = await control(runtime, "/control/admin-action", { mode: "slow" });

      expect(result.status).toBe(200);
      expect(result.statusCode).toBe(207);
      expect(result.body).toBe('{"items":[{"id":"record-1"}]}');
    } finally {
      await runtime.stop();
    }
  }, 60_000);

  it("rejects malformed context, duplicate and unknown context headers, and unsafe path segments", async () => {
    const runtime = await Runtime.start();
    try {
      const result = await control(runtime, "/control/admin-action-probes");

      expect(result.status).toBe(200);
      expect(result.duplicateHeaderStatus).toBe(400);
      expect(result.unknownHeaderStatus).toBe(400);
      expect(result.pathTraversalStatus).toBeGreaterThanOrEqual(400);
      expect(result.pathTraversalStatus).toBeLessThan(500);
      expect(result.callbackCalls).toBe(0);
      expect(result.anonymousRejected).toBe(true);
    } finally {
      await runtime.stop();
    }
  }, 60_000);

  it("bounds request and receipt bytes, propagates cancellation, and redacts handler errors", async () => {
    const runtime = await Runtime.start();
    try {
      const result = await control(runtime, "/control/admin-action-probes");

      expect(result.status).toBe(200);
      expect(result.oversizedRequestRefused).toBe(true);
      expect(result.oversizedReceiptStatus).toBe(500);
      expect(result.oversizedReceiptBody).not.toContain("secret-payload");
      expect(result.handlerErrorStatus).toBe(500);
      expect(result.handlerErrorBody).not.toContain("secret-payload");
      expect(result.cancellationObserved).toBe(true);
    } finally {
      await runtime.stop();
    }
  }, 60_000);
});
