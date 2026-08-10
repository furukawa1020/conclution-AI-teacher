import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../../", import.meta.url);

test("Hosting release binds one clean origin/main commit to immutable artifacts", async () => {
  const [build, deploy, firebase] = await Promise.all([
    readFile(new URL("scripts/build-web.ps1", root), "utf8"),
    readFile(new URL("scripts/deploy-hosting.ps1", root), "utf8"),
    readFile(new URL("firebase.json", root), "utf8"),
  ]);

  assert.match(build, /\[string\]\s+\$ExpectedGitCommit/u);
  assert.match(build, /Assert-ReleaseSourceState[\s\S]+starting the web release build/u);
  assert.match(build, /Assert-ReleaseSourceState[\s\S]+finalizing the web release build/u);
  assert.match(build, /\.kotae-release-manifest\.json/u);
  assert.match(build, /web-build\.lock/u);
  assert.match(build, /\[System\.IO\.FileShare\]::None/u);
  assert.match(build, /sha256\s*=\s*\(Get-FileHash/u);
  assert.match(build, /sourceCommit\s*=\s*\$ExpectedGitCommit/u);
  assert.match(build, /"voice-start-slo-policy\.mjs"/u);
  assert.match(
    build,
    /from "\.\/voice-start-slo-policy\.mjs";/u,
  );

  assert.match(
    deploy,
    /\[Parameter\(Mandatory\)\][\s\S]+\[string\]\s+\$ExpectedGitCommit/u,
  );
  assert.match(deploy, /\$head\s+-cne\s+\$ExpectedGitCommit/u);
  assert.match(deploy, /\$originMain\s+-cne\s+\$ExpectedGitCommit/u);
  assert.match(deploy, /status",\s*"--porcelain=v1",\s*"--untracked-files=all"/u);
  assert.match(deploy, /Hosting artifact does not match its release manifest/u);
  assert.match(deploy, /\$hostingSnapshot\s*=\s*Assert-HostingArtifact/u);
  assert.match(deploy, /Get-GzipBytes -Bytes \(\[byte\[\]\] \$hostingSnapshot\[\$relative\]\)/u);
  assert.doesNotMatch(deploy, /Get-GzipBytes -Path/u);
  assert.match(deploy, /"run",\s*"revisions",\s*"describe"/u);
  assert.match(deploy, /\$promotedRevision\.spec\.containers/u);
  assert.match(deploy, /status\.imageDigest/u);
  assert.match(deploy, /\[System\.Net\.HttpStatusCode\]::Unauthorized/u);
  assert.match(deploy, /"voice-start-slo-policy\.mjs"/u);
  assert.match(
    deploy,
    /from "\.\/voice-start-slo-policy\.mjs";/u,
  );

  const config = JSON.parse(firebase);
  assert.equal(config.hosting.rewrites[0].run.pinTag, false);
});
