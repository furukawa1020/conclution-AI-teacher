import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import {
  access,
  lstat,
  mkdtemp,
  readFile,
  readdir,
  realpath,
  rm,
} from "node:fs/promises";
import { constants as fsConstants } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT_DIRECTORY = path.dirname(fileURLToPath(import.meta.url));
const WORKSPACE = path.resolve(SCRIPT_DIRECTORY, "..");
const DEFAULT_DIST = path.join(WORKSPACE, "dist", "web");
const FIXTURE_DIRECTORY = path.join(
  WORKSPACE,
  "apps",
  "client",
  "tests",
  "browser",
);
const MANIFEST_NAME = ".kotae-release-manifest.json";
const MAXIMUM_ARTIFACT_BYTES = 15 * 1024 * 1024;
const MAXIMUM_TOTAL_BYTES = 25 * 1024 * 1024;
const PROFILE_CLEANUP_ATTEMPTS = 12;
const PROFILE_CLEANUP_PREFIX = "kotae-browser-audio-";
const PROFILE_CLEANUP_RETRY_MS = 100;
const RESULT_EXPRESSION =
  "globalThis.__KOTAE_BROWSER_AUDIO_RESULT__ ?? null";
const REQUIRED_ARTIFACTS = Object.freeze([
  "index.html",
  "bootstrap.js",
  "firebase-bridge.js",
  "guest-a-first-slo-policy.mjs",
  "passkey-policy.mjs",
  "temporal-vad-clock.mjs",
  "pcm-capture-worklet.js",
  "voice-session-policy.mjs",
  "voice-prepare-slo-policy.mjs",
  "voice-start-slo-policy.mjs",
  "voice-stream-policy.mjs",
  "assets/main.css",
  "wasm/kotae_client.js",
  "wasm/kotae_client_bg.wasm",
  "wasm/kotae_pcm_ring_bg.wasm",
]);
const FIXED_ARTIFACTS = new Set(REQUIRED_ARTIFACTS);
let chromeDiagnostic = "";

function fail(code) {
  throw new Error(code);
}

function parseArguments(argv) {
  const options = {
    chromePath: "",
    distPath: DEFAULT_DIST,
    expectedCommit: "",
    expectedManifestSha256: "",
  };
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    const value = argv[index + 1];
    if (
      ![
        "--chrome",
        "--dist",
        "--expected-commit",
        "--expected-manifest-sha256",
      ].includes(argument)
    ) {
      fail("browser_audio_argument_invalid");
    }
    if (!value || value.startsWith("--")) {
      fail("browser_audio_argument_value_missing");
    }
    index += 1;
    if (argument === "--chrome") options.chromePath = value;
    if (argument === "--dist") options.distPath = path.resolve(value);
    if (argument === "--expected-commit") {
      if (!/^[0-9a-f]{40}$/u.test(value)) {
        fail("browser_audio_expected_commit_invalid");
      }
      options.expectedCommit = value;
    }
    if (argument === "--expected-manifest-sha256") {
      if (!/^[0-9a-f]{64}$/u.test(value)) {
        fail("browser_audio_expected_manifest_invalid");
      }
      options.expectedManifestSha256 = value;
    }
  }
  if (options.expectedManifestSha256 && !options.expectedCommit) {
    fail("browser_audio_expected_release_incomplete");
  }
  return Object.freeze(options);
}

function normalizeRelativePath(value) {
  return value.split(path.sep).join("/");
}

function allowedArtifact(relativePath) {
  return (
    FIXED_ARTIFACTS.has(relativePath) ||
    /^wasm\/snippets\/[A-Za-z0-9._/-]+\.js$/u.test(relativePath)
  );
}

async function collectArtifactPaths(root, directory = root, output = []) {
  const entries = await readdir(directory, { withFileTypes: true });
  entries.sort((left, right) => left.name.localeCompare(right.name, "en"));
  for (const entry of entries) {
    const absolutePath = path.join(directory, entry.name);
    const metadata = await lstat(absolutePath);
    if (metadata.isSymbolicLink()) fail("browser_audio_artifact_symlink");
    if (metadata.isDirectory()) {
      await collectArtifactPaths(root, absolutePath, output);
      continue;
    }
    if (!metadata.isFile()) fail("browser_audio_artifact_type_invalid");
    const relativePath = normalizeRelativePath(path.relative(root, absolutePath));
    output.push(Object.freeze({ absolutePath, metadata, relativePath }));
  }
  return output;
}

