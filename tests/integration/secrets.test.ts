import { afterAll, beforeAll, describe, expect, it } from "vitest";

import {
  codeOfOutcome,
  contract,
  isSuccessOutcome,
  outcomesOf,
  statusOfOutcome,
} from "../support/contract";
import { fixture } from "../support/harness";
import { Runtime, type SecretResult } from "../support/runtime";

const granted = "granted";
const succeeded = "succeeded";
const spent = "grantSpent";
const failed = "failed";

// The SDK bounds the redemption handles it tracks per replica, so a replica that
// claims more than the bound refuses the next redemption as an opaque control
// failure rather than replaying an unknown outcome. Each block below therefore
// owns its own replica, and each stays inside one replica's budget.
function request(operation: string): { operation: string; reference: string; purpose: string } {
  return {
    operation,
    reference: fixture.secrets.reference,
    purpose: fixture.secrets.purpose,
  };
}

async function arm(runtime: Runtime, leg: string, mode: string): Promise<void> {
  const armed = await runtime.armSecretFault(leg, mode);
  expect(armed.status, `arm ${leg}/${mode}`).toBe(200);
}

async function clear(runtime: Runtime): Promise<void> {
  await arm(runtime, fixture.secrets.legs.issue, fixture.secrets.faults.none);
  await arm(runtime, fixture.secrets.legs.redemption, fixture.secrets.faults.none);
}

function active(runtime: Runtime): {
  generation: string;
  sha256: string;
  schemaVersion: string;
} {
  return {
    generation: runtime.info.generation,
    sha256: runtime.info.digest,
    schemaVersion: fixture.schemaVersion,
  };
}

describe("a secret grant a replica is not yet ready to receive", () => {
  let runtime: Runtime;

  beforeAll(async () => {
    runtime = await Runtime.start();
  }, 120_000);

  afterAll(async () => {
    await runtime.stop();
  });

  it("refuses a grant before Core has reloaded the replica", async () => {
    expect((await runtime.state()).body.readiness[fixture.keys.ready]).toBe(false);

    const refused = (
      await runtime.secret(request(fixture.secrets.operations.issue))
    ).body;

    expect(refused.outcome).toBe("notReady");
    expect(refused.failure).toBe("notReady");
    expect(isSuccessOutcome(String(refused.outcome))).toBe(false);
    expect(refused.bytes).toBeUndefined();
    expect(refused.valueSHA256).toBeUndefined();
  });
});

describe("a secret grant issued to a ready replica", () => {
  let runtime: Runtime;

  beforeAll(async () => {
    runtime = await Runtime.start();
    const applied = await runtime.reloadViaCore(active(runtime));
    expect(applied.status).toBe(200);
  }, 120_000);

  afterAll(async () => {
    await runtime.stop();
  });

  async function issue(): Promise<SecretResult> {
    return (await runtime.secret(request(fixture.secrets.operations.issue))).body;
  }

  it("lets a configuration applier obtain a grant scoped to the candidate generation", async () => {
    const state = await runtime.state();
    expect(state.status).toBe(200);
    expect(state.body.candidateSecretGeneration).toBe(runtime.info.generation);
  });

  it("grants the reference for the generation the replica actually applied", async () => {
    const answer = await issue();

    expect(answer.outcome).toBe(granted);
    expect(answer.failure).toBe("");
    expect(isSuccessOutcome(String(answer.outcome))).toBe(true);
    expect(outcomesOf("secretRedemption")).toContain(granted);
  });

  it("returns the secret bytes to the plugin as a count and a digest, never as a value", async () => {
    const answer = (await runtime.secret(request(fixture.secrets.operations.provide))).body;

    expect(answer.failure).toBe("");
    expect(answer.bytes).toBeGreaterThan(0);
    expect(answer.valueSHA256).toMatch(/^[0-9a-f]{64}$/);
  });

  it("redeems a grant exactly once and reports the second attempt as spent", async () => {
    const answer = (await runtime.secret(request(fixture.secrets.operations.redeemTwice))).body;

    expect(answer.first).toBe(succeeded);
    expect(answer.second).toBe(spent);
    expect(answer.failure).toBe("invalidSecretGrant");
    expect(statusOfOutcome(String(answer.second))).toBeGreaterThanOrEqual(400);
    expect(codeOfOutcome(String(answer.second))).toBe(contract.errors.grantSpent.code);
  });

  it("never puts a handle, a value, a reference or a purpose in any answer", async () => {
    const answers = [
      await issue(),
      (await runtime.secret(request(fixture.secrets.operations.provide))).body,
      (await runtime.secret(request(fixture.secrets.operations.redeemTwice))).body,
    ];

    for (const answer of answers) {
      const text = JSON.stringify(answer);
      expect(text, "a handle").not.toContain(fixture.secrets.handlePrefix);
      expect(text, "a value").not.toContain(fixture.secrets.valuePrefix);
      expect(text, "a reference").not.toContain(fixture.secrets.reference);
      expect(text, "a purpose").not.toContain(fixture.secrets.purpose);
    }
  });

  it("refuses a secret operation that names no reference or no purpose", async () => {
    for (const body of [
      { ...request(fixture.secrets.operations.issue), purpose: "" },
      { ...request(fixture.secrets.operations.issue), reference: "" },
    ]) {
      const refused = await runtime.secret(body);
      expect(refused.status, JSON.stringify(body)).toBeGreaterThanOrEqual(400);
    }
  });

  it("refuses a secret operation it does not implement", async () => {
    const refused = await runtime.secret({ ...request("rotate"), reference: "x", purpose: "y" });

    expect(refused.status).toBeGreaterThanOrEqual(400);
    expect(refused.text).not.toContain(fixture.secrets.handlePrefix);
  });

  it("bounds the reference, the purpose and the handle the contract declares", () => {
    const grant = contract.core.secretGrant;

    expect(grant.redemptionUseLimit).toBe(1);
    expect(grant.referenceMaximumLength).toBeGreaterThan(0);
    expect(grant.purposeMaximumLength).toBeGreaterThan(0);
    expect(grant.handleMaximumLength).toBeGreaterThan(0);
    expect(grant.issue.required).toEqual(
      expect.arrayContaining(["reference", "purpose", "generation"]),
    );
    expect(grant.redemption.required).toEqual(expect.arrayContaining(["handle"]));
  });
});

