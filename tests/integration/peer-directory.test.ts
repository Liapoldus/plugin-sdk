import { execFileSync } from "node:child_process";
import directorySchema from "../../infrastructure/assets/plugin-sdk/v2/peer-directory.schema.json";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const issuedAt = "2026-10-06T10:00:00Z";
const expiresAt = "2026-10-06T10:00:30Z";

function replica(overrides: Record<string, unknown> = {}) {
  return {
    identity: {
      instanceId: "forms-db",
      replicaId: "forms-1",
      incarnationId: "inc-1",
      placementId: "node-a",
    },
    endpoint: "/run/liapoldus/forms.sock",
    eligibility: "ready",
    eligibleUntil: "2026-10-06T10:00:20Z",
    weight: 3,
    peerContracts: [],
    ...overrides,
  };
}

function withIdentity(overrides: Record<string, unknown>) {
  const { replicaId, incarnationId, placementId, ...rest } = overrides;
  return {
    ...rest,
    identity: {
      instanceId: "forms-db",
      replicaId: replicaId ?? "forms-1",
      incarnationId: incarnationId ?? "inc-1",
      placementId: placementId ?? "node-a",
    },
  };
}

function directory(overrides: Record<string, unknown> = {}) {
  return {
    contractVersion: "liapoldus.plugin-sdk.peer-directory.v1",
    generation: "directory-42",
    issuedAt,
    expiresAt,
    caller: {
      instanceId: "server",
      replicaId: "server-1",
      incarnationId: "server-inc-1",
      placementId: "node-a",
    },
    links: [
      {
        linkId: "forms-local",
        targetInstanceId: "forms-db",
        placementRule: "same-placement",
        carrier: "unix",
        securityProfile: "mtls",
        requiredPeerContracts: [],
        replicas: [replica()],
      },
      {
        linkId: "forms-remote",
        targetInstanceId: "forms-db",
        placementRule: "remote",
        carrier: "quic",
        securityProfile: "mtls",
        requiredPeerContracts: [],
        replicas: [replica(withIdentity({
          replicaId: "forms-2",
          incarnationId: "inc-2",
          placementId: "node-b",
          endpoint: "forms-2.internal:9443",
          weight: 1,
        }))],
      },
    ],
    ...overrides,
  };
}

function localLink(overrides: Record<string, unknown> = {}) {
  return {
    linkId: "forms-local",
    targetInstanceId: "forms-db",
    placementRule: "same-placement",
    carrier: "unix",
    securityProfile: "mtls",
    requiredPeerContracts: [],
    replicas: [replica()],
    ...overrides,
  };
}

function resolvePeer(peerDirectory: unknown, request: unknown) {
  const output = execFileSync("go", ["run", "./tests/fixtures/peer-directory"], {
    cwd: root,
    env: { ...process.env, GOWORK: "off" },
    input: JSON.stringify({ directory: peerDirectory, request }),
    encoding: "utf8",
  });
  return JSON.parse(output) as { peer: { carrier: string; replicaId: string } };
}