function exactObjectKeys(value, expected) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const keys = Reflect.ownKeys(value);
  return (
    keys.length === expected.length &&
    keys.every((key) => typeof key === "string" && expected.includes(key))
  );
}

function sha256(buffer) {
  return createHash("sha256").update(buffer).digest("hex");
}

async function validateArtifacts(
  distPath,
  expectedCommit,
  expectedManifestSha256,
) {
  const distMetadata = await lstat(distPath).catch(() => null);
  if (!distMetadata?.isDirectory() || distMetadata.isSymbolicLink()) {
    fail("browser_audio_dist_invalid");
  }
  const resolvedDist = await realpath(distPath);
  const entries = await collectArtifactPaths(resolvedDist);
  const manifestEntry = entries.find(
    ({ relativePath }) => relativePath === MANIFEST_NAME,
  );
  const artifactEntries = entries.filter(
    ({ relativePath }) => relativePath !== MANIFEST_NAME,
  );
  if ((expectedCommit || expectedManifestSha256) && !manifestEntry) {
    fail("browser_audio_release_manifest_required");
  }
  for (const { metadata, relativePath } of artifactEntries) {
    if (!allowedArtifact(relativePath)) fail("browser_audio_artifact_not_allowed");
    if (metadata.size > MAXIMUM_ARTIFACT_BYTES) {
      fail("browser_audio_artifact_too_large");
    }
  }
  const actualPaths = new Set(
    artifactEntries.map(({ relativePath }) => relativePath),
  );
  for (const required of REQUIRED_ARTIFACTS) {
    if (!actualPaths.has(required)) fail("browser_audio_artifact_missing");
  }

  const snapshots = new Map();
  let totalBytes = 0;
  for (const { absolutePath, relativePath } of artifactEntries) {
    const bytes = await readFile(absolutePath);
    if (bytes.byteLength > MAXIMUM_ARTIFACT_BYTES) {
      fail("browser_audio_artifact_too_large");
    }
    totalBytes += bytes.byteLength;
    if (!Number.isSafeInteger(totalBytes) || totalBytes > MAXIMUM_TOTAL_BYTES) {
      fail("browser_audio_artifacts_too_large");
    }
    snapshots.set(
      `/${relativePath}`,
      Object.freeze({ bytes, relativePath, sha256: sha256(bytes) }),
    );
  }

  let provenance = Object.freeze({
    manifestSha256: null,
    mode: "local",
    sourceCommit: null,
  });
  if (manifestEntry) {
    const manifestBytes = await readFile(manifestEntry.absolutePath);
    if (manifestBytes.byteLength > 1024 * 1024) {
      fail("browser_audio_release_manifest_too_large");
    }
    const manifestSha256 = sha256(manifestBytes);
    if (
      expectedManifestSha256 &&
      manifestSha256 !== expectedManifestSha256
    ) {
      fail("browser_audio_release_manifest_mismatch");
    }
    let manifest;
    try {
      manifest = JSON.parse(manifestBytes.toString("utf8"));
    } catch {
      fail("browser_audio_release_manifest_invalid");
    }
    const toolchain = manifest?.toolchain;
    const reviewedWasmBindgen = {
      "windows-x86_64": {
        archiveName: "wasm-bindgen-0.2.126-x86_64-pc-windows-msvc.tar.gz",
        archiveSha256:
          "5a3773c7e69cfb2d865e235e9210de184c8c3af1787720646ec1a8bbe09c6179",
        executableSha256:
          "2d5de73be088f1b53764fb298fe24f9fd44f438fa02e5d208159780b20e858ed",
        hosts: new Set([
          "x86_64-pc-windows-msvc",
          "x86_64-pc-windows-gnu",
        ]),
      },
      "linux-x86_64": {
        archiveName: "wasm-bindgen-0.2.126-x86_64-unknown-linux-musl.tar.gz",
        archiveSha256:
          "064948d58e2d6c0a745216477a639ba696216d6309aaa902939d1b865b1d869d",
        executableSha256:
          "d8d94635b40d1d8a93562fc7ced6093488252600dba39ce1dbab410b89157d8b",
        hosts: new Set([
          "x86_64-unknown-linux-gnu",
          "x86_64-unknown-linux-musl",
        ]),
      },
    }[toolchain?.platform];
    if (
      !exactObjectKeys(manifest, [
        "schemaVersion",
        "sourceCommit",
        "toolchain",
        "artifacts",
      ]) ||
      manifest.schemaVersion !== 2 ||
      !/^[0-9a-f]{40}$/u.test(manifest.sourceCommit) ||
      !exactObjectKeys(toolchain, [
        "platform",
        "rustToolchain",
        "rustcCommit",
        "cargoCommit",
        "rustHost",
        "rustTarget",
        "rustChannelManifestSource",
        "rustChannelManifestSha256",
        "wasmBindgenVersion",
        "wasmBindgenArchiveName",
        "wasmBindgenArchiveSha256",
        "wasmBindgenExecutableSha256",
        "wasmBindgenSource",
      ]) ||
      !reviewedWasmBindgen ||
      toolchain.rustToolchain !== "1.93.0" ||
      toolchain.rustcCommit !== "254b59607d4417e9dffbc307138ae5c86280fe4c" ||
      toolchain.cargoCommit !== "083ac5135f967fd9dc906ab057a2315861c7a80d" ||
      !reviewedWasmBindgen.hosts.has(toolchain.rustHost) ||
      toolchain.rustTarget !== "wasm32-unknown-unknown" ||
      toolchain.rustChannelManifestSource !==
        "https://static.rust-lang.org/dist/channel-rust-1.93.0.toml" ||
      toolchain.rustChannelManifestSha256 !==
        "beb6ba4e41c84e9c11c80e6804a007497d0c8ba0810cd403fabc8f4a9c45b1f8" ||
      toolchain.wasmBindgenVersion !== "0.2.126" ||
      toolchain.wasmBindgenArchiveName !== reviewedWasmBindgen.archiveName ||
      toolchain.wasmBindgenArchiveSha256 !== reviewedWasmBindgen.archiveSha256 ||
      toolchain.wasmBindgenExecutableSha256 !==
        reviewedWasmBindgen.executableSha256 ||
      toolchain.wasmBindgenSource !==
        `https://github.com/wasm-bindgen/wasm-bindgen/releases/download/0.2.126/${reviewedWasmBindgen.archiveName}` ||
      !Array.isArray(manifest.artifacts)
    ) {
      fail("browser_audio_release_manifest_invalid");
    }
    if (expectedCommit && manifest.sourceCommit !== expectedCommit) {
      fail("browser_audio_release_commit_mismatch");
    }
    if (manifest.artifacts.length !== snapshots.size) {
      fail("browser_audio_release_manifest_incomplete");
    }
    const manifestPaths = new Set();
    for (const artifact of manifest.artifacts) {
      if (
        !exactObjectKeys(artifact, ["path", "sha256", "bytes"]) ||
        typeof artifact.path !== "string" ||
        !allowedArtifact(artifact.path) ||
        !/^[0-9a-f]{64}$/u.test(artifact.sha256) ||
        !Number.isSafeInteger(artifact.bytes) ||
        artifact.bytes < 0 ||
        manifestPaths.has(artifact.path)
      ) {
        fail("browser_audio_release_manifest_invalid");
      }
      manifestPaths.add(artifact.path);
      const snapshot = snapshots.get(`/${artifact.path}`);
      if (
        !snapshot ||
        snapshot.bytes.byteLength !== artifact.bytes ||
        snapshot.sha256 !== artifact.sha256
      ) {
        fail("browser_audio_release_hash_mismatch");
      }
    }
    provenance = Object.freeze({
      manifestSha256,
      mode: "release",
      sourceCommit: manifest.sourceCommit,
    });
  }
  return Object.freeze({ provenance, snapshots });
}

