import tls from "node:tls";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { fixture } from "../support/harness";
import { Runtime, type ReadyLine } from "../support/runtime";

// The credential-rotation surface is fixture-only state, so this file owns its
// process and takes the surface down itself instead of leaving a rotated
// listener behind for other files to inherit.
const observationTimeoutMilliseconds = 10_000;

let runtime: Runtime;

beforeAll(async () => {
  runtime = await Runtime.start();
}, 120_000);

afterAll(async () => {
  await runtime.stop();
});

type PeerObservation = {
  serial: string;
  subject: string;
  status: number;
};

// Completes one real request against a mutual-TLS surface and reports the
// certificate the listener actually presented, as the wire carried it. Nothing
// here trusts the surface's own account of itself.
function observePeer(url: URL, ready: ReadyLine): Promise<PeerObservation> {
  return new Promise<PeerObservation>((resolve) => {
    const socket = tls.connect({
      host: url.hostname,
      port: Number(url.port),
      servername: fixture.credentials.serverName,
      ca: ready.caBundlePEM,
      cert: ready.corePlaneClientCertificatePEM,
      key: ready.corePlaneClientKeyPEM,
    });
    const settle = (observation: PeerObservation) => {
      socket.destroy();
      resolve(observation);
    };
    const fail = (failure: NodeJS.ErrnoException) => {
      settle({ serial: `refused ${failure.code ?? failure.message}`, subject: "", status: 0 });
    };
    socket.setTimeout(observationTimeoutMilliseconds);
    socket.once("secureConnect", () => {
      const certificate = socket.getPeerCertificate() as { serialNumber?: string; subject?: string };
      socket.write(
        `GET / HTTP/1.1\r\nHost: ${fixture.credentials.serverName}\r\nConnection: close\r\n\r\n`,
      );
      let raw = "";
      let status = 0;
      socket.setEncoding("utf8");
      socket.on("data", (chunk: string) => {
        raw += chunk;
        if (status === 0) {
          status = Number(raw.split(" ")[1] ?? 0);
        }
      });
      socket.once("end", () =>
        settle({
          serial: certificate.serialNumber ?? "",
          subject: certificate.subject ?? "",
          status,
        }),
      );
    });
    socket.once("timeout", () => fail(Object.assign(new Error("timeout"), { code: "ETIMEDOUT" })));
    socket.once("error", fail);
  });
}

// The same request without a client certificate. The rotation surface is a
// production-shaped mutual-TLS listener, so a client that offers no credential
// has to be refused rather than served.
//
// The probe has to complete a real request rather than trust the handshake event.
// Under TLS 1.3 the client finishes its side of the handshake and fires
// secureConnect before the server's certificate-required alert arrives, because
// the server's rejection of the client is decided after the client's Finished. A
// probe that settled on secureConnect would therefore report a refusal as a
// served answer. The verdict is only read off a response the peer actually
// produced, so an alert, a reset and a hang are all refusals and only a real
// status line counts as being served.
function observeWithoutCredential(url: URL, ready: ReadyLine): Promise<string> {
  return new Promise<string>((resolve) => {
    const socket = tls.connect({
      host: url.hostname,
      port: Number(url.port),
      servername: fixture.credentials.serverName,
      ca: ready.caBundlePEM,
    });
    const settle = (result: string) => {
      socket.destroy();
      resolve(result);
    };
    socket.setTimeout(observationTimeoutMilliseconds);
    socket.once("secureConnect", () => {
      socket.write(
        `GET / HTTP/1.1\r\nHost: ${fixture.credentials.serverName}\r\nConnection: close\r\n\r\n`,
      );
      let raw = "";
      socket.setEncoding("utf8");
      socket.on("data", (chunk: string) => {
        raw += chunk;
        const status = Number(raw.split(" ")[1] ?? 0);
        if (status > 0) {
          settle(`answered ${status}`);
        }
      });
      socket.once("end", () => settle(`answered ${Number(raw.split(" ")[1] ?? 0)}`));
    });
    socket.once("timeout", () => settle("timeout"));
    socket.once("error", (failure: NodeJS.ErrnoException) =>
      settle(failure.code ?? failure.message),
    );
  });
}

