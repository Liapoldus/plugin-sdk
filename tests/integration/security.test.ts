import net from "node:net";
import tls from "node:tls";
import https from "node:https";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { contract, mediaType, path, transportProblem } from "../support/contract";
import { fixture } from "../support/harness";
import { Runtime, bodyOf, type Answer } from "../support/runtime";

const okStatus = 200;
const tlsVersion12 = "TLSv1.2";
const acceptedOutcome = new Set([
  "invalidRequest",
  "notPermitted",
  "unknownGeneration",
  "staleGeneration",
  "generationConflict",
  "digestMismatch",
  "schemaVersionMismatch",
  "documentMalformed",
  "documentOversized",
  "applyRejected",
  "coreUnavailable",
  "cancelled",
  "deadlineExceeded",
]);

let runtime: Runtime;
let active: { generation: string; sha256: string; schemaVersion: string };

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

function endpoint(name: string): { host: string; port: number } {
  const url = new URL(runtime.info.pluginHTTPSURL);
  return { host: url.hostname, port: Number(url.port) };
}

function handshake(options: { maxVersion?: string; minVersion?: string }): Promise<string> {
  const { host, port } = endpoint("ready");
  return new Promise<string>((resolve) => {
    const socket = tls.connect({
      host,
      port,
      servername: "liapoldus",
      rejectUnauthorized: false,
      ...options,
    });
    const settle = (result: string) => {
      socket.destroy();
      resolve(result);
    };
    socket.setTimeout(5000);
    socket.once("secureConnect", () => settle(`connected ${socket.getProtocol()}`));
    socket.once("timeout", () => settle("timeout"));
    socket.once("error", (failure: NodeJS.ErrnoException) =>
      settle(failure.code ?? failure.message),
    );
  });
}

function plainRequest(): Promise<string> {
  const { host, port } = endpoint("ready");
  return new Promise<string>((resolve) => {
    const socket = net.connect(port, host, () => {
      socket.write(`GET ${path("ready")} HTTP/1.1\r\nHost: liapoldus\r\nConnection: close\r\n\r\n`);
    });
    socket.setEncoding("utf8");
    let received = "";
    socket.on("data", (chunk: string) => {
      received += chunk;
    });
    const settle = () => {
      socket.destroy();
      resolve(received);
    };
    socket.once("end", settle);
    socket.once("error", (failure: NodeJS.ErrnoException) => resolve(failure.code ?? ""));
    setTimeout(settle, 5000);
  });
}

function anonymousRequest(): Promise<string> {
  const { host, port } = endpoint("ready");
  return new Promise<string>((resolve) => {
    const request = https.request(
      {
        host,
        port,
        path: path("ready"),
        method: "GET",
        rejectUnauthorized: false,
        agent: false,
      },
      (response) => {
        response.resume();
        resolve(`answered ${response.statusCode ?? 0}`);
      },
    );
    request.once("error", (failure: NodeJS.ErrnoException) =>
      resolve(failure.code ?? failure.message),
    );
    request.end();
  });
}