function mimeType(relativePath) {
  if (relativePath.endsWith(".wasm")) return "application/wasm";
  if (relativePath.endsWith(".html")) return "text/html; charset=utf-8";
  if (relativePath.endsWith(".css")) return "text/css; charset=utf-8";
  if (relativePath.endsWith(".js") || relativePath.endsWith(".mjs")) {
    return "text/javascript; charset=utf-8";
  }
  return "application/octet-stream";
}

function sendResponse(response, status, body, contentType, method) {
  response.writeHead(status, {
    "Cache-Control": "no-store",
    "Content-Security-Policy":
      "default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; connect-src 'self'; worker-src 'self'; base-uri 'none'; object-src 'none'",
    "Cross-Origin-Resource-Policy": "same-origin",
    "Referrer-Policy": "no-referrer",
    "X-Content-Type-Options": "nosniff",
    "Content-Type": contentType,
    "Content-Length": body.byteLength,
  });
  if (method === "HEAD") {
    response.end();
  } else {
    response.end(body);
  }
}

async function startServer(snapshots) {
  const fixtureHtml = await readFile(
    path.join(FIXTURE_DIRECTORY, "pcm-ring-audio-worklet.fixture.html"),
  );
  const fixtureModule = await readFile(
    path.join(FIXTURE_DIRECTORY, "pcm-ring-audio-worklet.fixture.mjs"),
  );
  const virtualRoutes = new Map([
    [
      "/__fixture/pcm-ring-audio-worklet.fixture.html",
      Object.freeze({ bytes: fixtureHtml, relativePath: "fixture.html" }),
    ],
    [
      "/__fixture/pcm-ring-audio-worklet.fixture.mjs",
      Object.freeze({ bytes: fixtureModule, relativePath: "fixture.mjs" }),
    ],
  ]);
  const server = createServer((request, response) => {
    const method = request.method ?? "";
    if (method !== "GET" && method !== "HEAD") {
      response.writeHead(405, { "Cache-Control": "no-store" });
      response.end();
      return;
    }
    let pathname;
    try {
      pathname = new URL(request.url ?? "", "http://127.0.0.1").pathname;
    } catch {
      response.writeHead(400, { "Cache-Control": "no-store" });
      response.end();
      return;
    }
    const snapshot = virtualRoutes.get(pathname) ?? snapshots.get(pathname);
    if (!snapshot) {
      response.writeHead(404, { "Cache-Control": "no-store" });
      response.end();
      return;
    }
    sendResponse(
      response,
      200,
      snapshot.bytes,
      mimeType(snapshot.relativePath),
      method,
    );
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      server.removeListener("error", reject);
      resolve();
    });
  });
  const address = server.address();
  if (!address || typeof address === "string" || address.address !== "127.0.0.1") {
    server.close();
    fail("browser_audio_loopback_server_invalid");
  }
  return Object.freeze({
    server,
    url: `http://127.0.0.1:${address.port}/__fixture/pcm-ring-audio-worklet.fixture.html`,
  });
}

