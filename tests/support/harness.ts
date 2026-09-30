export const fixture = {
  instanceId: "fixture-instance",
  replicaId: "fixture-replica-1",
  schemaVersion: "fixture/v1",
  otherSchemaVersion: "other/v1",
  generations: {
    active: "generation-active",
    duplicateKeys: "generation-duplicate-keys",
    applyFailure: "generation-apply-failure",
    corrupt: "generation-corrupt",
    dropped: "generation-dropped",
    recovered: "generation-recovered",
  },
  document: {
    wellFormed: '{"replica":"fixture","enabled":true,"items":[1,2,3]}\n',
    corrupt: '{"replica":"fixture","enabled":false}\n',
  },
  states: {
    active: "active",
    previous: "previous",
  },
  pullFaults: {
    none: "",
    unknown: "unknownGeneration",
    stale: "staleGeneration",
    denied: "notPermitted",
    cancelled: "cancelled",
    expired: "deadlineExceeded",
    unavailable: "coreUnavailable",
  },
  secrets: {
    reference: "fixture-secret-reference",
    purpose: "fixture-purpose",
    handlePrefix: "test-grant-",
    valuePrefix: "value-for-",
    operations: {
      issue: "issue",
      provide: "provide",
      redeemTwice: "redeemTwice",
    },
    legs: {
      issue: "issue",
      redemption: "redemption",
    },
    faults: {
      none: "",
      denied: "grantDenied",
      unknown: "grantUnknown",
      expired: "grantExpired",
      spent: "grantSpent",
      notPermitted: "notPermitted",
    },
  },
  documents: {
    manifest: "manifest",
    configurationSchema: "configurationSchema",
    metrics: "metrics",
  },
  identities: {
    coreReplica: "liapoldus-core-replica",
    pluginReplica: "liapoldus-plugin-replica",
  },
  // The credentials the rotation surface is served with, before and after the
  // operator writes new material to disk. They are fixture serials, not contract
  // values, and a harness uses them to see which certificate the listener
  // actually presented.
  credentials: {
    serverName: "localhost",
    rotationBeforeSerial: "9",
    rotationAfterSerial: "A",
  },
  // The document the load surface serves while a drain is in progress, and the
  // bound inside which the drain has to finish.
  load: {
    document: '{"operation":"load","status":"drained"}\n',
    boundedMilliseconds: 5_000,
  },
  control: {
    reload: "/control/reload",
    publish: "/control/publish",
    fault: "/control/fault",
    secretFault: "/control/secret-fault",
    secret: "/control/secret",
    state: "/control/state",
    document: "/control/document",
    drops: "/control/drops",
    connections: "/control/connections",
    rotation: "/control/rotation",
    load: "/control/load",
  },
  keys: {
    generation: "generation",
    sha256: "sha256",
    schemaVersion: "schemaVersion",
    outcome: "outcome",
    applied: "applied",
    code: "code",
    ready: "ready",
    pendingGeneration: "pendingGeneration",
    appliedGeneration: "appliedGeneration",
    instanceId: "instanceId",
    replicaId: "replicaId",
  },
};
