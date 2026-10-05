// Private child launch recipe shared by replay entry points.
import { fileURLToPath } from "node:url";
import assert from "node:assert/strict";
const worker = fileURLToPath(new URL("./duplex-worker.js", import.meta.url));
export function childSpec(runtime, profile, goChild) {
  const negotiated = profile.startsWith("negotiated:");
  const entry = negotiated ? fileURLToPath(new URL("./negotiated-worker.js", import.meta.url)) : worker;
  if (runtime === "go") {
    assert.ok(goChild, "Go test child path required");
    return {
      command: goChild,
      args: [negotiated ? "-test.run=^TestNegotiatedFixtureChild$" : "-test.run=^TestDuplexFixtureChild$"],
      options: { env: { SDK_FIXTURE_CHILD: profile } },
    };
  }
  if (runtime === "node")
    return { command: process.execPath, args: [entry, profile], options: {} };
  if (runtime === "deno")
    return {
      command: "deno",
      args: [
        "run",
        "--cached-only",
        "--no-npm",
        "--no-check",
        "--allow-env",
        "--fixture-control-permission",
        entry,
        profile,
      ],
      options: { pathControl: true },
    };
  throw new Error("unknown fixture runtime");
}
