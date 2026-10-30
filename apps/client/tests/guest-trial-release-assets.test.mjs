import assert from "node:assert/strict";
import {readFileSync} from "node:fs";
import test from "node:test";

test("guest timing imports remain in every release asset allowlist", () => {
  const root = new URL("../../../", import.meta.url);
  for (const [path, count] of [["scripts/build-web.ps1", 3],
    ["scripts/deploy-hosting.ps1", 2], ["scripts/test-browser-audio.mjs", 1]]) {
    const source = readFileSync(new URL(path, root), "utf8");
    for (const name of ["guest-trial-controller.mjs", "guest-trial-timing.mjs", "guest-trial-latency.mjs"]) {
      assert.equal(source.split(`"${name}"`).length - 1, count, `${path}: ${name}`);
    }
  }
});
