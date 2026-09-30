import { readFileSync } from "node:fs";
import { resolve } from "node:path";

export const projectRoot = resolve(import.meta.dirname, "../..");

export const contractFile = resolve(
  projectRoot,
  "infrastructure/assets/plugin-sdk/v1/http-contract.json",
);

export const contract = JSON.parse(readFileSync(contractFile, "utf8"));

export type Endpoint = { method: string; path: string };
export type Problem = { status: number; code: string };

export const endpointNames: string[] = Object.keys(contract.plugin.endpoints);
export const problemKeys: string[] = Object.keys(contract.problems);
export const errorKeys: string[] = Object.keys(contract.errors);
export const outcomeFamilies: string[] = Object.keys(contract.outcomes);
export const outcomeNames: string[] = Object.values(contract.outcomes).flat() as string[];
export const successOutcomes: string[] = contract.successOutcomes;

export function endpoint(name: string): Endpoint {
  const found = contract.plugin.endpoints[name];
  if (!found) {
    throw new Error(`the contract registers no endpoint named ${name}`);
  }
  return found as Endpoint;
}

export function path(name: string): string {
  return endpoint(name).path;
}

export function method(name: string): string {
  return endpoint(name).method;
}

export function transportProblem(key: string): Problem {
  const found = contract.problems[key];
  if (!found) {
    throw new Error(`the contract registers no transport problem named ${key}`);
  }
  return found as Problem;
}

export function error(key: string): Problem {
  const found = contract.errors[key];
  if (!found) {
    throw new Error(`the contract registers no error named ${key}`);
  }
  return found as Problem;
}

export function problemOfOutcome(outcome: string): Problem {
  const key = contract.outcomeProblems[outcome];
  if (typeof key !== "string") {
    throw new Error(`the contract registers no problem for outcome ${outcome}`);
  }
  return error(key);
}

export function statusOfOutcome(outcome: string): number {
  return problemOfOutcome(outcome).status;
}

export function codeOfOutcome(outcome: string): string {
  return problemOfOutcome(outcome).code;
}

export function isSuccessOutcome(outcome: string): boolean {
  return successOutcomes.includes(outcome);
}

export function outcomesOf(family: string): string[] {
  const found = contract.outcomes[family];
  if (!found) {
    throw new Error(`the contract registers no outcome family named ${family}`);
  }
  return found as string[];
}

export const mediaType = {
  json: contract.plugin.responses.contentTypes.json,
  metrics: contract.plugin.responses.contentTypes.metrics,
  registration: contract.identity.registration.mediaType,
  reloadRequest: contract.plugin.reloadRequest.mediaType,
  reloadAcknowledgement: contract.plugin.reloadAcknowledgement.mediaType,
  readiness: contract.plugin.readiness.mediaType,
  manifest: contract.plugin.manifest.mediaType,
  configurationSchema: contract.plugin.configurationSchema.mediaType,
  configPull: contract.core.configPull.responseMediaType,
};

export const maximumBytes = {
  registration: contract.identity.registration.maximumBytes,
  reloadRequest: contract.plugin.reloadRequest.maximumBytes,
  reloadAcknowledgement: contract.plugin.reloadAcknowledgement.maximumBytes,
  readiness: contract.plugin.readiness.maximumBytes,
  manifest: contract.plugin.manifest.maximumBytes,
  configurationSchema: contract.plugin.configurationSchema.maximumBytes,
  metadata: contract.plugin.maximumMetadataBytes,
  configPull: contract.core.configPull.maximumBytes,
};

export const responseHeader: Record<string, string> = contract.core.configPull.responseHeaders;

export const pluginResponses = contract.plugin.responses;

export const metrics = contract.plugin.responses.metrics;

export const registrationRequired: string[] = contract.identity.registration.required;

export const health = {
  status: contract.plugin.responses.health.status as number,
  body: contract.plugin.responses.health.body as Record<string, unknown>,
};

export function requiredFields(document: string): string[] {
  const found = contract.plugin[document];
  if (!found) {
    throw new Error(`the contract registers no document named ${document}`);
  }
  return found.required as string[];
}

export function pullPath(generation: string): string {
  return contract.core.configPull.pathTemplate.replace(
    "{generation}",
    encodeURIComponent(generation),
  );
}