describe("the mutual-TLS boundary the contract requires", () => {
  it("requires a client certificate and pins the peer identity it accepts", () => {
    const security = contract.transportSecurity;

    expect(security.clientCertificateRequired).toBe(true);
    expect(security.peerIdentity.revocationFailClosed).toBe(true);
    expect(security.peerIdentity.commonNameRequired).toBe(true);
    expect(security.peerIdentity.uniformResourceIdentifierPrefix).toMatch(
      /^[a-z][a-z0-9+.-]*:\/\/.+/,
    );
    expect(security.peerIdentity.commonNameMaximumLength).toBeGreaterThan(0);
    expect(runtime.info.mTLSRejectsAnonymous).toBe("true");
  });

  it("refuses a client that presents no certificate", async () => {
    const result = await anonymousRequest();

    expect(result, "an anonymous client must not be answered").toMatch(
      /CERTIFICATE_REQUIRED|certificate_required|unknown ca|handshake failure/,
    );
    expect(result).not.toMatch(/^answered/);
  });

  it("refuses a peer whose certificate the revocation list names", () => {
    // A live handshake, not a restatement of the contract: the fixture offers a
    // genuine Core replica client certificate from the same authority, with the
    // same name and the same client-auth usage as the live credential, differing
    // only in that the revocation list names its serial. Nothing else about it
    // could be refused, so a refusal is evidence about revocation specifically.
    // The same probe first proves the listener answers a good credential, so a
    // refused connection cannot be mistaken for a listener that was never
    // serving.
    expect(
      runtime.info.mTLSRejectsRevoked,
      "a revoked peer must be refused by the listener itself",
    ).toBe("true");
  });

  it("refuses a peer that presents a different replica identity", () => {
    // A live handshake, not a restatement of the contract. The impostor is issued
    // by the very authority the listener trusts, is in date, carries the same
    // client-auth usage as the live credential, and its URI still satisfies the
    // contract's trust-domain prefix rule. The only things that differ are the
    // common name and the URI, so a refusal cannot be explained by trust, by
    // validity, by revocation or by usage: it is evidence about the pinned peer
    // identity specifically. Both names differ on purpose, because the SDK
    // accepts a peer that matches either one, and an impostor that got only the
    // common name wrong would be accepted on its URI.
    expect(
      runtime.info.mTLSRejectsWrongIdentity,
      "a peer presenting a different identity must be refused by the listener itself",
    ).toBe("true");
  });

  it("refuses a peer whose certificate has expired", () => {
    // A live handshake, not a restatement of the contract. The expired peer is
    // the registered identity, issued by the trusted authority, with the same
    // client-auth usage and a serial the revocation list does not name. The only
    // thing wrong with it is that its validity window has closed, so a refusal
    // is evidence about validity specifically.
    expect(
      runtime.info.mTLSRejectsExpired,
      "an expired peer must be refused by the listener itself",
    ).toBe("true");
  });

  it("refuses a plaintext request on the control listener", async () => {
    const answer = await plainRequest();

    expect(answer).toContain("400");
    expect(answer.toLowerCase()).toContain("https");
  });

  it("refuses a handshake below the TLS floor the contract names", async () => {
    expect(contract.transportSecurity.minimumTLSVersion).toBe(772);

    const result = await handshake({ maxVersion: tlsVersion12 });

    expect(result).toMatch(/PROTOCOL_VERSION|WRONG_VERSION|NO_PROTOCOLS/);
  });

  it("names one trust domain for the control plane and nothing else", () => {
    const security = contract.transportSecurity;
    const securityValues = JSON.stringify(security);

    expect(security.trustDomain).toBeTruthy();
    expect(securityValues).toContain(security.trustDomain);
    expect(securityValues).not.toContain("InsecureSkipVerify");
    expect(securityValues).not.toContain("http://");
  });
});