async function findChrome(explicitPath) {
  const candidates = [
    explicitPath,
    process.env.KOTAE_CHROME_PATH,
    process.env.CHROME_PATH,
    process.platform === "win32"
      ? "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe"
      : "",
    process.platform === "win32"
      ? "C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe"
      : "",
    process.platform === "win32" && process.env.LOCALAPPDATA
      ? path.join(
          process.env.LOCALAPPDATA,
          "Google",
          "Chrome",
          "Application",
          "chrome.exe",
        )
      : "",
    process.platform === "darwin"
      ? "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
      : "",
    process.platform === "linux" ? "/usr/bin/google-chrome" : "",
    process.platform === "linux" ? "/usr/bin/google-chrome-stable" : "",
    process.platform === "linux" ? "/usr/bin/chromium" : "",
    process.platform === "linux" ? "/usr/bin/chromium-browser" : "",
  ].filter(Boolean);
  for (const candidate of candidates) {
    const resolved = path.resolve(candidate);
    try {
      await access(resolved, fsConstants.X_OK);
      const metadata = await lstat(resolved);
      if (metadata.isFile() && !metadata.isSymbolicLink()) return resolved;
    } catch {
      // Try the next explicit, environment, or platform-owned location.
    }
  }
  fail("browser_audio_chrome_not_found");
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

async function readDevToolsEndpoint(profileDirectory, chromeProcess) {
  const activePortPath = path.join(profileDirectory, "DevToolsActivePort");
  const deadline = Date.now() + 8_000;
  while (Date.now() < deadline) {
    if (chromeProcess.exitCode !== null) fail("browser_audio_chrome_exited_early");
    try {
      const lines = (await readFile(activePortPath, "utf8"))
        .split(/\r?\n/u)
        .filter(Boolean);
      const port = Number(lines[0]);
      if (
        Number.isSafeInteger(port) &&
        port > 0 &&
        port <= 65_535 &&
        /^\/devtools\/browser\/[A-Za-z0-9-]+$/u.test(lines[1] ?? "")
      ) {
        return `ws://127.0.0.1:${port}${lines[1]}`;
      }
    } catch {
      // Chrome writes this atomic endpoint file after the browser is ready.
    }
    await delay(25);
  }
  fail("browser_audio_devtools_timeout");
}

class CdpClient {
  constructor(socket) {
    this.socket = socket;
    this.nextId = 1;
    this.pending = new Map();
    this.runtimeException = false;
    socket.addEventListener("message", (event) => {
      let message;
      try {
        message = JSON.parse(String(event.data));
      } catch {
        return;
      }
      if (message.method === "Runtime.exceptionThrown") {
        this.runtimeException = true;
      }
      if (!Number.isSafeInteger(message.id)) return;
      const pending = this.pending.get(message.id);
      if (!pending) return;
      this.pending.delete(message.id);
      if (message.error) {
        pending.reject(new Error("browser_audio_cdp_command_failed"));
      } else {
        pending.resolve(message.result ?? {});
      }
    });
    socket.addEventListener("close", () => {
      for (const pending of this.pending.values()) {
        pending.reject(new Error("browser_audio_cdp_closed"));
      }
      this.pending.clear();
    });
  }

  send(method, params = {}, sessionId = undefined) {
    const id = this.nextId;
    this.nextId += 1;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { reject, resolve });
      const message = { id, method, params };
      if (sessionId) message.sessionId = sessionId;
      this.socket.send(JSON.stringify(message));
    });
  }
}

