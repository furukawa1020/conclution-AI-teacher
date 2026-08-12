import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../../", import.meta.url);

test("Cloud Run source upload is an exact Go API allowlist", async () => {
  const source = await readFile(new URL(".gcloudignore", root), "utf8");
  const rules = source
    .split(/\r?\n/u)
    .map((line) => line.trim())
    .filter((line) => line.length > 0 && !line.startsWith("#"));

  assert.deepEqual(rules, [
    "**",
    "!.gcloudignore",
    "!.dockerignore",
    "!Dockerfile",
    "!go.mod",
    "!go.sum",
    "!cmd/",
    "!cmd/**",
    "!internal/",
    "!internal/**",
    "*_test.go",
  ]);
  assert.ok(!rules.includes("!config/**"));
  assert.ok(!rules.includes("!rust-toolchain.toml"));
});