// A certificate serial is an ASN.1 INTEGER, so the wire form and the fixture's
// published form can differ in leading zeros for the same number. The subject of
// these assertions is which certificate was presented, so both sides are read as
// numbers and a formatting difference cannot decide the verdict.
function sameSerial(observed: string, expected: string): boolean {
  const read = (value: string) => BigInt(value.startsWith("0x") ? value : `0x${value}`);
  try {
    return read(observed) === read(expected);
  } catch {
    return observed === expected;
  }
}

describe("the credential rotation a surface performs while it serves", () => {
  it("serves a new certificate on the wire after new material is written to disk", async () => {
    const started = await runtime.rotation("start");

    expect(started.status).toBe(200);
    expect(started.body.failure ?? "").toBe("");
    const address = new URL(String(started.body.url));
    expect(address.protocol).toBe("https:");

    const before = await observePeer(address, runtime.info);
    expect(before.status, "the rotation surface answers a good credential").toBe(200);
    expect(
      sameSerial(before.serial, fixture.credentials.rotationBeforeSerial),
      "the first handshake must present the certificate written to disk first",
    ).toBe(true);

    const rotated = await runtime.rotation("rotate");

    expect(rotated.status).toBe(200);
    expect(rotated.body.failure ?? "").toBe("");
    expect(
      sameSerial(
        String(rotated.body.providerSerialBefore),
        fixture.credentials.rotationBeforeSerial,
      ),
      "the provider re-read the material instead of keeping the certificate it loaded first",
    ).toBe(true);
    expect(sameSerial(String(rotated.body.providerSerialAfter), fixture.credentials.rotationAfterSerial)).toBe(
      true,
    );
    expect(sameSerial(String(rotated.body.servedSerialBefore), fixture.credentials.rotationBeforeSerial)).toBe(
      true,
    );
    expect(sameSerial(String(rotated.body.servedSerialAfter), fixture.credentials.rotationAfterSerial)).toBe(
      true,
    );

    const after = await observePeer(address, runtime.info);
    expect(after.status).toBe(200);
    expect(
      sameSerial(after.serial, fixture.credentials.rotationAfterSerial),
      "a new handshake must present the replacement certificate, not the retired one",
    ).toBe(true);
    expect(after.serial).not.toBe(before.serial);

    const stopped = await runtime.rotation("stop");

    expect(stopped.status).toBe(200);
    expect(stopped.body.closed, "the listener must be shut down, not merely forgotten").toBe(true);
    expect(stopped.body.removed, "the rotated material must be removed from disk").toBe(true);
  });

  it("never downgrades the rotation surface to a plaintext or anonymous listener", async () => {
    const started = await runtime.rotation("start");

    expect(started.status).toBe(200);
    const address = new URL(String(started.body.url));

    const answer = await observeWithoutCredential(address, runtime.info);

    expect(answer, "a rotation surface must not answer an anonymous client").not.toMatch(/^answered/);
    expect(answer).toMatch(/CERTIFICATE_REQUIRED|ERR_SSL|EPROTO|ECONNRESET|ECONNREFUSED/);
  });

  it("refuses a new connection once the rotation surface has been drained", async () => {
    const started = await runtime.rotation("start");

    expect(started.status).toBe(200);
    const address = new URL(String(started.body.url));
    expect((await observePeer(address, runtime.info)).status).toBe(200);

    const stopped = await runtime.rotation("stop");

    expect(stopped.status).toBe(200);
    const after = await observePeer(address, runtime.info);
    expect(
      sameSerial(after.serial, fixture.credentials.rotationAfterSerial),
      "a drained surface must stop accepting connections",
    ).toBe(false);
    expect(after.serial).toMatch(/^refused |^$/);
  });
});