async function connectCdp(endpoint) {
  const socket = new WebSocket(endpoint);
  await Promise.race([
    new Promise((resolve, reject) => {
      socket.addEventListener("open", resolve, { once: true });
      socket.addEventListener(
        "error",
        () => reject(new Error("browser_audio_cdp_connect_failed")),
        { once: true },
      );
    }),
    delay(5_000).then(() => fail("browser_audio_cdp_connect_timeout")),
  ]);
  return new CdpClient(socket);
}

async function waitForBrowserResult(client, sessionId) {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    const evaluation = await client.send(
      "Runtime.evaluate",
      {
        expression: RESULT_EXPRESSION,
        returnByValue: true,
      },
      sessionId,
    );
    if (evaluation.exceptionDetails) fail("browser_audio_result_read_failed");
    const value = evaluation.result?.value;
    if (value !== null && value !== undefined) return value;
    if (client.runtimeException) fail("browser_audio_runtime_exception");
    await delay(25);
  }
  fail("browser_audio_fixture_timeout");
}

function validateBrowserResult(result) {
  if (process.env.KOTAE_BROWSER_AUDIO_DEBUG === "1") {
    process.stderr.write(
      `browser_audio_result=${JSON.stringify(result).slice(0, 2_048)}\n`,
    );
  }
  if (
    exactObjectKeys(result, ["schemaVersion", "status", "code"]) &&
    result.schemaVersion === 1 &&
    result.status === "failed" &&
    typeof result.code === "string" &&
    /^[a-z0-9_]{1,120}$/u.test(result.code)
  ) {
    fail(`browser_audio_fixture_failed_${result.code}`);
  }
  const expectedKeys = [
    "schemaVersion",
    "status",
    "sampleRateHz",
    "zeroOutputCapture",
    "wasmModuleCloned",
    "directWasmGenerationIsolation",
    "intentionalFastLaneValidated",
    "observationAddingValidated",
    "acousticExchangeabilityValidated",
    "acousticCoverageWireValidated",
    "guestAFirstSprintSloValidated",
    "guestQuietOnsetValidated",
    "quietSubbandEvidenceValidated",
    "quietSpectralCompensationValidated",
    "temporalVadClockValidated",
    "preConfirmFrames",
    "wrappedFrames",
    "frameBytes",
    "sequenceContiguous",
    "contextMonotonic",
    "senderDetachGuardPassed",
    "stoppedLeakFrames",
    "freshGenerationFrames",
    "freshGenerationIsolated",
    "sameContextReuseFrames",
    "sameContextReuseIsolated",
  ];
  if (
    !exactObjectKeys(result, expectedKeys) ||
    result.schemaVersion !== 1 ||
    result.status !== "passed" ||
    result.sampleRateHz !== 48_000 ||
    result.zeroOutputCapture !== true ||
    result.wasmModuleCloned !== true ||
    result.directWasmGenerationIsolation !== true ||
    result.intentionalFastLaneValidated !== true ||
    result.observationAddingValidated !== true ||
    result.acousticExchangeabilityValidated !== true ||
    result.acousticCoverageWireValidated !== true ||
    result.guestAFirstSprintSloValidated !== true ||
    result.guestQuietOnsetValidated !== true ||
    result.quietSubbandEvidenceValidated !== true ||
    result.quietSpectralCompensationValidated !== true ||
    result.temporalVadClockValidated !== true ||
    result.preConfirmFrames !== 0 ||
    result.wrappedFrames !== 5 ||
    result.frameBytes !== 640 ||
    result.sequenceContiguous !== true ||
    result.contextMonotonic !== true ||
    result.senderDetachGuardPassed !== true ||
    result.stoppedLeakFrames !== 0 ||
    result.freshGenerationFrames !== 3 ||
    result.freshGenerationIsolated !== true ||
    result.sameContextReuseFrames !== 2 ||
    result.sameContextReuseIsolated !== true
  ) {
    fail("browser_audio_result_invalid");
  }
  return Object.freeze(result);
}