describe("versioned plugin peer directory", () => {
  it("publishes the exact generic DTO contract and bounded carriers", () => {
    expect(directorySchema.properties.contractVersion.const).toBe("liapoldus.plugin-sdk.peer-directory.v1");
    expect(directorySchema.required).toEqual(["contractVersion", "generation", "issuedAt", "expiresAt", "caller", "links"]);
    expect(directorySchema.$defs.link.properties.placementRule.enum).toEqual(["same-placement", "remote"]);
    expect(directorySchema.$defs.link.properties.carrier.enum).toEqual(["tcp", "quic", "unix", "windows-named-pipe"]);
    expect(directorySchema.$defs.link.properties.securityProfile.const).toBe("mtls");
    expect(directorySchema.$defs.replica.properties.weight).toMatchObject({ minimum: 1, maximum: 100 });
    expect(directorySchema.$defs.replica.required).toContain("peerContracts");
    expect(directorySchema.$defs.link.required).toContain("requiredPeerContracts");
    expect(directorySchema.properties.links).not.toHaveProperty("minItems");
  });

  it("accepts an explicit empty authorization directory and resolves no peer", () => {
    const denied = directory({ links: [] });
    const missingLinks = directory();
    delete (missingLinks as { links?: unknown }).links;

    expect(resolvePeer(denied, {
      linkId: "forms-local",
      now: "2026-10-06T10:00:05Z",
      selectionOrdinal: 0,
    })).toEqual({ error: "peer_link_not_found" });
    expect(resolvePeer(missingLinks, {
      linkId: "forms-local",
      now: "2026-10-06T10:00:05Z",
      selectionOrdinal: 0,
    })).toEqual({ error: "invalid_directory" });
  });

  it("resolves the explicitly selected carrier and placement rule without transport fallback", () => {
    const result = resolvePeer(directory(), {
      linkId: "forms-remote",
      now: "2026-10-06T10:00:05Z",
      selectionOrdinal: 0,
    });

    expect(result).toEqual({
      peer: {
        directoryGeneration: "directory-42",
        linkId: "forms-remote",
        targetInstanceId: "forms-db",
        replicaId: "forms-2",
        incarnationId: "inc-2",
        placementId: "node-b",
        carrier: "quic",
        securityProfile: "mtls",
        endpoint: "forms-2.internal:9443",
        eligibleUntil: "2026-10-06T10:00:20Z",
      },
    });
  });

  it("accepts the explicit Windows local carrier and rejects malformed remote hosts", () => {
    const windowsLocal = directory({ links: [{
      linkId: "forms-local",
      targetInstanceId: "forms-db",
      placementRule: "same-placement",
      carrier: "windows-named-pipe",
      securityProfile: "mtls",
      requiredPeerContracts: [],
      replicas: [replica({ endpoint: "\\\\.\\pipe\\forms-v1" })],
    }] });
    const invalidRemote = directory({ links: [{
      linkId: "forms-remote",
      targetInstanceId: "forms-db",
      placementRule: "remote",
      carrier: "tcp",
      securityProfile: "mtls",
      requiredPeerContracts: [],
      replicas: [replica(withIdentity({ placementId: "node-b", endpoint: "bad host:9443" }))],
    }] });

    expect(resolvePeer(windowsLocal, {
      linkId: "forms-local",
      now: "2026-10-06T10:00:05Z",
      selectionOrdinal: 0,
    }).peer.carrier).toBe("windows-named-pipe");
    expect(resolvePeer(invalidRemote, {
      linkId: "forms-remote",
      now: "2026-10-06T10:00:05Z",
      selectionOrdinal: 0,
    })).toEqual({ error: "invalid_directory" });
  });

  it("selects exact weighted sequence slots after deterministic identity sorting", () => {
    const weighted = directory({ links: [{
      linkId: "forms-local",
      targetInstanceId: "forms-db",
      placementRule: "same-placement",
      carrier: "unix",
      securityProfile: "mtls",
      requiredPeerContracts: [],
      replicas: [
        replica(withIdentity({ replicaId: "forms-b", incarnationId: "inc-b", weight: 1 })),
        replica(withIdentity({ replicaId: "forms-a", incarnationId: "inc-a", weight: 3 })),
      ],
    }] });
    const results = [0, 1, 2, 3].map((selectionOrdinal) =>
      resolvePeer(weighted, { linkId: "forms-local", now: "2026-10-06T10:00:05Z", selectionOrdinal }).peer.replicaId,
    );

    expect(results).toEqual(["forms-a", "forms-a", "forms-a", "forms-b"]);
  });

  it("routes a stable key deterministically and gives it precedence over the sequence", () => {
    const input = { linkId: "forms-local", now: "2026-10-06T10:00:05Z", stableRoutingKey: "tenant-42" };
    expect(resolvePeer(directory(), { ...input, selectionOrdinal: 0 }))
      .toEqual(resolvePeer(directory(), { ...input, selectionOrdinal: 99 }));
  });

  it("refuses a same-placement directory containing a cross-placement candidate", () => {
    const invalid = directory({ links: [{
      linkId: "forms-local",
      targetInstanceId: "forms-db",
      placementRule: "same-placement",
      carrier: "unix",
      securityProfile: "mtls",
      requiredPeerContracts: [],
      replicas: [replica(withIdentity({ placementId: "node-b" }))],
    }] });

    expect(resolvePeer(invalid, { linkId: "forms-local", now: "2026-10-06T10:00:05Z", selectionOrdinal: 0 }))
      .toEqual({ error: "invalid_directory" });
  });

  it("refuses a remote carrier candidate placed with the caller", () => {
    const invalid = directory({ links: [{
      linkId: "forms-remote",
      targetInstanceId: "forms-db",
      placementRule: "remote",
      carrier: "quic",
      securityProfile: "mtls",
      requiredPeerContracts: [],
      replicas: [replica({ endpoint: "forms.internal:9443" })],
    }] });

    expect(resolvePeer(invalid, { linkId: "forms-remote", now: "2026-10-06T10:00:05Z", selectionOrdinal: 0 }))
      .toEqual({ error: "invalid_directory" });
  });

  it("does not fall back when every candidate is ineligible or its lease has expired", () => {
    const unavailable = directory({ links: [{
      linkId: "forms-local",
      targetInstanceId: "forms-db",
      placementRule: "same-placement",
      carrier: "unix",
      securityProfile: "mtls",
      requiredPeerContracts: [],
      replicas: [
        replica({ eligibility: "draining", endpoint: "", eligibleUntil: null }),
        replica(withIdentity({ replicaId: "forms-2", incarnationId: "inc-2", eligibleUntil: "2026-10-06T10:00:04Z" })),
      ],
    }] });

    expect(resolvePeer(unavailable, { linkId: "forms-local", now: "2026-10-06T10:00:05Z", selectionOrdinal: 0 }))
      .toEqual({ error: "no_eligible_peer" });
  });

  it("requires a stable key or explicit sequence and rejects expired snapshots", () => {
    expect(resolvePeer(directory(), { linkId: "forms-local", now: "2026-10-06T10:00:05Z" }))
      .toEqual({ error: "invalid_resolution_request" });
    expect(resolvePeer(directory(), { linkId: "forms-local", now: "2026-10-06T10:00:31Z", selectionOrdinal: 0 }))
      .toEqual({ error: "peer_directory_expired" });
  });

  it("does not use a directory before issuedAt", () => {
    expect(resolvePeer(directory(), { linkId: "forms-local", now: "2026-10-06T09:59:59Z", selectionOrdinal: 0 }))
      .toEqual({ error: "peer_directory_not_yet_valid" });
  });

  it("rejects omitted required arrays and unknown DTO fields", () => {
    const missing = directory({ links: [{
      linkId: "forms-local",
      targetInstanceId: "forms-db",
      placementRule: "same-placement",
      carrier: "unix",
      securityProfile: "mtls",
      replicas: [replica()],
    }] });
    const unknown = directory({ unexpected: true });

    expect(resolvePeer(missing, { linkId: "forms-local", now: "2026-10-06T10:00:05Z", selectionOrdinal: 0 }))
      .toEqual({ error: "invalid_directory" });
    expect(resolvePeer(unknown, { linkId: "forms-local", now: "2026-10-06T10:00:05Z", selectionOrdinal: 0 }))
      .toEqual({ error: "invalid_directory" });
  });

  it("applies the declared half-open SemVer range to generic peer contracts", () => {
    const compatible = directory({ links: [{
      linkId: "forms-local",
      targetInstanceId: "forms-db",
      placementRule: "same-placement",
      carrier: "unix",
      securityProfile: "mtls",
      requiredPeerContracts: [{
        contractId: "org.liapoldus.forms-api",
        minimumVersion: "1.2.0",
        maximumVersionExclusive: "2.0.0",
      }],
      replicas: [
        replica({ peerContracts: [{
          contractId: "org.liapoldus.forms-api",
          version: "1.2.0",
          sha256: "a".repeat(64),
        }] }),
        replica(withIdentity({
          replicaId: "forms-2",
          incarnationId: "inc-2",
          peerContracts: [{
            contractId: "org.liapoldus.forms-api",
            version: "2.0.0",
            sha256: "b".repeat(64),
          }],
        })),
      ],
    }] });

    const selected = resolvePeer(compatible, {
      linkId: "forms-local",
      now: "2026-10-06T10:00:05Z",
      selectionOrdinal: 1,
    });
    expect(selected.peer.replicaId).toBe("forms-1");
  });

  it("rejects malformed or inverted peer-contract ranges", () => {
    const invalid = directory({ links: [{
      linkId: "forms-local",
      targetInstanceId: "forms-db",
      placementRule: "same-placement",
      carrier: "unix",
      securityProfile: "mtls",
      requiredPeerContracts: [{
        contractId: "org.liapoldus.forms-api",
        minimumVersion: "2.0.0",
        maximumVersionExclusive: "1.0.0",
      }],
      replicas: [replica()],
    }] });

    expect(resolvePeer(invalid, {
      linkId: "forms-local",
      now: "2026-10-06T10:00:05Z",
      selectionOrdinal: 0,
    })).toEqual({ error: "invalid_directory" });
  });

  it("refuses a directory larger than the one-megabyte byte bound before decoding", () => {
    const oversizedReplicas = Array.from({ length: 512 }, (_, index) => replica(withIdentity({
      replicaId: `forms-${String(index)}`,
      incarnationId: `inc-${String(index)}`,
      endpoint: `/${"a".repeat(2000)}`,
    })));
    const oversized = directory({ links: [localLink({ replicas: oversizedReplicas })] });

    expect(oversizedReplicas.length).toBeLessThanOrEqual(512);
    expect(JSON.stringify(oversized).length).toBeGreaterThan(1_048_576);
    expect(resolvePeer(oversized, {
      linkId: "forms-local",
      now: "2026-10-06T10:00:05Z",
      selectionOrdinal: 0,
    })).toEqual({ error: "invalid_directory" });
  });

  it("refuses a directory with more than 256 links", () => {
    const links = Array.from({ length: 257 }, (_, index) => localLink({
      linkId: `link-${String(index).padStart(3, "0")}`,
      replicas: [],
      requiredPeerContracts: [],
    }));

    expect(links.length).toBeGreaterThan(256);
    expect(resolvePeer(directory({ links }), {
      linkId: "link-000",
      now: "2026-10-06T10:00:05Z",
      selectionOrdinal: 0,
    })).toEqual({ error: "invalid_directory" });
  });

  it("refuses a link with more than 512 replicas", () => {
    const replicas = Array.from({ length: 513 }, (_, index) => replica(withIdentity({
      replicaId: `forms-${String(index)}`,
      incarnationId: `inc-${String(index)}`,
      eligibility: "draining",
      endpoint: "",
      eligibleUntil: null,
      weight: 1,
    })));

    expect(replicas.length).toBeGreaterThan(512);
    expect(resolvePeer(directory({ links: [localLink({ replicas })] }), {
      linkId: "forms-local",
      now: "2026-10-06T10:00:05Z",
      selectionOrdinal: 0,
    })).toEqual({ error: "invalid_directory" });
  });

  it("refuses more than 64 required or advertised peer contracts", () => {
    const required = Array.from({ length: 65 }, (_, index) => ({
      contractId: `org.example.c${String(index)}`,
      minimumVersion: "1.0.0",
      maximumVersionExclusive: "2.0.0",
    }));
    const advertised = Array.from({ length: 65 }, (_, index) => ({
      contractId: `org.example.c${String(index)}`,
      version: "1.0.0",
      sha256: "a".repeat(64),
    }));
    const request = { linkId: "forms-local", now: "2026-10-06T10:00:05Z", selectionOrdinal: 0 };

    expect(resolvePeer(directory({ links: [localLink({ requiredPeerContracts: required })] }), request))
      .toEqual({ error: "invalid_directory" });
    expect(resolvePeer(directory({ links: [localLink({ replicas: [replica({ peerContracts: advertised })] })] }), request))
      .toEqual({ error: "invalid_directory" });
  });

  it("refuses replica weights outside the inclusive 1..100 bounds", () => {
    const request = { linkId: "forms-local", now: "2026-10-06T10:00:05Z", selectionOrdinal: 0 };

    expect(resolvePeer(directory({ links: [localLink({ replicas: [replica({ weight: 101 })] })] }), request))
      .toEqual({ error: "invalid_directory" });
    expect(resolvePeer(directory({ links: [localLink({ replicas: [replica({ weight: 0 })] })] }), request))
      .toEqual({ error: "invalid_directory" });
  });

  it("refuses a directory lifetime above 30 seconds and accepts exactly 30", () => {
    const request = { linkId: "forms-local", now: "2026-10-06T10:00:05Z", selectionOrdinal: 0 };

    expect(resolvePeer(directory({ expiresAt: "2026-10-06T10:00:31Z" }), request))
      .toEqual({ error: "invalid_directory" });
    expect(resolvePeer(directory({ expiresAt: "2026-10-06T10:00:30Z" }), request).peer.replicaId)
      .toBe("forms-1");
  });

  it("refuses a stable routing key above 256 bytes and accepts exactly 256", () => {
    const request = { linkId: "forms-local", now: "2026-10-06T10:00:05Z" };

    expect(resolvePeer(directory(), { ...request, stableRoutingKey: "k".repeat(257) }))
      .toEqual({ error: "invalid_resolution_request" });
    expect(resolvePeer(directory(), { ...request, stableRoutingKey: "k".repeat(256) }).peer.replicaId)
      .toBe("forms-1");
  });

  it("refuses duplicate linkIds within one directory", () => {
    const duplicate = directory({ links: [
      localLink(),
      localLink({ replicas: [replica(withIdentity({ replicaId: "forms-2", incarnationId: "inc-2" }))] }),
    ] });

    expect(resolvePeer(duplicate, {
      linkId: "forms-local",
      now: "2026-10-06T10:00:05Z",
      selectionOrdinal: 0,
    })).toEqual({ error: "invalid_directory" });
  });

  it("refuses conflicting digests for one contract version", () => {
    const conflicting = directory({ links: [localLink({
      replicas: [
        replica({ peerContracts: [{ contractId: "org.liapoldus.forms-api", version: "1.2.0", sha256: "a".repeat(64) }] }),
        replica(withIdentity({
          replicaId: "forms-2",
          incarnationId: "inc-2",
          peerContracts: [{ contractId: "org.liapoldus.forms-api", version: "1.2.0", sha256: "b".repeat(64) }],
        })),
      ],
    })] });

    expect(resolvePeer(conflicting, {
      linkId: "forms-local",
      now: "2026-10-06T10:00:05Z",
      selectionOrdinal: 0,
    })).toEqual({ error: "invalid_directory" });
  });
});
