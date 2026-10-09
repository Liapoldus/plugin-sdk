import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { fixture } from "../support/harness";
import { Runtime } from "../support/runtime";

let runtime: Runtime;
let active: { generation: string; sha256: string; schemaVersion: string };

const dropCount = 5;

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

describe("a request dropped after the listener has accepted it", () => {
  it("reports no lifecycle result and applies nothing, and the surface keeps serving", async () => {
    // The surface has to be carrying a known generation before the drops are
    // measured, otherwise "no dropped request applied a generation" is a claim
    // about a plugin that had nothing applied to begin with. The generation is
    // published and reloaded through the ordinary path first, so the assertion
    // below is a comparison between two real applied states.
    const seeded = await runtime.publish({
      generation: active.generation,
      rawJSON: fixture.document.wellFormed,
    });

    expect(seeded.status).toBe(200);

    const accepted = await runtime.reloadViaCore(active);

    expect(accepted.status).toBe(200);
    expect(accepted.body.applied).toBe(true);

    const opened = await runtime.state();

    expect(opened.status).toBe(200);
    expect(opened.body.activeGeneration).toBe(runtime.info.generation);

    const armed = await runtime.armConnectionDrops(dropCount);

    expect(armed.status).toBe(200);
    expect(armed.body.armed).toBe(dropCount);
    expect(armed.body.dropped).toBe(0);

    for (let attempt = 0; attempt < dropCount; attempt += 1) {
      const published = await runtime.publish({
        generation: fixture.generations.dropped,
        rawJSON: fixture.document.wellFormed,
      });
      expect(published.status).toBe(200);

      const answer = await runtime.reloadViaCore({
        ...active,
        generation: fixture.generations.dropped,
      });

      expect(answer.status, `drop ${String(attempt)} must not be acknowledged`).toBeGreaterThanOrEqual(400);
      expect(answer.body.applied, "a dropped request applies nothing").toBe(false);
      expect(
        answer.body.outcome,
        "a dropped request publishes no lifecycle outcome, because no peer ever answered it",
      ).toBe("");
      expect(answer.body.generation).not.toBe(fixture.generations.dropped);
    }

    const disarmed = await runtime.armConnectionDrops(0);

    expect(disarmed.status).toBe(200);
    expect(
      disarmed.body.dropped,
      "every requested drop has to land, or the rest of this test proves nothing",
    ).toBe(dropCount);
    expect(disarmed.body.remaining).toBe(0);

    const midway = await runtime.state();

    expect(midway.status).toBe(200);
    expect(
      midway.body.activeGeneration,
      "no dropped request applied a generation",
    ).toBe(runtime.info.generation);
    expect(midway.body.applyApplied).toBe(opened.body.applyApplied);
  });

  it("serves a later generation on the same surface once the drops stop", async () => {
    const published = await runtime.publish({
      generation: fixture.generations.recovered,
      rawJSON: fixture.document.wellFormed,
    });

    expect(published.status).toBe(200);

    const answer = await runtime.reloadViaCore({
      ...active,
      generation: fixture.generations.recovered,
    });

    expect(answer.status).toBe(200);
    expect(answer.body.applied).toBe(true);
    expect(answer.body.outcome).toBe("applied");
    expect(answer.body.generation).toBe(fixture.generations.recovered);

    const final = await runtime.state();

    expect(final.status).toBe(200);
    expect(final.body.activeGeneration).toBe(fixture.generations.recovered);
  });

  it("leaves no connection open behind it", async () => {
    const connections = await runtime.connections();

    expect(connections.status).toBe(200);
    expect(
      connections.body.opened,
      "the surface has to have served the dropped calls and the recovered one",
    ).toBeGreaterThanOrEqual(dropCount + 1);
    // A hijacked connection is a connection the server handed to somebody else
    // through an upgrade, which is not what a dropped request produces: the
    // server closes the connection and forgets it. The invariant that matters is
    // therefore that the drops reached the listener at all and that none of the
    // connections they opened is still counted as open, so the count of opened
    // connections is the evidence and the count of open ones is the verdict.
    expect(
      connections.body.open,
      "a dropped connection must not be left counted as open",
    ).toBe(0);
  });
});