async function closeServer(server) {
  if (!server) return;
  const closed = new Promise((resolve) => server.close(resolve));
  server.closeAllConnections?.();
  await Promise.race([closed, delay(2_000)]);
}

async function waitForProcessExit(child, timeoutMs) {
  if (
    !child ||
    child.exitCode !== null ||
    child.signalCode !== null
  ) {
    return true;
  }
  return Promise.race([
    new Promise((resolve) => child.once("exit", () => resolve(true))),
    delay(timeoutMs).then(() => false),
  ]);
}

async function terminateChromeProcessTree(chromeProcess) {
  if (
    !chromeProcess ||
    chromeProcess.exitCode !== null ||
    chromeProcess.signalCode !== null
  ) {
    return;
  }
  if (process.platform !== "win32") {
    chromeProcess.kill();
    await waitForProcessExit(chromeProcess, 2_000);
    return;
  }

  const systemRoot = process.env.SystemRoot;
  if (!systemRoot || !path.isAbsolute(systemRoot)) {
    fail("browser_audio_taskkill_boundary_invalid");
  }
  const taskkillPath = path.join(path.resolve(systemRoot), "System32", "taskkill.exe");
  const taskkillMetadata = await lstat(taskkillPath).catch(() => null);
  if (!taskkillMetadata?.isFile() || taskkillMetadata.isSymbolicLink()) {
    fail("browser_audio_taskkill_boundary_invalid");
  }
  const processId = chromeProcess.pid;
  if (!Number.isSafeInteger(processId) || processId <= 0) {
    fail("browser_audio_taskkill_boundary_invalid");
  }
  const taskkill = spawn(
    taskkillPath,
    ["/PID", String(processId), "/T", "/F"],
    { stdio: "ignore", windowsHide: true },
  );
  if (!(await waitForProcessExit(taskkill, 5_000))) {
    taskkill.kill();
    await waitForProcessExit(taskkill, 1_000);
  }
  if (
    chromeProcess.exitCode === null &&
    chromeProcess.signalCode === null
  ) {
    chromeProcess.kill();
  }
  await waitForProcessExit(chromeProcess, 2_000);
}

