import { execFile, spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { createInterface } from "node:readline";
import { join } from "node:path";
import { promisify } from "node:util";
import { afterAll } from "vitest";

import { projectRoot } from "./contract";
import { fixture } from "./harness";

const execute = promisify(execFile);

const startupTimeoutMilliseconds = 90_000;
const stopTimeoutMilliseconds = 15_000;

export type ReadyLine = {
  pluginURL: string;
  controlURL: string;
  coreURL: string;
  pluginHTTPSURL: string;
  generation: string;
  digest: string;
  duplicateDigest: string;
  expectedRawJSON: string;
  mTLSRejectsAnonymous: string;
  mTLSRejectsRevoked: string;
  mTLSRejectsWrongIdentity: string;
  mTLSRejectsExpired: string;
  rejectsUnsafeControlURLs: string;
  // The fixture's own throwaway authority and the plugin client keypair. They are
  // published so a harness can verify a fixture-only mutual-TLS surface and
  // authenticate to it from outside this process. The material is generated in
  // memory for a single run and means nothing outside it.
  caBundlePEM: string;
  corePlaneClientCertificatePEM: string;
  corePlaneClientKeyPEM: string;
};

export type ReloadRequest = {
  generation: string;
  sha256: string;
  schemaVersion: string;
};

export type ReloadAcknowledgement = {
  generation: string;
  sha256: string;
  schemaVersion: string;
  applied: boolean;
  outcome: string;
};

export type PublishRequest = {
  generation: string;
  schemaVersion?: string;
  rawJSON: string;
  state?: string;
  announcedDigest?: string;
  announcedSchemaVersion?: string;
  malformed?: boolean;
  oversized?: boolean;
};

export type State = {
  operation: string;
  readiness: Record<string, unknown>;
  readinessFailure: string;
  identity: Record<string, unknown>;
  identityFailure: string;
  applyApplied: number;
  applyRefused: number;
  activeGeneration: string;
  activeDigest: string;
  candidateSecretGeneration: string;
};

export type SecretResult = {
  operation: string;
  outcome?: string;
  failure?: string;
  bytes?: number;
  valueSHA256?: string;
  first?: string;
  second?: string;
};

export type ControlAnswer<T> = { status: number; text: string; body: T };

// The answer to an arming call on the connection-drop switch. A count of zero
// disarms the switch and reports how many drops actually landed, so a test can
// tell an injection that happened from one it merely asked for.
export type DropsResult = {
  operation: string;
  armed: number;
  remaining: number;
  dropped: number;
};

// The connection state of the plugin's mutual-TLS listener. An injected drop
// removes its connection from the server's bookkeeping, so a connection that is
// still counted as open after a drop is a leak.
export type ConnectionsResult = {
  operation: string;
  opened: number;
  open: number;
  hijacked: number;
};

// The credential-rotation surface. Serials are reported from both the material
// the provider holds and the configuration the listener is serving, and the
// answer carries no key material.
export type RotationResult = {
  operation: string;
  url?: string;
  providerSerialBefore?: string;
  providerSerialAfter?: string;
  servedSerialBefore?: string;
  servedSerialAfter?: string;
  closed?: boolean;
  removed?: boolean;
  failure?: string;
};

// The load surface and the drain it is put through. The response is read by a
// production mutual-TLS client while the server drains, so the answer reports
// what that client actually received.
export type LoadResult = {
  operation: string;
  url?: string;
  inFlight?: number;
  served?: number;
  elapsedMilliseconds?: number;
  afterShutdown?: string;
  bodyBytes?: number;
  bodySHA256?: string;
  status?: number;
  failure?: string;
};

export type Answer = {
  status: number;
  mediaType: string;
  allow: string | null;
  text: string;
};

let buildRoot: Promise<string> | undefined;
const binaries = new Map<string, Promise<string>>();

afterAll(async () => {
  const root = buildRoot;
  if (root) {
    await rm(await root, { recursive: true, force: true });
  }
});

function fixtureBinary(name: string): Promise<string> {
  const existing = binaries.get(name);
  if (existing) {
    return existing;
  }
  buildRoot ??= mkdtemp(join(tmpdir(), "plugin-sdk-fixtures-"));
  const building = buildRoot.then((root) => {
    const target = join(root, name);
    return execute("go", ["build", "-o", target, `./tests/fixtures/${name}`], {
      cwd: projectRoot,
    }).then(() => target);
  });
  binaries.set(name, building);
  return building;
}

function announce(name: string, diagnostics: string) {
  return (error: unknown) =>
    new Error(
      `the ${name} fixture could not start: ${
        error instanceof Error ? error.message : String(error)
      }\n${diagnostics}`,
    );
}

export class Runtime {
  readonly info: ReadyLine;

  readonly #child: ChildProcessWithoutNullStreams;
  readonly #diagnostics: string[];
  #stopped = false;

  private constructor(
    child: ChildProcessWithoutNullStreams,
    info: ReadyLine,
    diagnostics: string[],
  ) {
    this.#child = child;
    this.info = info;
    this.#diagnostics = diagnostics;
  }

  static async start(name = "reload-runtime"): Promise<Runtime> {
    const executable = await fixtureBinary(name);
    const child = spawn(executable, [], {
      cwd: projectRoot,
      stdio: ["ignore", "pipe", "pipe"],
    }) as ChildProcessWithoutNullStreams;
    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    const diagnostics: string[] = [];
    child.stderr.on("data", (chunk: string) => {
      diagnostics.push(chunk);
    });
    const info = await new Promise<ReadyLine>((resolve, reject) => {
      const reported = announce(name, diagnostics.join(""));
      const lines = createInterface({ input: child.stdout });
      const settle = (finish: () => void) => {
        clearTimeout(timer);
        lines.close();
        child.off("exit", onExit);
        finish();
      };
      const onExit = (code: number | null) => {
        settle(() => reject(reported(new Error(`it exited with code ${code}`))));
      };
      const timer = setTimeout(() => {
        settle(() =>
          reject(
            reported(
              new Error(
                `it did not announce readiness within ${startupTimeoutMilliseconds}ms`,
              ),
            ),
          ),
        );
      }, startupTimeoutMilliseconds);
      lines.once("line", (line: string) => {
        try {
          const parsed = JSON.parse(line) as ReadyLine;
          settle(() => resolve(parsed));
        } catch (error) {
          settle(() => reject(reported(error)));
        }
      });
      child.once("exit", onExit);
    });
    return new Runtime(child, info, diagnostics);
  }

  get stderr(): string {
    return this.#diagnostics.join("");
  }

  get pluginURL(): string {
    return this.info.pluginURL;
  }

  get pluginHTTPSURL(): string {
    return this.info.pluginHTTPSURL;
  }

  async stop(): Promise<void> {
    if (this.#stopped) {
      return;
    }
    this.#stopped = true;
    const exited = new Promise<void>((resolve) => {
      this.#child.once("exit", () => resolve());
    });
    this.#child.kill("SIGTERM");
    const forced = setTimeout(() => this.#child.kill("SIGKILL"), stopTimeoutMilliseconds);
    try {
      await exited;
    } finally {
      clearTimeout(forced);
    }
  }

  async control<T>(route: string, request: unknown): Promise<ControlAnswer<T>> {
    const response = await fetch(new URL(route, this.info.controlURL), {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(request ?? {}),
    });
    const text = await response.text();
    return {
      status: response.status,
      text,
      body: (text.length > 0 ? JSON.parse(text) : null) as T,
    };
  }

  reloadViaCore(request: ReloadRequest): Promise<ControlAnswer<ReloadAcknowledgement>> {
    return this.control<ReloadAcknowledgement>(fixture.control.reload, request);
  }

  publish(request: PublishRequest): Promise<ControlAnswer<{ digest: string; state: string }>> {
    return this.control(fixture.control.publish, request);
  }

  armPullFault(generation: string, mode: string): Promise<ControlAnswer<{ mode: string }>> {
    return this.control(fixture.control.fault, { generation, mode });
  }

  armSecretFault(leg: string, mode: string): Promise<ControlAnswer<{ mode: string }>> {
    return this.control(fixture.control.secretFault, { leg, mode });
  }

  secret(request: {
    operation: string;
    reference: string;
    purpose: string;
  }): Promise<ControlAnswer<SecretResult>> {
    return this.control<SecretResult>(fixture.control.secret, request);
  }

  state(): Promise<ControlAnswer<State>> {
    return this.control<State>(fixture.control.state, {});
  }

  document<T = unknown>(name: string): Promise<ControlAnswer<T>> {
    return this.control<T>(fixture.control.document, { name });
  }

  // Arms or disarms the connection-drop switch. Passing zero disarms it and
  // reports the drops that actually landed.
  armConnectionDrops(count: number): Promise<ControlAnswer<DropsResult>> {
    return this.control<DropsResult>(fixture.control.drops, { count });
  }

  connections(): Promise<ControlAnswer<ConnectionsResult>> {
    return this.control<ConnectionsResult>(fixture.control.connections, {});
  }

  rotation(operation: "start" | "rotate" | "stop"): Promise<ControlAnswer<RotationResult>> {
    return this.control<RotationResult>(fixture.control.rotation, { operation });
  }

  load(operation: "start" | "shutdown"): Promise<ControlAnswer<LoadResult>> {
    return this.control<LoadResult>(fixture.control.load, { operation });
  }

  async get(route: string, accept?: string): Promise<Answer> {
    const headers: Record<string, string> = {};
    if (accept) {
      headers.accept = accept;
    }
    return this.send(this.info.pluginURL, route, { method: "GET", headers });
  }

  async post(route: string, body: string, contentType?: string): Promise<Answer> {
    return this.send(this.info.pluginURL, route, {
      method: "POST",
      headers: contentType ? { "content-type": contentType } : {},
      body,
    });
  }

  async send(
    base: string,
    route: string,
    init: { method: string; headers?: Record<string, string>; body?: string },
  ): Promise<Answer> {
    const response = await fetch(new URL(route, base), init);
    const text = await response.text();
    return {
      status: response.status,
      mediaType: response.headers.get("content-type") ?? "",
      allow: response.headers.get("allow"),
      text,
    };
  }
}

export function bodyOf(answer: Answer | ControlAnswer<unknown>): Record<string, unknown> {
  return JSON.parse(answer.text) as Record<string, unknown>;
}

export function fieldsOf(document: unknown): string[] {
  return Object.keys(document as Record<string, unknown>).sort();
}