describe("a secret grant Core refuses to answer", () => {
  const refusals: { leg: string; mode: string; issue: string; first: string }[] = [
    { leg: "issue", mode: "grantDenied", issue: "grantDenied", first: "" },
    { leg: "issue", mode: "notPermitted", issue: "notPermitted", first: "" },
    { leg: "issue", mode: "grantUnknown", issue: "coreUnavailable", first: "" },
    { leg: "redemption", mode: "grantUnknown", issue: granted, first: "grantUnknown" },
    { leg: "redemption", mode: "grantDenied", issue: granted, first: "grantDenied" },
    { leg: "redemption", mode: "grantExpired", issue: granted, first: "grantExpired" },
    { leg: "redemption", mode: "grantSpent", issue: granted, first: spent },
    { leg: "redemption", mode: "notPermitted", issue: granted, first: "notPermitted" },
  ];

  let runtime: Runtime;

  beforeAll(async () => {
    runtime = await Runtime.start();
    const applied = await runtime.reloadViaCore(active(runtime));
    expect(applied.status).toBe(200);
  }, 120_000);

  afterAll(async () => {
    await runtime.stop();
  });

  for (const refusal of refusals) {
    it(`reports ${refusal.mode} on the ${refusal.leg} leg as a contract outcome`, async () => {
      await clear(runtime);
      await arm(runtime, refusal.leg, refusal.mode);

      if (refusal.leg === fixture.secrets.legs.issue) {
        const refused = (
          await runtime.secret(request(fixture.secrets.operations.issue))
        ).body;

        expect(refused.outcome, "the issued grant").toBe(refusal.issue);
        expect(refused.failure, "a refused issue is reported once").toBe(failed);
        expect(isSuccessOutcome(refusal.issue), refusal.issue).toBe(false);
        expect(outcomesOf("secretRedemption")).toContain(refusal.issue);
        return;
      }

      const redeemed = (
        await runtime.secret(request(fixture.secrets.operations.redeemTwice))
      ).body;

      expect(redeemed.first, "the first redemption").toBe(refusal.first);
      expect(redeemed.second, "the replay is refused locally").toBe(spent);
      expect(redeemed.failure, "a spent grant is reported once").toBe("invalidSecretGrant");
      expect(outcomesOf("secretRedemption")).toContain(refusal.first);
      expect(isSuccessOutcome(refusal.first), refusal.first).toBe(false);
    });
  }

  it("restores the ordinary path once the fault is cleared", async () => {
    await clear(runtime);

    const answer = (
      await runtime.secret(request(fixture.secrets.operations.issue))
    ).body;

    expect(answer.outcome).toBe(granted);
  });
});
