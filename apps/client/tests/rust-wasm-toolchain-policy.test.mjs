import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../../", import.meta.url);

test("release toolchain fixes reviewed Rust and wasm-bindgen identities", async () => {
  const [
    identityText,
    rustToolchain,
    policy,
    installer,
    fixture,
    build,
    deploy,
    browserGate,
    documentation,
  ] = await Promise.all([
    readFile(new URL("config/release-toolchain.json", root), "utf8"),
    readFile(new URL("rust-toolchain.toml", root), "utf8"),
    readFile(new URL("scripts/rust-wasm-toolchain.ps1", root), "utf8"),
    readFile(
      new URL("scripts/install-release-wasm-bindgen.ps1", root),
      "utf8",
    ),
    readFile(new URL("scripts/test-rust-wasm-toolchain.ps1", root), "utf8"),
    readFile(new URL("scripts/build-web.ps1", root), "utf8"),
    readFile(new URL("scripts/deploy-hosting.ps1", root), "utf8"),
    readFile(new URL("scripts/test-browser-audio.mjs", root), "utf8"),
    readFile(new URL("docs/rust-wasm-release-toolchain.md", root), "utf8"),
  ]);

  const identity = JSON.parse(identityText);
  assert.deepEqual(Object.keys(identity).sort(), [
    "rust",
    "schemaVersion",
    "wasmBindgen",
  ]);
  assert.equal(identity.schemaVersion, 1);
  assert.equal(identity.rust.toolchain, "1.93.0");
  assert.equal(
    identity.rust.rustcCommit,
    "254b59607d4417e9dffbc307138ae5c86280fe4c",
  );
  assert.equal(
    identity.rust.cargoCommit,
    "083ac5135f967fd9dc906ab057a2315861c7a80d",
  );
  assert.equal(identity.rust.target, "wasm32-unknown-unknown");
  assert.equal(
    identity.rust.channelManifestSha256,
    "beb6ba4e41c84e9c11c80e6804a007497d0c8ba0810cd403fabc8f4a9c45b1f8",
  );
  assert.equal(identity.wasmBindgen.version, "0.2.126");

  const windows = identity.wasmBindgen.platforms["windows-x86_64"];
  const linux = identity.wasmBindgen.platforms["linux-x86_64"];
  assert.equal(
    windows.archiveSha256,
    "5a3773c7e69cfb2d865e235e9210de184c8c3af1787720646ec1a8bbe09c6179",
  );
  assert.equal(
    windows.executableSha256,
    "2d5de73be088f1b53764fb298fe24f9fd44f438fa02e5d208159780b20e858ed",
  );
  assert.equal(
    linux.archiveSha256,
    "064948d58e2d6c0a745216477a639ba696216d6309aaa902939d1b865b1d869d",
  );
  assert.equal(
    linux.executableSha256,
    "d8d94635b40d1d8a93562fc7ced6093488252600dba39ce1dbab410b89157d8b",
  );
  for (const platform of [windows, linux]) {
    assert.equal(
      platform.source,
      `https://github.com/wasm-bindgen/wasm-bindgen/releases/download/0.2.126/${platform.archiveName}`,
    );
  }

  assert.match(rustToolchain, /channel = "1\.93\.0"/u);
  assert.match(rustToolchain, /profile = "minimal"/u);
  assert.match(rustToolchain, /targets = \["wasm32-unknown-unknown"\]/u);

  assert.match(policy, /function Assert-CanonicalLeafPath/u);
  assert.match(policy, /function Select-CanonicalApplicationPath/u);
  assert.match(policy, /function Get-CanonicalGitCommand/u);
  assert.match(
    policy,
    /Get-Command "git" -CommandType Application -All/u,
  );
  assert.match(policy, /FileAttributes\]::ReparsePoint/u);
  assert.match(policy, /LinkType/u);
  assert.match(policy, /function Assert-ReleaseToolchain/u);
  assert.match(policy, /function Assert-ReleaseToolchainProvenance/u);
  assert.match(policy, /Get-FileHash -Algorithm SHA256/u);
  assert.match(
    policy,
    /rustupCommand\.Source\s+which\s+--toolchain\s+\$toolchainName\s+cargo/u,
  );
  assert.match(
    policy,
    /rustupCommand\.Source\s+which\s+--toolchain\s+\$toolchainName\s+rustc/u,
  );
  assert.doesNotMatch(
    policy,
    /\$toolchainArgument\s*=\s*"\+\$\(\$Configuration\.rust\.toolchain\)"/u,
  );
  assert.match(installer, /archive SHA-256 does not match/u);
  assert.match(installer, /archive contains an unexpected path/u);
  assert.match(installer, /Get-Command[\s\S]+-All/u);
  assert.equal(
    (installer.match(/Select-CanonicalApplicationPath/gu) ?? []).length,
    2,
  );
  assert.match(installer, /\/usr\/bin\/curl/u);
  assert.match(installer, /\/usr\/bin\/tar/u);
  assert.match(fixture, /zero application candidates/u);
  assert.match(fixture, /selected application link ancestor/u);
  assert.match(fixture, /ItemType Junction/u);
  assert.match(fixture, /ItemType SymbolicLink/u);
  assert.match(fixture, /symlink or reparse ancestor/u);

  const preflightIndex = build.indexOf("Assert-ReleaseToolchain");
  const buildIndex = build.indexOf('" build `');
  assert.ok(preflightIndex >= 0 && buildIndex > preflightIndex);
  assert.match(build, /schemaVersion = 2/u);
  assert.match(build, /\$releaseGit = Get-CanonicalGitCommand/u);
  assert.match(build, /toolchain = \$releaseToolchain/u);
  assert.match(deploy, /Assert-ReleaseToolchainProvenance/u);
  assert.match(deploy, /\$git = Get-CanonicalGitCommand/u);
  assert.match(
    deploy,
    /artifacts,schemaVersion,sourceCommit,toolchain/u,
  );
  assert.match(browserGate, /manifest\.schemaVersion !== 2/u);
  assert.match(browserGate, /wasmBindgenExecutableSha256/u);

  assert.match(documentation, /local absolute path/u);

  for (const committedText of [
    identityText,
    rustToolchain,
    policy,
    installer,
    fixture,
    documentation,
  ]) {
    assert.doesNotMatch(committedText, /C:\\Users\\/u);
    assert.doesNotMatch(committedText, /\/home\/[A-Za-z0-9._-]+\//u);
  }
});
