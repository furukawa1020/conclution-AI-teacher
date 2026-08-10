import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { fileURLToPath } from "node:url";

const root = new URL("../../../", import.meta.url);

test("Hosting release binds one clean origin/main commit to immutable artifacts", async () => {
  const [
    build,
    deploy,
    firebase,
    cloudSetup,
    provenance,
    provenanceFixtures,
  ] = await Promise.all([
    readFile(new URL("scripts/build-web.ps1", root), "utf8"),
    readFile(new URL("scripts/deploy-hosting.ps1", root), "utf8"),
    readFile(new URL("firebase.json", root), "utf8"),
    readFile(new URL("docs/cloud-setup.md", root), "utf8"),
    readFile(new URL("scripts/release-provenance.ps1", root), "utf8"),
    readFile(new URL("scripts/test-release-provenance.ps1", root), "utf8"),
  ]);

  assert.match(build, /\[string\]\s+\$ExpectedGitCommit/u);
  assert.match(build, /Assert-ReleaseSourceState[\s\S]+starting the web release build/u);
  assert.match(build, /Assert-ReleaseSourceState[\s\S]+finalizing the web release build/u);
  assert.match(build, /\.kotae-release-manifest\.json/u);
  assert.match(build, /web-build\.lock/u);
  assert.match(build, /\[System\.IO\.FileShare\]::None/u);
  assert.match(build, /sha256\s*=\s*\(Get-FileHash/u);
  assert.match(build, /sourceCommit\s*=\s*\$ExpectedGitCommit/u);
  assert.match(build, /"voice-prepare-slo-policy\.mjs"/u);
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
  assert.match(deploy, /\$expectedSourceRevision\s*=\s*"main"/u);
  assert.match(
    deploy,
    /\$releaseProvenancePath\s*=\s*Join-Path\s+\$PSScriptRoot\s+"release-provenance\.ps1"/u,
  );
  assert.match(deploy, /^\. \$releaseProvenancePath$/mu);
  assert.doesNotMatch(
    deploy,
    /function\s+(?:Assert-SourceProvenanceLabels|Get-SolePromotedRevisionName)/u,
  );
  assert.equal(
    (deploy.match(/-ExpectedGitCommit\s+\$ExpectedGitCommit/g) ?? []).length,
    3,
  );
  assert.equal(
    (
      deploy.match(
        /-ExpectedSourceRevision\s+\$expectedSourceRevision/g,
      ) ?? []
    ).length,
    3,
  );
  assert.match(deploy, /\$traffic\s*=\s*@\(\$service\.status\.traffic\)/u);
  assert.equal(
    (deploy.match(/Get-SolePromotedRevisionName/g) ?? []).length,
    1,
  );
  assert.match(deploy, /-Traffic\s+\$traffic/u);
  assert.match(
    deploy,
    /-LatestReadyRevisionName\s+\(\[string\]\s+\$service\.status\.latestReadyRevisionName\)/u,
  );
  assert.match(
    deploy,
    /-Boundary\s+"The promoted Cloud Run traffic"/u,
  );

  assert.match(provenance, /function\s+Assert-SourceProvenanceLabels/u);
  assert.match(provenance, /\[string\]\s+\$ExpectedGitCommit/u);
  assert.match(provenance, /\[string\]\s+\$ExpectedSourceRevision/u);
  assert.match(
    provenance,
    /\$sourceCommit\s+-cnotmatch\s+'\^\[0-9a-f\]\{40\}\$'/u,
  );
  assert.match(provenance, /\$sourceCommit\s+-cne\s+\$ExpectedGitCommit/u);
  assert.match(
    provenance,
    /\$sourceRevision\s+-cne\s+\$ExpectedSourceRevision/u,
  );
  assert.match(provenance, /\$unknownSourceLabels\.Count\s+-ne\s+0/u);
  assert.match(provenance, /\$_\s+-like\s+"source-\*"/u);
  assert.match(provenance, /function\s+Get-SolePromotedRevisionName/u);
  assert.match(provenance, /\[object\[\]\]\s+\$Traffic/u);
  assert.match(provenance, /\[string\]\s+\$LatestReadyRevisionName/u);
  assert.match(provenance, /\$Traffic\.Count\s+-ne\s+1/u);
  assert.match(provenance, /\$percent\s+-cne\s+"100"/u);
  assert.match(
    provenance,
    /\$revisionName\s+-cne\s+\$LatestReadyRevisionName/u,
  );
  assert.match(
    provenance,
    /-not\s+\[string\]::IsNullOrWhiteSpace\(\$tag\)/u,
  );
  assert.doesNotMatch(provenance, /\$ErrorActionPreference|Set-StrictMode|Write-Output/u);
  assert.match(
    deploy,
    /-Labels\s+\$service\.metadata\.labels\s+`[\s\S]+-Boundary\s+"The promoted Cloud Run service"/u,
  );
  assert.match(
    deploy,
    /-Labels\s+\$service\.spec\.template\.metadata\.labels\s+`[\s\S]+-Boundary\s+"The promoted Cloud Run revision template"/u,
  );
  assert.match(
    deploy,
    /-Labels\s+\$promotedRevision\.metadata\.labels\s+`[\s\S]+-Boundary\s+"The revision receiving production traffic"/u,
  );
  assert.match(deploy, /\[System\.Net\.HttpStatusCode\]::Unauthorized/u);
  assert.match(deploy, /"voice-prepare-slo-policy\.mjs"/u);
  assert.match(deploy, /"voice-start-slo-policy\.mjs"/u);
  assert.match(
    deploy,
    /from "\.\/voice-start-slo-policy\.mjs";/u,
  );

  assert.match(cloudSetup, /\$SourceRevision\s*=\s*"main"/u);
  assert.match(
    cloudSetup,
    /--update-labels="application=kotae-ai,source-commit=\$GitSha,source-revision=\$SourceRevision"/u,
  );
  assert.doesNotMatch(cloudSetup, /source-commit=.*GitSha\.Substring/u);

  for (const fixtureName of [
    "valid",
    "null",
    "missing",
    "12-character commit",
    "stale 40-character commit",
    "wrong source revision",
    "unknown source label",
    "valid traffic",
    "null traffic",
    "empty traffic",
    "multiple traffic targets",
    "tagged traffic",
    "latest ready mismatch",
    "percent is not 100",
    "revision name missing",
    "revision name empty",
  ]) {
    assert.ok(provenanceFixtures.includes(`\"${fixtureName}\"`));
  }
  if (process.platform === "win32") {
    const fixtureOutput = execFileSync(
      "powershell.exe",
      [
        "-NoLogo",
        "-NoProfile",
        "-NonInteractive",
        "-ExecutionPolicy",
        "Bypass",
        "-File",
        fileURLToPath(
          new URL("scripts/test-release-provenance.ps1", root),
        ),
      ],
      { encoding: "utf8", windowsHide: true },
    );
    assert.match(fixtureOutput, /RELEASE_PROVENANCE_FIXTURES=PASS/u);
    assert.match(fixtureOutput, /RELEASE_TRAFFIC_FIXTURES=PASS/u);
  }

  const config = JSON.parse(firebase);
  assert.equal(config.hosting.rewrites[0].run.pinTag, false);
});