async function removeProfileDirectory(profileDirectory) {
  const resolvedProfile = path.resolve(profileDirectory);
  const resolvedTemporaryRoot = path.resolve(tmpdir());
  if (
    path.dirname(resolvedProfile) !== resolvedTemporaryRoot ||
    !path.basename(resolvedProfile).startsWith(PROFILE_CLEANUP_PREFIX)
  ) {
    fail("browser_audio_profile_cleanup_boundary_invalid");
  }
  const retryableCodes = new Set(["EACCES", "EBUSY", "ENOTEMPTY", "EPERM"]);
  for (let attempt = 0; attempt < PROFILE_CLEANUP_ATTEMPTS; attempt += 1) {
    try {
      const metadata = await lstat(resolvedProfile).catch((error) => {
        if (error?.code === "ENOENT") return null;
        throw error;
      });
      if (metadata === null) return;
      if (!metadata.isDirectory() || metadata.isSymbolicLink()) {
        fail("browser_audio_profile_cleanup_boundary_invalid");
      }
      await rm(resolvedProfile, { force: true, recursive: true });
      return;
    } catch (error) {
      if (error?.message === "browser_audio_profile_cleanup_boundary_invalid") {
        throw error;
      }
      const retryable = retryableCodes.has(error?.code);
      if (!retryable || attempt + 1 === PROFILE_CLEANUP_ATTEMPTS) {
        fail("browser_audio_profile_cleanup_failed");
      }
      await delay(
        PROFILE_CLEANUP_RETRY_MS * Math.min(attempt + 1, 4),
      );
    }
  }
  fail("browser_audio_profile_cleanup_failed");
}

