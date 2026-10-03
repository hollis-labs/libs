import { readFileSync } from "node:fs";
import { Registry } from "../src/index.js";
import type { Options } from "../src/index.js";
export const corpus = JSON.parse(
  readFileSync(
    new URL("../../conformance/execution.json", import.meta.url),
    "utf8",
  ),
) as {
  catalog: unknown;
  cases: {
    name: string;
    hook: string;
    config?: { maxDepth: number };
    payload: string;
    registrations: {
      name: string;
      behavior?: string;
      output?: string;
      options?: Options;
    }[];
    expected?: {
      status: string;
      order: string[];
      classes: string[];
      value?: string;
    }[];
    seen?: string[];
    remove?: string[];
    dispose?: boolean;
    warnings?: number;
    receipt?: string;
    error?: string;
  }[];
};
export function registry(): Registry {
  return new Registry(JSON.stringify(corpus.catalog), (d) => ({
    input: validator,
    ...(d.kind === "filter" ? { output: validator } : {}),
  }));
}
function validator(json: string): void {
  if (json === '{"reject":true}') throw Error("reject");
}