describe("the refusal bodies a replica publishes", () => {
  it("publishes only the outcome and the code the contract defines", async () => {
    const attempts: { body: string; contentType: string }[] = [
      { body: JSON.stringify({ ...active, generation: "../escape" }), contentType: mediaType.reloadRequest },
      {
        body: `{"generation":"${active.generation}","generation":"${active.generation}","sha256":"${active.sha256}","schemaVersion":"${active.schemaVersion}"}`,
        contentType: mediaType.reloadRequest,
      },
      { body: JSON.stringify({ generation: active.generation }), contentType: mediaType.reloadRequest },
      { body: JSON.stringify({ ...active, generation: "g".repeat(4096) }), contentType: mediaType.reloadRequest },
      { body: JSON.stringify(active), contentType: "text/plain" },
    ];

    for (const attempt of attempts) {
      const answer = await runtime.post(path("reload"), attempt.body, attempt.contentType);
      const document = bodyOf(answer);
      const members = Object.keys(document).sort();

      expect(answer.status, attempt.body.slice(0, 40)).toBeGreaterThanOrEqual(400);
      expect(answer.status, attempt.body.slice(0, 40)).toBeLessThan(600);
      expect(answer.mediaType, "a refusal is a JSON document").toContain(mediaType.json);
      if (document[fixture.keys.outcome] !== undefined) {
        expect(
          acceptedOutcome.has(String(document[fixture.keys.outcome])),
          `outcome ${String(document[fixture.keys.outcome])}`,
        ).toBe(true);
        expect(members, "an outcome refusal carries its outcome and its code").toEqual(
          [fixture.keys.code, fixture.keys.outcome].sort(),
        );
      } else {
        expect(members, "a transport refusal carries only its code").toEqual([
          fixture.keys.code,
        ]);
      }
      expect(String(document[fixture.keys.code])).toMatch(/^[a-z][a-z0-9_]*$/);
    }
  });

  it("names a transport refusal with a code the contract publishes", async () => {
    const answer = await runtime.post(path("reload"), "{}", "text/plain");

    expect(answer.status).toBe(transportProblem("unsupportedMediaType").status);
    expect(bodyOf(answer)[fixture.keys.code]).toBe(
      transportProblem("unsupportedMediaType").code,
    );
  });

  it("never repeats a product setting, a document or an address in a refusal", async () => {
    const secret = "value-for-never-logged";
    const attempts: Answer[] = [
      await runtime.post(
        path("reload"),
        JSON.stringify({ ...active, generation: "generation-secret", password: secret }),
        mediaType.reloadRequest,
      ),
      await runtime.get("/_liapoldus/v1/secret"),
      await runtime.get("/_liapoldus/v1/config"),
    ];

    for (const attempt of attempts) {
      expect(attempt.text, "a request member").not.toContain(secret);
      expect(attempt.text, "a document body").not.toContain(fixture.document.wellFormed);
      expect(attempt.text, "a path").not.toMatch(/\/(home|var|etc|Users)\//);
      expect(attempt.text, "a scheme-qualified address").not.toMatch(/127\.0\.0\.1|localhost/);
      expect(attempt.text, "a certificate").not.toMatch(/BEGIN [A-Z ]*PRIVATE KEY/);
    }
  });
});

describe("the log a replica publishes", () => {
  it("publishes one bounded JSON lifecycle event per outcome and no secret material", async () => {
    const answer = await runtime.post(
      path("reload"),
      JSON.stringify({ ...active, generation: "generation-logged" }),
      mediaType.reloadRequest,
    );
    expect(answer.status).toBeGreaterThanOrEqual(400);

    const generation = "generation-logged-pull";
    await runtime.publish({ generation, rawJSON: fixture.document.wellFormed });
    await runtime.armPullFault(generation, fixture.pullFaults.unavailable);
    const refused = await runtime.reloadViaCore({ ...active, generation });
    expect(refused.status).toBeGreaterThanOrEqual(400);

    const lines = runtime.stderr
      .split("\n")
      .filter((line) => line.trim().length > 0)
      .map((line) => JSON.parse(line) as Record<string, unknown>);

    expect(lines.length).toBeGreaterThan(0);
    const allowed = new Set(["kind", "level", "message", "outcome", "stream", "time"]);
    for (const line of lines) {
      for (const member of Object.keys(line)) {
        expect(allowed.has(member), `log member ${member}`).toBe(true);
      }
      expect(String(line.kind)).toBeTruthy();
      expect(String(line.level)).toBeTruthy();
      expect(contract.logging.levels as string[]).toContain(String(line.level));
      expect(String(line.stream)).toBe(contract.logging.stream);
    }

    const text = runtime.stderr;
    for (const prefix of [
      fixture.secrets.handlePrefix,
      fixture.secrets.valuePrefix,
      fixture.secrets.reference,
      fixture.secrets.purpose,
      fixture.document.wellFormed,
    ]) {
      expect(text, `secret material ${prefix.slice(0, 12)}`).not.toContain(prefix);
    }
    expect(text).not.toMatch(/BEGIN [A-Z ]*PRIVATE KEY/);
    expect(text).not.toMatch(/127\.0\.0\.1|localhost/);
  });

  it("records exactly one event per lifecycle outcome it reports", async () => {
    const outcome = "coreUnavailable";
    const generation = "generation-single-event";
    await runtime.publish({ generation, rawJSON: fixture.document.wellFormed });
    await runtime.armPullFault(generation, fixture.pullFaults.unavailable);
    const before = runtime.stderr.length;
    const refused = await runtime.reloadViaCore({ ...active, generation });
    const fresh = runtime.stderr.slice(before);

    expect(refused.status).toBeGreaterThanOrEqual(400);
    const events = fresh
      .split("\n")
      .filter((line) => line.trim().length > 0)
      .map((line) => JSON.parse(line) as Record<string, unknown>)
      .filter((event) => event.outcome === outcome);
    expect(events).toHaveLength(1);
    expect(events[0].kind).toBe("reload");
  });

  it("keeps a log line inside the bounds the contract publishes", async () => {
    const limits = contract.logging as unknown as Record<string, unknown>;

    for (const line of runtime.stderr.split("\n").filter((each) => each.trim().length > 0)) {
      const event = JSON.parse(line) as Record<string, unknown>;
      const members = Object.keys(event);

      expect(members.length, "fields per event").toBeLessThanOrEqual(
        limits.maximumFields as number,
      );
      for (const member of members) {
        expect(member.length, `key length of ${member}`).toBeLessThanOrEqual(
          limits.maximumKeyLength as number,
        );
        expect(String(event[member]).length, `value length of ${member}`).toBeLessThanOrEqual(
          limits.maximumValueLength as number,
        );
      }
    }
  });
});

describe("the control URLs the SDK will dial", () => {
  it("refuses a control URL that is not a mutual-TLS Core endpoint", () => {
    expect(runtime.info.rejectsUnsafeControlURLs).toBe("true");
    expect(runtime.info.coreURL.startsWith("https://")).toBe(true);
    expect(runtime.info.pluginHTTPSURL.startsWith("https://")).toBe(true);
  });

  it("serves the replica's own surface on a separate plaintext test mirror only", async () => {
    expect(runtime.info.pluginURL.startsWith("http://")).toBe(true);
    expect(runtime.info.pluginURL).not.toBe(runtime.info.pluginHTTPSURL);
    expect(await anonymousRequest()).not.toMatch(/^answered/);
    const mirror = await runtime.get(path("health"));

    expect(mirror.status).toBe(okStatus);
  });
});
