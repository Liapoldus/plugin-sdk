import { existsSync } from "node:fs";
import contract from "../../infrastructure/assets/plugin-sdk/v2/peer-directory-poll.json";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const root = resolve(fileURLToPath(new URL("../..", import.meta.url)));
const contractPath = resolve(root, "infrastructure/assets/plugin-sdk/v2/peer-directory-poll.json");

describe("authenticated peer-directory polling contract", () => {
  it("fixes conditional long-poll, bounded wait, caller binding and response semantics", () => {
    expect(existsSync(contractPath)).toBe(true);

    expect(contract.contractVersion).toBe("liapoldus.plugin-sdk.peer-directory-poll.v1");
    expect(contract.endpoint).toEqual({ method: "GET", path: "/internal/v2/plugin-peer-directory" });
    expect(contract.identityBinding).toBe("authenticated-client-certificate-uri-san");
    expect(contract.transportSecurity).toEqual({
      tlsRequired: true,
      clientCertificateRequired: true,
      certificateIdentity: "spiffe-uri-template",
    });
    expect(contract.identity).toEqual({
      uriTemplate: "spiffe://liapoldus/plugin/{instanceId}/{replicaId}/{incarnationId}",
      commonNameUsed: false,
    });
    expect(contract.poll).toMatchObject({
      initialRequest: "immediate-current-directory",
      unchanged: "304-not-modified",
      changed: "200-directory",
      waitParameter: "waitMs",
      defaultWaitMs: 20000,
      maximumWaitMs: 20000,
      requestDeadlineMs: 25000,
      etag: "strong-generation-tag",
      ifNoneMatch: "exact-current-etag-only",
      initialWait: "must-be-zero",
      cancellation: "abort-pending-wait",
      retry: "caller-controlled-no-automatic-replay",
    });
    expect(contract.requests).toEqual({
      maximumBytes: 0,
      queryParameters: ["waitMs"],
      unknownQueryParameters: "reject",
    });
    expect(contract.responses.directory).toMatchObject({
      status: 200,
      maximumBytes: 1_048_576,
      schema: "https://liapoldus.github.io/spec/plugin-sdk/v2/peer-directory.schema.json",
    });
    expect(contract.responses.unchanged).toEqual({
      status: 304,
      maximumBytes: 0,
      mediaType: "none",
      etag: "same-as-if-none-match",
    });
    expect(contract.headers.request).toEqual(["accept:application/json", "if-none-match:strong-generation-tag"]);
    expect(contract.headers.response).toEqual(["etag:strong-generation-tag", "cache-control:no-store"]);
    expect(contract.problems).toEqual({
      invalidRequest: { status: 400, code: "invalid_request" },
      unauthenticated: { status: 401, code: "unauthenticated" },
      identityMismatch: { status: 403, code: "identity_mismatch" },
      unavailable: { status: 503, code: "core_unavailable" },
    });
  });
});