async function main() {
  const options = parseArguments(process.argv.slice(2));
  const chromePath = await findChrome(options.chromePath);
  const { provenance, snapshots } = await validateArtifacts(
    options.distPath,
    options.expectedCommit,
    options.expectedManifestSha256,
  );
  const serverState = await startServer(snapshots);
  const profileDirectory = await mkdtemp(
    path.join(tmpdir(), "kotae-browser-audio-"),
  );
  let chromeProcess;
  let cleanupError;
  let cdp;
  let passedOutput;
  let primaryError;
  let targetId;
  try {
    chromeProcess = spawn(
      chromePath,
      [
        "--headless=new",
        "--autoplay-policy=no-user-gesture-required",
        "--disable-background-networking",
        "--disable-component-update",
        "--disable-default-apps",
        "--disable-extensions",
        "--disable-gpu",
        "--disable-sync",
        "--metrics-recording-only",
        "--no-default-browser-check",
        "--no-first-run",
        // Windows CI/job-object isolation can make Chromium's nested sandbox
        // terminate its GPU process before CDP attaches. This fixture serves
        // only hash-verified release bytes over loopback; Linux keeps the
        // browser sandbox enabled.
        ...(process.platform === "win32" ? ["--no-sandbox"] : []),
        "--remote-debugging-address=127.0.0.1",
        "--remote-debugging-port=0",
        `--user-data-dir=${profileDirectory}`,
        "--window-size=800,600",
        "about:blank",
      ],
      {
        stdio: ["ignore", "ignore", "pipe"],
        windowsHide: true,
      },
    );
    chromeProcess.stderr?.on("data", (chunk) => {
      chromeDiagnostic = `${chromeDiagnostic}${String(chunk)}`.slice(-8_192);
    });
    const endpoint = await readDevToolsEndpoint(
      profileDirectory,
      chromeProcess,
    );
    cdp = await connectCdp(endpoint);
    const created = await cdp.send("Target.createTarget", { url: "about:blank" });
    targetId = created.targetId;
    if (typeof targetId !== "string" || targetId.length === 0) {
      fail("browser_audio_target_create_failed");
    }
    const attached = await cdp.send("Target.attachToTarget", {
      flatten: true,
      targetId,
    });
    const sessionId = attached.sessionId;
    if (typeof sessionId !== "string" || sessionId.length === 0) {
      fail("browser_audio_target_attach_failed");
    }
    await cdp.send("Runtime.enable", {}, sessionId);
    await cdp.send("Page.enable", {}, sessionId);
    await cdp.send("Page.navigate", { url: serverState.url }, sessionId);
    const result = validateBrowserResult(
      await waitForBrowserResult(cdp, sessionId),
    );
    passedOutput = JSON.stringify({
      status: "passed",
      provenance: provenance.mode,
      sourceCommit: provenance.sourceCommit,
      manifestSha256: provenance.manifestSha256,
      sampleRateHz: result.sampleRateHz,
      zeroOutputCapture: result.zeroOutputCapture,
      directWasmGenerationIsolation:
        result.directWasmGenerationIsolation,
      intentionalFastLaneValidated: result.intentionalFastLaneValidated,
      observationAddingValidated: result.observationAddingValidated,
      acousticExchangeabilityValidated:
        result.acousticExchangeabilityValidated,
      acousticCoverageWireValidated: result.acousticCoverageWireValidated,
      guestAFirstSprintSloValidated: result.guestAFirstSprintSloValidated,
      guestQuietOnsetValidated: result.guestQuietOnsetValidated,
      quietSubbandEvidenceValidated: result.quietSubbandEvidenceValidated,
      quietSpectralCompensationValidated:
        result.quietSpectralCompensationValidated,
      temporalVadClockValidated: result.temporalVadClockValidated,
      wrappedFrames: result.wrappedFrames,
      freshGenerationFrames: result.freshGenerationFrames,
      sameContextReuseFrames: result.sameContextReuseFrames,
      sameContextReuseIsolated: result.sameContextReuseIsolated,
      senderDetachGuardPassed: result.senderDetachGuardPassed,
    });
  } catch (error) {
    primaryError = error;
  } finally {
    try {
      if (cdp) {
        if (targetId) {
          await Promise.race([
            cdp.send("Target.closeTarget", { targetId }).catch(() => {}),
            delay(1_000),
          ]);
        }
        if (process.platform !== "win32") {
          await Promise.race([
            cdp.send("Browser.close").catch(() => {}),
            delay(1_000),
          ]);
        }
        try {
          cdp.socket.close();
        } catch {
          // Browser.close may already have closed the debugger transport.
        }
      }
      if (process.platform === "win32") {
        await terminateChromeProcessTree(chromeProcess);
      } else if (!(await waitForProcessExit(chromeProcess, 5_000))) {
        await terminateChromeProcessTree(chromeProcess);
      }
      await closeServer(serverState.server);
      await removeProfileDirectory(profileDirectory);
    } catch (error) {
      cleanupError = error;
    }
  }
  if (primaryError !== undefined) {
    if (cleanupError !== undefined) {
      const cleanupCode =
        cleanupError instanceof Error &&
        /^[a-z0-9_]{1,160}$/u.test(cleanupError.message)
          ? cleanupError.message
          : "browser_audio_cleanup_failed";
      chromeDiagnostic = `${chromeDiagnostic}\ncleanup=${cleanupCode}`.slice(
        -8_192,
      );
    }
    throw primaryError;
  }
  if (cleanupError !== undefined) {
    throw cleanupError;
  }
  if (typeof passedOutput !== "string") {
    fail("browser_audio_result_missing");
  }
  process.stdout.write(`${passedOutput}\n`);
}

main().catch((error) => {
  const code =
    error instanceof Error && /^[a-z0-9_]{1,160}$/u.test(error.message)
      ? error.message
      : "browser_audio_gate_failed";
  process.stderr.write(`${code}\n`);
  if (process.env.KOTAE_BROWSER_AUDIO_DEBUG === "1" && error instanceof Error) {
    process.stderr.write(`${String(error.stack ?? error.message).slice(0, 8_192)}\n`);
  }
  if (process.env.KOTAE_BROWSER_AUDIO_DEBUG === "1" && chromeDiagnostic) {
    process.stderr.write(chromeDiagnostic);
  }
  process.exitCode = 1;
});
