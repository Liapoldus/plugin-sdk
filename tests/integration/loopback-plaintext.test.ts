import { required } from "../support/value";
import { execFile, spawn, type ChildProcessByStdio } from "node:child_process";
import type { Readable } from "node:stream";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { createInterface } from "node:readline";
import { join } from "node:path";
import { promisify } from "node:util";
import { afterAll, describe, expect, it } from "vitest";

import { contractFile, projectRoot } from "../support/contract";

const execute = promisify(execFile);
let buildDirectory: string | undefined;
let binary: string | undefined;

afterAll(async () => {
  if (buildDirectory) await rm(buildDirectory, { recursive: true, force: true });
});

async function fixtureBinary(): Promise<string> {
  if (binary) return binary;
  buildDirectory = await mkdtemp(join(tmpdir(), "plugin-sdk-loopback-health-"));
  binary = join(buildDirectory, "loopback-health");
  await execute("go", ["build", "-o", binary, "./tests/fixtures/loopback-health"], {
    cwd: projectRoot,
  });
  return binary;
}

type StartedFixture = {
  child: ChildProcessByStdio<null, Readable, Readable>;
  info: { enabled: boolean; healthURL?: string };
  stop: () => Promise<void>;
};

async function startFixture(mode: string): Promise<StartedFixture> {
  const child = spawn(await fixtureBinary(), [mode], {
    cwd: projectRoot,
    stdio: ["ignore", "pipe", "pipe"],
  });
  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");
  const info = await new Promise<{ enabled: boolean; healthURL?: string }>((resolve, reject) => {
    const lines = createInterface({ input: child.stdout });
    const timeout = setTimeout(() => { reject(new Error("fixture startup timed out")); }, 30_000);
    lines.once("line", (line) => {
      clearTimeout(timeout);
      lines.close();
      try {
        resolve(JSON.parse(line) as { enabled: boolean; healthURL?: string });
      } catch (error) {
        reject(error instanceof Error ? error : new Error(String(error)));
      }
    });
    child.once("exit", (code) => {
      clearTimeout(timeout);
      reject(new Error(`fixture exited during startup (${String(code)})`));
    });
    child.once("error", reject);
  });
  return {
    child,
    info,
    stop: async () => {
      if (child.exitCode !== null) return;
      child.kill("SIGTERM");
      await new Promise<void>((resolve) => child.once("exit", () => { resolve(); }));
    },
  };
}

async function request(url: string, path: string, method = "GET") {
  const response = await fetch(new URL(path, url), { method });
  return { status: response.status, text: await response.text() };
}

describe("the opt-in loopback plaintext health profile", () => {
  it("is disabled by default and leaves the v1 mTLS contract unchanged", async () => {
    const contract = JSON.parse(await readFile(contractFile, "utf8")) as {
      contractVersion: string;
      transportSecurity: { clientCertificateRequired: boolean };
      plugin: { endpoints: Record<string, { method: string; path: string }> };
    };
    const profile = JSON.parse(
      await readFile(join(projectRoot, "infrastructure/assets/plugin-sdk/v2/loopback-plaintext-profile.json"), "utf8"),
    ) as {
      enabledByDefault: boolean;
      exposedEndpoint: { httpContractVersion: string; endpointKey: string };
      excludedEndpointKeys: string[];
      coreEndpointsAlwaysRequireMutualTLS: string[];
      transport: { remoteBindAllowed: boolean; tlsFailureFallback: boolean };
    };

    expect(contract.contractVersion).toBe("liapoldus.plugin-sdk.http.v2");
    expect(contract.transportSecurity.clientCertificateRequired).toBe(true);
    expect(profile.enabledByDefault).toBe(false);
    expect(profile.exposedEndpoint).toEqual({
      httpContractVersion: contract.contractVersion,
      endpointKey: "health",
    });
    expect(profile.excludedEndpointKeys.toSorted()).toEqual(
      Object.keys(contract.plugin.endpoints).filter((name) => name !== "health").toSorted(),
    );
    expect(profile.coreEndpointsAlwaysRequireMutualTLS.toSorted()).toEqual(
      ["configPull", "secretGrant.issue", "secretGrant.redemption"].toSorted(),
    );
    expect(profile.transport).toMatchObject({ remoteBindAllowed: false, tlsFailureFallback: false });

    const fixture = await startFixture("default");
    try {
      expect(fixture.info).toEqual({ enabled: false });
    } finally {
      await fixture.stop();
    }
  });

  it("serves only the non-sensitive health response on the selected loopback profile", async () => {
    const fixture = await startFixture("enabled");
    try {
      expect(fixture.info.enabled).toBe(true);
      expect(fixture.info.healthURL).toMatch(/^http:\/\/127\.0\.0\.1:/);
      const good = await request(required(fixture.info.healthURL), "/_liapoldus/v1/health");
      expect(good.status).toBe(200);
      expect(JSON.parse(good.text)).toEqual({ status: "ok" });

      for (const [path, method] of [
        ["/_liapoldus/v1/ready", "GET"],
        ["/_liapoldus/v1/identity", "GET"],
        ["/_liapoldus/v1/manifest", "GET"],
        ["/_liapoldus/v1/config-schema", "GET"],
        ["/_liapoldus/v1/reload", "POST"],
        ["/_liapoldus/v1/admin-surface", "GET"],
        ["/_liapoldus/v1/admin-action/forms/delete", "POST"],
        ["/_liapoldus/v1/artifact-stream", "POST"],
        ["/_liapoldus/v1/metrics", "GET"],
        ["/internal/v1/plugin-config/generation-active", "GET"],
        ["/internal/v1/plugin-secret-grants", "POST"],
        ["/internal/v1/plugin-secret-grants/handle/redemption", "POST"],
      ] as const) {
        const answer = await request(required(fixture.info.healthURL), path, method);
        expect(answer.status, `${method} ${path} must not be exposed in plaintext`).toBe(404);
        expect(answer.text).not.toMatch(/generation|replica|secret|grant|manifest|schema/i);
      }
    } finally {
      await fixture.stop();
    }
  });

  it("rejects non-loopback binds instead of exposing the plaintext profile remotely", async () => {
    const child = spawn(await fixtureBinary(), ["remote"], {
      cwd: projectRoot,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stderr = "";
    child.stderr.setEncoding("utf8");
    child.stderr.on("data", (chunk: string) => (stderr += chunk));
    const code = await new Promise<number | null>((resolve, reject) => {
      child.once("error", reject);
      child.once("exit", resolve);
    });
    expect(code).not.toBe(0);
    expect(stderr).toContain("loopback bind rejected");
    expect(stderr).not.toContain("0.0.0.0");
  });
});
