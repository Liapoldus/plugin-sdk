import { execFile } from "node:child_process";
import { join } from "node:path";
import { promisify } from "node:util";
import { describe, expect, it } from "vitest";

const execFileAsync = promisify(execFile);
const root = join(import.meta.dirname, "../..");

describe("Core-to-plugin response body lifetime", () => {
  it("keeps the request context alive until the schema body is consumed", async () => {
    const { stdout } = await execFileAsync("go", ["run", "./tests/fixtures/body-lifetime"], { cwd: root });
    expect(JSON.parse(stdout)).toEqual({ schema: '{"type":"object"}' });
  }, 60_000);
});
