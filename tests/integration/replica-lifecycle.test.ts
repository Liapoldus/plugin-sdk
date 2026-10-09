import { execFileSync } from "node:child_process";
import contract from "../../infrastructure/assets/plugin-sdk/v2/replica-lifecycle.json";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { beforeAll, describe, expect, it } from "vitest";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
type LifecycleResult = {
  registered: { identity: { incarnationId: string }; leaseExpiresAt: string };
  renewed: { leaseExpiresAt: string };
  reconnected: { identity: { incarnationId: string }; leaseExpiresAt: string };
  expiredRenewal: unknown;
  spoofedCertificate: unknown;
  spoofedBody: unknown;
  mutatedEndpoint: unknown;
  immutablePlacement: unknown;
  invalidRelease: unknown;
  invalidCompatibility: unknown;
  releaseCohortCompatibility: unknown;
  deregistered: unknown;
};

function runLifecycle() {
  const output = execFileSync("go", ["run", "./tests/fixtures/replica-lifecycle"], {
    cwd: root,
    env: { ...process.env, GOWORK: "off" },
    encoding: "utf8",
  });
  return JSON.parse(output) as LifecycleResult;
}

describe("versioned replica registration and lease lifecycle", () => {
  let lifecycle: ReturnType<typeof runLifecycle>;

  beforeAll(() => {
    lifecycle = runLifecycle();
  }, 30000);

  it("publishes the generic mTLS registration contract and lifecycle bounds", () => {
    expect(contract.contractVersion).toBe("liapoldus.plugin-sdk.replica-lifecycle.v2");
    expect(contract.lease).toEqual({ ttlSeconds: 30, renewIntervalSeconds: 10 });
    expect(contract.registrationSemantics).toMatchObject({
      activeDuplicate: "same-identity-and-immutable-metadata-renews-lease",
      changedImmutableMetadata: "reject-with-identity-conflict",
      revokedIncarnationCanRenew: false,
    });
    expect(contract.registrationSemantics.immutableFields).toContain("identity");
    expect(contract.registrationSemantics.renewableFields).toEqual(["appliedGeneration", "ready"]);
    expect(contract.releaseCohortCompatibility).toEqual({
      sameReleaseDigest: "compatible",
      differentRelease: "mutual-acceptance-of-all-advertised-contract-versions",
      missingEvidence: "incompatible",
      versionRange: "semver-half-open",
    });
    expect(contract.identity.uriTemplate).toContain("{instanceId}/{replicaId}/{incarnationId}");
    expect(contract.endpoints.register.method).toBe("POST");
    expect(contract.endpoints.renew.method).toBe("POST");
    expect(contract.endpoints.deregister.method).toBe("POST");
    expect(contract.requests.register.required).toEqual([
      "contractVersion", "identity", "restEndpoint", "peerEndpoints",
      "release", "advertisedContracts", "acceptedContracts", "appliedGeneration", "ready",
    ]);
  });

  it("registers, renews, fences an expired lease, and reconnects under a new incarnation", () => {
    const result = lifecycle;

    expect(result.registered.identity.incarnationId).toBe("incarnation-a");
    expect(result.registered.leaseExpiresAt).toBe("2026-10-06T10:00:30Z");
    expect(result.renewed.leaseExpiresAt).toBe("2026-10-06T10:00:40Z");
    expect(result.expiredRenewal).toEqual({ error: "lease_expired" });
    expect(result.reconnected.identity.incarnationId).toBe("incarnation-b");
    expect(result.reconnected.leaseExpiresAt).toBe("2026-10-06T10:01:10Z");
  });

  it("binds certificate URI to replica identity and rejects spoofed or mutated registration facts", () => {
    const result = lifecycle;

    expect(result.spoofedCertificate).toEqual({ error: "invalid_client_identity" });
    expect(result.spoofedBody).toEqual({ error: "identity_mismatch" });
    expect(result.mutatedEndpoint).toEqual({ error: "identity_conflict" });
    expect(result.immutablePlacement).toEqual({ error: "identity_mismatch" });
  });

  it("validates immutable registration metadata, release SemVer, digests, and compatibility ranges", () => {
    const result = lifecycle;

    expect(result.invalidRelease).toEqual({ error: "invalid_registration" });
    expect(result.invalidCompatibility).toEqual({ error: "invalid_registration" });
    expect(result.releaseCohortCompatibility).toEqual({
      sameDigest: true,
      mutuallyAccepted: true,
      oneWayAcceptance: false,
      missingEvidence: false,
    });
    expect(result.deregistered).toMatchObject({ deregistered: true });
  });

  it("polls a caller-bound directory, observes generation changes, returns unchanged and cancels cleanly", () => {
    expect(lifecycle).toMatchObject({
      peerDirectoryInitial: true,
      peerDirectoryChanged: true,
      peerDirectoryUnchanged: true,
      peerDirectoryCallerMismatch: true,
      peerDirectoryCancelled: true,
    });
  });

  it("aborts cancelled lifecycle calls without replaying them and refuses unknown or repeated deregistration", () => {
    expect(lifecycle).toMatchObject({
      cancelledRegister: { error: "lifecycle_unavailable" },
      cancelledRenew: { error: "lifecycle_unavailable" },
      cancelledDeregister: { error: "lifecycle_unavailable" },
      cancelledCallsNotReplayed: { error: "replica_not_registered" },
      registeredAfterCancellation: {
        identity: { replicaId: "forms-2", incarnationId: "incarnation-c" },
        leaseExpiresAt: "2026-10-06T10:01:10Z",
      },
      unknownDeregistration: { error: "replica_not_registered" },
      deregisteredClientC: { deregistered: true },
      repeatedDeregistration: { error: "replica_not_registered" },
    });
  });

  it("refuses over-limit contract lists and peer endpoint registrations locally against the published bounds", () => {
    expect(lifecycle).toMatchObject({
      overLimitAdvertised: { error: "invalid_registration" },
      overLimitAccepted: { error: "invalid_registration" },
      maximumPeerEndpoints: 4,
      maximumContractsPerList: 64,
      overLimitPeerEndpoints: { error: "invalid_registration" },
    });
  });
});
