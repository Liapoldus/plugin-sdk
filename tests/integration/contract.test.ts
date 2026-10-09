import { describe, expect, it } from "vitest";

import {
  codeOfOutcome,
  contract,
  endpoint,
  error,
  contractFile,
  endpointNames,
  errorKeys,
  isSuccessOutcome,
  mediaType,
  maximumBytes,
  outcomeFamilies,
  outcomeNames,
  outcomesOf,
  problemKeys,
  problemOfOutcome,
  pullPath,
  requiredFields,
  responseHeader,
  statusOfOutcome,
  successOutcomes,
  transportProblem,
} from "../support/contract";

const httpMethods = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"];
const tlsVersion12 = 772;
const durableSlots = ["active", "previous"];

describe("the versioned HTTP contract asset", () => {
  it("publishes one contract version and one file for it", () => {
    expect(typeof contract.contractVersion).toBe("string");
    expect(contract.contractVersion.length).toBeGreaterThan(0);
    expect(contractFile).toMatch(/assets[/\\]plugin-sdk[/\\]v1[/\\]http-contract\.json$/);
  });

  it("registers every plugin endpoint once, with a unique method and path", () => {
    const pairs = endpointNames.map(
      (name) => `${endpoint(name).method} ${endpoint(name).path}`,
    );
    const paths = endpointNames.map((name) => endpoint(name).path);
    const writes = pairs.filter((pair) => !pair.startsWith("GET "));

    expect(new Set(paths).size).toBe(paths.length);
    expect(new Set(pairs).size).toBe(pairs.length);
    expect(writes, "the plugin publishes lifecycle, artifact, and admin action write paths").toHaveLength(3);
    for (const [index, name] of endpointNames.entries()) {
      expect(pairs[index], `method of ${name}`).toMatch(
        new RegExp(`^${httpMethods.join("|")} /`),
      );
      expect(paths[index], `path of ${name}`).not.toContain(" ");
    }
  });

  it("publishes the bounded artifact stream endpoint and envelope contract", () => {
    const endpoint = contract.plugin.endpoints.artifactStream;
    const stream = contract.plugin.artifactStream;

    expect(endpoint).toEqual({ method: "POST", path: (expect.stringMatching(/^\/_liapoldus\/v1\//) as unknown) });
    expect(stream).toMatchObject({
      mediaType: "multipart/form-data",
      parts: ["metadata", "artifact"],
      partOrder: ["metadata", "artifact"],
      maximumArtifactBytes: 134217728,
      maximumMetadataBytes: 65536,
      maximumMultipartOverheadBytes: 65536,
      maximumRequestBytes: 134348800,
      maximumReceiptBytes: (expect.any(Number) as unknown),
      acceptedStatus: 202,
      filenameForwarded: false,
      invocationContext: {
        maximumBytes: 8192,
        required: ["callerId", "instanceId", "pageId", "actionId", "surfaceDigest", "idempotencyKey", "requestId"],
        optional: ["ifMatch"],
        headers: {
          callerId: "Liapoldus-Caller",
          instanceId: "Liapoldus-Instance",
          pageId: "Liapoldus-Page",
          actionId: "Liapoldus-Action",
          surfaceDigest: "Liapoldus-Surface-Digest",
          idempotencyKey: "Idempotency-Key",
          requestId: "Liapoldus-Request-ID",
          ifMatch: "If-Match",
        },
      },
    });
    expect(stream.maximumRequestBytes).toBe(
      stream.maximumArtifactBytes + stream.maximumMetadataBytes + stream.maximumMultipartOverheadBytes,
    );
  });

  it("publishes bounded, generic admin surface discovery and action contracts", () => {
    expect(contract.plugin.endpoints.adminSurface).toEqual({
      method: "GET",
      path: "/_liapoldus/v1/admin-surface",
    });
    expect(contract.plugin.endpoints.adminAction).toEqual({
      method: "POST",
      path: "/_liapoldus/v1/admin-action/{page}/{action}",
    });
    expect(contract.plugin.adminSurface).toMatchObject({
      mediaType: mediaType.json,
      maximumBytes: 262144,
      digestAlgorithm: "SHA-256",
    });
    expect(contract.plugin.adminAction).toMatchObject({
      mediaType: mediaType.json,
      maximumRequestBytes: 1048576,
      maximumResponseBytes: 1048576,
      deadlineSeconds: (expect.any(Number) as unknown),
      pathSegmentPattern: (expect.any(String) as unknown),
      invocationContext: {
        maximumBytes: 8192,
        required: ["callerId", "instanceId", "pageId", "actionId", "surfaceDigest", "requestId"],
        optional: ["idempotencyKey", "ifMatch"],
        unknownHeaderPrefix: (expect.any(String) as unknown),
      },
      responseStatus: { minimum: 200, maximum: 599 },
    });
  });

  it("declares a single media type per document and one usable value for each", () => {
    for (const [name, value] of Object.entries(mediaType)) {
      expect(value, `media type of ${name}`).toBeTruthy();
      expect(value, `media type of ${name}`).toMatch(/^[a-z]+\/[a-z0-9.+-]+/);
    }
  });

  it("declares a positive limit for every document and caps metadata at its documents", () => {
    for (const [name, value] of Object.entries(maximumBytes)) {
      expect(Number.isInteger(value), `limit of ${name}`).toBe(true);
      expect(value, `limit of ${name}`).toBeGreaterThan(0);
    }
    expect(maximumBytes.metadata).toBeGreaterThanOrEqual(maximumBytes.manifest);
    expect(maximumBytes.metadata).toBeGreaterThanOrEqual(maximumBytes.configurationSchema);
  });

  it("names the required members of every document that publishes a member list", () => {
    for (const document of ["reloadRequest", "reloadAcknowledgement", "readiness"]) {
      const required = requiredFields(document);
      expect(required.length, `required members of ${document}`).toBeGreaterThan(0);
      expect(new Set(required).size, `required members of ${document}`).toBe(required.length);
    }
    expect(requiredFields("reloadRequest")).toContain("generation");
    expect(requiredFields("reloadAcknowledgement")).toContain("outcome");
    expect(requiredFields("readiness")).toContain("ready");
    expect(contract.identity.registration.required.length).toBeGreaterThan(0);
  });

  it("gives every transport refusal a status and a unique snake_case code", () => {
    const codes = new Set<string>();
    for (const key of problemKeys) {
      const problem = transportProblem(key);
      expect(problem.status, `status of ${key}`).toBeGreaterThanOrEqual(400);
      expect(problem.status, `status of ${key}`).toBeLessThan(600);
      expect(problem.code, `code of ${key}`).toMatch(/^[a-z][a-z0-9_]*$/);
      expect(codes.has(problem.code), `code ${problem.code} is shared`).toBe(false);
      codes.add(problem.code);
    }
  });

  it("gives every error a status and a code no transport refusal already owns", () => {
    const transport = new Set(problemKeys.map((key) => transportProblem(key).code));
    const codes = new Set<string>();
    for (const key of errorKeys) {
      const problem = error(key);
      expect(problem.status, `status of ${key}`).toBeGreaterThanOrEqual(400);
      expect(problem.status, `status of ${key}`).toBeLessThan(600);
      expect(problem.code, `code of ${key}`).toMatch(/^[a-z][a-z0-9_]*$/);
      expect(transport.has(problem.code), `code ${problem.code} is shared with a transport refusal`).toBe(
        false,
      );
      expect(codes.has(problem.code), `code ${problem.code} is shared`).toBe(false);
      codes.add(problem.code);
    }
  });

  it("resolves every registered outcome problem to a defined error", () => {
    for (const [outcome, key] of Object.entries(contract.outcomeProblems)) {
      expect(contract.errors[key], `problem key of ${outcome}`).toBeDefined();
    }
  });

  it("publishes no refusal for a success outcome and no success for a refusal", () => {
    for (const outcome of successOutcomes) {
      expect(contract.outcomeProblems[outcome], `problem of success ${outcome}`).toBeUndefined();
    }
    for (const outcome of Object.keys(contract.outcomeProblems)) {
      expect(isSuccessOutcome(outcome), `success of refusal ${outcome}`).toBe(false);
    }
  });

  it("gives every family a unique, resolvable, non-empty outcome list", () => {
    for (const family of outcomeFamilies) {
      const outcomes = outcomesOf(family);
      expect(outcomes.length, `outcomes of ${family}`).toBeGreaterThan(0);
      expect(new Set(outcomes).size, `outcomes of ${family}`).toBe(outcomes.length);
      for (const outcome of outcomes) {
        if (isSuccessOutcome(outcome)) {
          continue;
        }
        expect(problemOfOutcome(outcome), `problem of ${family}/${outcome}`).toBeDefined();
      }
      expect(
        outcomes.filter(isSuccessOutcome).length,
        `successes of ${family}`,
      ).toBeGreaterThan(0);
    }
  });

  it("keeps each call family's own successes inside that family", () => {
    const perFamily = new Map<string, string[]>();
    const seen = new Set<string>();
    for (const family of outcomeFamilies) {
      const successes = outcomesOf(family).filter(isSuccessOutcome);
      expect(successes.length, `successes of ${family}`).toBeGreaterThan(0);
      for (const outcome of successes) {
        expect(seen.has(outcome), `${outcome} is a success in two families`).toBe(false);
        seen.add(outcome);
      }
      perFamily.set(family, successes);
    }
    expect([...seen].sort()).toEqual([...successOutcomes].sort());
    expect(perFamily.get("reload")).toContain(
      contract.idempotency.repeatOfActiveGeneration,
    );
    expect(perFamily.get("configPull")?.filter((outcome) => perFamily.get("reload")?.includes(outcome)))
      .toEqual([]);
    expect(
      perFamily.get("secretRedemption")?.filter((outcome) => perFamily.get("reload")?.includes(outcome)),
    ).toEqual([]);
  });

  it("gives a shared refusal one meaning in every family that lists it", () => {
    const shared = outcomeNames.filter(
      (outcome) => outcomeFamilies.filter((family) => outcomesOf(family).includes(outcome)).length > 1,
    );
    expect(shared.length, "the contract shares at least one refusal across families").toBeGreaterThan(
      0,
    );
    for (const outcome of shared) {
      expect(isSuccessOutcome(outcome), `${outcome} is shared as a success`).toBe(false);
      const key = contract.outcomeProblems[outcome];
      expect(contract.errors[key as string], `problem of ${outcome}`).toBeDefined();
      const families = outcomeFamilies.filter((family) => outcomesOf(family).includes(outcome));
      expect(families.length, `families of ${outcome}`).toBeGreaterThan(1);
    }
  });

  it("refuses a conflicting descriptor for the active generation with a conflict", () => {
    const repeat = contract.idempotency.repeatOfActiveGeneration;
    const conflict = contract.idempotency.conflictingDescriptorForActiveGeneration;

    expect(isSuccessOutcome(repeat), repeat).toBe(true);
    expect(outcomesOf("reload")).toContain(repeat);
    expect(isSuccessOutcome(conflict), conflict).toBe(false);
    expect(problemOfOutcome(conflict).status).toBeGreaterThanOrEqual(400);
    expect(problemOfOutcome(conflict).status).toBeLessThan(500);
  });

  it("keys a reload by the replica identity and the whole descriptor", () => {
    const keyed = contract.idempotency.reloadKeyedBy;

    expect(new Set(keyed).size).toBe(keyed.length);
    for (const required of ["generation", "sha256", "schemaVersion"]) {
      expect(keyed).toContain(required);
    }
    for (const required of contract.identity.replica.fields) {
      expect(keyed).toContain(required);
    }
    expect(keyed.length).toBe(contract.identity.replica.fields.length + 3);
  });

  it("publishes only the two durable slots for a generation pull", () => {
    const states = contract.core.configPull.generationStates;

    expect([...states].sort()).toEqual([...durableSlots].sort());
  });

  it("registers every response header a pull must carry and names the pull path", () => {
    for (const key of ["generation", "sha256", "schemaVersion", "generationState"]) {
      expect(responseHeader[key], `response header ${key}`).toBeTruthy();
    }
    expect(new Set(Object.values(responseHeader)).size).toBe(Object.keys(responseHeader).length);
    expect(contract.core.configPull.pathTemplate).toContain("{generation}");
    expect(pullPath("one-generation")).toContain("one-generation");
    expect(pullPath("one-generation")).not.toContain("{generation}");
  });

  it("requires a client certificate at the contract TLS floor and fails closed on revocation", () => {
    expect(contract.transportSecurity.minimumTLSVersion).toBe(tlsVersion12);
    expect(contract.transportSecurity.clientCertificateRequired).toBe(true);
    expect(contract.transportSecurity.peerIdentity.revocationFailClosed).toBe(true);
    expect(contract.transportSecurity.peerIdentity.commonNameRequired).toBe(true);
    expect(contract.transportSecurity.peerIdentity.uniformResourceIdentifierPrefix).toMatch(
      /^[a-z][a-z0-9+.-]*:\/\/.+/,
    );
    expect(
      contract.transportSecurity.peerIdentity.commonNameMaximumLength,
    ).toBeGreaterThan(0);
  });

  it("grants a secret exactly once and bounds what it accepts and returns", () => {
    const secret = contract.core.secretGrant;

    expect(secret.redemptionUseLimit).toBe(1);
    expect(secret.issue.required).toContain("generation");
    expect(secret.redemption.required).toContain("handle");
    expect(secret.handleMaximumLength).toBeGreaterThan(0);
    expect(secret.referenceMaximumLength).toBeGreaterThan(0);
    expect(secret.purposeMaximumLength).toBeGreaterThan(0);
    expect(secret.issue.maximumRequestBytes).toBeGreaterThan(0);
    expect(secret.issue.maximumResponseBytes).toBeGreaterThan(0);
    expect(secret.redemption.maximumResponseBytes).toBeGreaterThan(secret.issue.maximumResponseBytes);
  });

  it("redacts the members that can carry a credential, document or setting", () => {
    const always = contract.logging.alwaysRedactedKeys;
    const redacted = (contract.logging.redactedKeys).map((key) => key.toLowerCase());

    expect(always.length).toBeGreaterThan(0);
    expect(new Set(always).size).toBe(always.length);
    for (const key of always) {
      const token = key.toLowerCase();
      const covered = redacted.some((each) => token.includes(each));
      expect(covered, `always-redacted key ${key} is inside the redacted set`).toBe(true);
    }
    for (const key of redacted) {
      expect(key, `redacted key ${key}`).toMatch(/^[a-z0-9]+$/);
    }
    expect(contract.logging.redactedPlaceholder.length).toBeGreaterThan(0);
    for (const [name, value] of Object.entries(contract.logging)) {
      if (["maximumFields", "maximumKeyLength", "maximumValueLength"].includes(name)) {
        expect(Number.isInteger(value), `${name} is a bound`).toBe(true);
        expect(value as number).toBeGreaterThan(0);
      }
    }
  });

  it("answers every outcome the contract publishes with a status and a code", () => {
    for (const outcome of outcomeNames) {
      if (isSuccessOutcome(outcome)) {
        expect(contract.outcomeProblems[outcome], `problem of success ${outcome}`).toBeUndefined();
        continue;
      }
      const status = statusOfOutcome(outcome);
      const code = codeOfOutcome(outcome);
      expect(Number.isInteger(status), `status of ${outcome}`).toBe(true);
      expect(status, `status of ${outcome}`).toBeGreaterThanOrEqual(400);
      expect(status, `status of ${outcome}`).toBeLessThan(600);
      expect(code, `code of ${outcome}`).toMatch(/^[a-z][a-z0-9_]*$/);
    }
  });
});
