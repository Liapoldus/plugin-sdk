import { required } from "./support/value";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const projectRoot = resolve(import.meta.dirname, "..");

describe("Plugin SDK layer structure", () => {
  it("contains only the four approved production layers", () => {
    const layerNames = readdirSync(projectRoot)
      .filter((name) => statSync(resolve(projectRoot, name)).isDirectory())
      .filter((name) => ![".git", ".github", "docs", "tests", "contracts", "node_modules"].includes(name))
      .sort();

    expect(layerNames).toEqual([
      "application",
      "domain",
      "infrastructure",
      "presentation",
    ]);
  });

  it("keeps domain divided into models and interfaces only", () => {
    expect(readdirSync(resolve(projectRoot, "domain")).sort()).toEqual([
      "interfaces",
      "models",
    ]);
  });

  it("keeps imports directed inward across the four layers", () => {
    const allowed: Record<string, string[]> = {
      domain: ["domain/"],
      application: ["domain/"],
      infrastructure: ["domain/"],
      presentation: ["domain/", "application/"],
    };

    for (const layer of Object.keys(allowed)) {
      for (const path of goFiles(resolve(projectRoot, layer))) {
        const source = readFileSync(path, "utf8");
        for (const match of source.matchAll(/"github\.com\/Liapoldus\/plugin-sdk\/([^"]+)"/g)) {
          const layerImport = required(match[1]).split("/").slice(1).join("/");
          expect(allowed[layer]).toContain(`${required(layerImport.split("/")[0])}/`);
        }
      }
    }
  });
});

function goFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) return goFiles(path);
    return entry.isFile() && entry.name.endsWith(".go") && !entry.name.endsWith("_test.go") ? [path] : [];
  });
}
