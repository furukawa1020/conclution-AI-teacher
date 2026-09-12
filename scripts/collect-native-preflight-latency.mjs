import { randomUUID } from "node:crypto";
import { access, mkdir, rename, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { createNativePreflightBrowserCollector } from "./native-preflight-browser-collector-policy.mjs";
import {
  evaluateNativePreflightBenchmark,
  NATIVE_PREFLIGHT_BENCHMARK_VERSION,
} from "./native-preflight-latency-gate.mjs";

const PRODUCTION_ORIGIN = "https://kotae-ai.web.app";
const EVIDENCE_DIRECTORY = ".release-evidence";
const BINDING_NAME = "__kotaeNativePreflightBenchmarkEvent";
const NETWORK_CONDITIONS = new Set(["ethernet", "unshaped", "wifi", "4g-emulated"]);
const REQUIRED_OPTIONS = Object.freeze([
  "cost-micro-usd-per-unused-ms",
  "debugging-port",
  "device-model",
  "network-condition",
  "operating-system",
  "output",
  "runtime-revision",
  "source-commit",
]);
const OPTIONAL_OPTIONS = new Set(["timeout-ms"]);

function fail(code) {
  throw new TypeError(code);
}

function boundedText(value) {
  return typeof value === "string" && value.length > 0 && value.length <= 128 &&
    value.trim() === value && !/[\r\n\u0000]/u.test(value);
}

export function parseCollectorArguments(argv) {
  const values = new Map();
  for (let index = 0; index < argv.length; index += 2) {
    const token = argv[index];
    const value = argv[index + 1];
    if (!/^--[a-z0-9-]+$/u.test(token ?? "") || value === undefined || value.startsWith("--")) {
      fail("native_preflight_collector_arguments_invalid");
    }
    const key = token.slice(2);
    if ((!REQUIRED_OPTIONS.includes(key) && !OPTIONAL_OPTIONS.has(key)) || values.has(key)) {
      fail("native_preflight_collector_arguments_invalid");
    }
    values.set(key, value);
  }
  if (!REQUIRED_OPTIONS.every((key) => values.has(key))) {
    fail("native_preflight_collector_arguments_invalid");
  }
  const debuggingPort = Number(values.get("debugging-port"));
  const timeoutMs = Number(values.get("timeout-ms") ?? 3_600_000);
  const costMicroUsdPerUnusedMs = Number(values.get("cost-micro-usd-per-unused-ms"));
  const sourceCommit = values.get("source-commit");
  const runtimeRevision = values.get("runtime-revision");
  const networkCondition = values.get("network-condition");
  const deviceModel = values.get("device-model");
  const operatingSystem = values.get("operating-system");
  if (!Number.isSafeInteger(debuggingPort) || debuggingPort < 1 || debuggingPort > 65_535 ||
      !Number.isSafeInteger(timeoutMs) || timeoutMs < 60_000 || timeoutMs > 3_600_000 ||
      !Number.isFinite(costMicroUsdPerUnusedMs) || costMicroUsdPerUnusedMs < 0 ||
      !/^[0-9a-f]{40}$/u.test(sourceCommit) ||
      !/^kotae-api-[a-z0-9-]{1,52}$/u.test(runtimeRevision) ||
      !NETWORK_CONDITIONS.has(networkCondition) ||
      !boundedText(deviceModel) || !boundedText(operatingSystem)) {
    fail("native_preflight_collector_arguments_invalid");
  }
  return Object.freeze({
    costMicroUsdPerUnusedMs,
    debuggingPort,
    deviceModel,
    networkCondition,
    operatingSystem,
    output: values.get("output"),
    runtimeRevision,
    sourceCommit,
    timeoutMs,
  });
}

export function resolveEvidencePaths(workspace, output) {
  if (typeof workspace !== "string" || !boundedText(output) || path.isAbsolute(output) ||
      path.extname(output).toLowerCase() !== ".json") {
    fail("native_preflight_collector_output_invalid");
  }
  const evidenceRoot = path.resolve(workspace, EVIDENCE_DIRECTORY);
  const rawPath = path.resolve(evidenceRoot, output);
  const relative = path.relative(evidenceRoot, rawPath);
  if (relative === "" || relative.startsWith("..") || path.isAbsolute(relative)) {
    fail("native_preflight_collector_output_invalid");
  }
  const resultPath = rawPath.slice(0, -5) + ".result.json";
  return Object.freeze({ evidenceRoot, rawPath, resultPath });
}

export function selectProductionTarget(targets, debuggingPort) {
  if (!Array.isArray(targets) || !Number.isSafeInteger(debuggingPort)) {
    fail("native_preflight_collector_target_invalid");
  }
  const endpointPattern = new RegExp(
    `^ws://127\\.0\\.0\\.1:${debuggingPort}/devtools/page/[A-Za-z0-9-]+$`,
    "u",
  );
  const matches = targets.filter((target) => {
    try {
      return target?.type === "page" && new URL(target.url).origin === PRODUCTION_ORIGIN &&
        typeof target.webSocketDebuggerUrl === "string" &&
        endpointPattern.test(target.webSocketDebuggerUrl);
    } catch {
      return false;
    }
  });
  if (matches.length !== 1) fail("native_preflight_collector_target_invalid");
  return matches[0];
}

export function buildRawBenchmark({ browserVersion, options, runs, measuredAt }) {
  if (!Array.isArray(runs) || runs.length !== 100) fail("native_preflight_collector_runs_incomplete");
  const shared = (run) => ({
    estimatedCostMicroUsd: run.estimatedCostMicroUsd,
    fallback: run.fallback,
    index: run.index,
    unusedConnectionMs: run.unusedConnectionMs,
  });
  return {
    coldRuns: runs.map((run) => ({ ...shared(run), latencyMs: run.coldMs })),
    environment: {
      browser: "Google Chrome",
      browserVersion,
      cloudRegion: "asia-northeast1",
      deviceModel: options.deviceModel,
      measuredAt,
      networkCondition: options.networkCondition,
      operatingSystem: options.operatingSystem,
      providerRegion: "us-central1",
      runtimeRevision: options.runtimeRevision,
    },
    schemaVersion: NATIVE_PREFLIGHT_BENCHMARK_VERSION,
    sourceCommit: options.sourceCommit,
    warmRuns: runs.map((run) => ({
      ...shared(run),
      latencyMs: run.warmMs,
      speechEndConnectionWaitMs: run.speechEndConnectionWaitMs,
    })),
  };
}

class CdpClient {
  constructor(socket) {
    this.socket = socket;
    this.nextId = 1;
    this.pending = new Map();
    this.listeners = new Set();
    socket.addEventListener("message", (event) => {
      let message;
      try { message = JSON.parse(String(event.data)); } catch { return; }
      if (message.method === "Runtime.bindingCalled") {
        for (const listener of this.listeners) listener(message.params);
      }
      if (!Number.isSafeInteger(message.id)) return;
      const pending = this.pending.get(message.id);
      if (!pending) return;
      this.pending.delete(message.id);
      if (message.error) pending.reject(new Error("native_preflight_collector_cdp_command_failed"));
      else pending.resolve(message.result ?? {});
    });
    socket.addEventListener("close", () => {
      for (const pending of this.pending.values()) pending.reject(new Error("native_preflight_collector_cdp_closed"));
      this.pending.clear();
    });
  }

  onBinding(listener) { this.listeners.add(listener); }
  send(method, params = {}) {
    const id = this.nextId++;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { reject, resolve });
      this.socket.send(JSON.stringify({ id, method, params }));
    });
  }
  close() { this.socket.close(); }
}

async function connect(endpoint) {
  const socket = new WebSocket(endpoint);
  await Promise.race([
    new Promise((resolve, reject) => {
      socket.addEventListener("open", resolve, { once: true });
      socket.addEventListener("error", () => reject(new Error("native_preflight_collector_cdp_connect_failed")), { once: true });
    }),
    new Promise((_, reject) => setTimeout(() => reject(new Error("native_preflight_collector_cdp_connect_timeout")), 5_000)),
  ]);
  return new CdpClient(socket);
}

const INSTALL_EXPRESSION = `(() => {
  if (Object.prototype.hasOwnProperty.call(globalThis, "__KOTAE_NATIVE_PREFLIGHT_BENCHMARK__")) throw new Error("collector_already_installed");
  const emit = (type, event) => globalThis.${BINDING_NAME}(JSON.stringify({ type, detail: event.detail }));
  const latency = (event) => emit("latency", event);
  const prepare = (event) => emit("prepare", event);
  addEventListener("kotae:native-preflight-latency", latency);
  addEventListener("kotae:voice-prepare-slo", prepare);
  Object.defineProperty(globalThis, "__KOTAE_NATIVE_PREFLIGHT_BENCHMARK__", { configurable: true, value: Object.freeze({ cleanup() {
    removeEventListener("kotae:native-preflight-latency", latency);
    removeEventListener("kotae:voice-prepare-slo", prepare);
    delete globalThis.__KOTAE_NATIVE_PREFLIGHT_BENCHMARK__;
  } }) });
  return true;
})()`;

async function assertReleaseCommit(sourceCommit) {
  const response = await fetch(`${PRODUCTION_ORIGIN}/kotae-release-manifest.json`, { cache: "no-store", redirect: "error" });
  if (!response.ok) fail("native_preflight_collector_release_manifest_invalid");
  const manifest = await response.json();
  if (manifest?.sourceCommit !== sourceCommit) fail("native_preflight_collector_release_commit_mismatch");
}

async function writeArtifacts(paths, raw, result) {
  await mkdir(paths.evidenceRoot, { recursive: true });
  for (const destination of [paths.rawPath, paths.resultPath]) {
    try { await access(destination); fail("native_preflight_collector_output_exists"); } catch (error) {
      if (error?.code !== "ENOENT") throw error;
    }
  }
  const rawTemporary = `${paths.rawPath}.${randomUUID()}.tmp`;
  const resultTemporary = `${paths.resultPath}.${randomUUID()}.tmp`;
  await mkdir(path.dirname(paths.rawPath), { recursive: true });
  await writeFile(rawTemporary, `${JSON.stringify(raw, null, 2)}\n`, { encoding: "utf8", flag: "wx" });
  await writeFile(resultTemporary, `${JSON.stringify(result, null, 2)}\n`, { encoding: "utf8", flag: "wx" });
  await rename(rawTemporary, paths.rawPath);
  await rename(resultTemporary, paths.resultPath);
}

export async function collectNativePreflightBenchmark(options, { workspace = process.cwd() } = {}) {
  const paths = resolveEvidencePaths(workspace, options.output);
  await assertReleaseCommit(options.sourceCommit);
  const base = `http://127.0.0.1:${options.debuggingPort}`;
  const [targetsResponse, versionResponse] = await Promise.all([
    fetch(`${base}/json/list`, { redirect: "error" }),
    fetch(`${base}/json/version`, { redirect: "error" }),
  ]);
  if (!targetsResponse.ok || !versionResponse.ok) fail("native_preflight_collector_devtools_invalid");
  const target = selectProductionTarget(await targetsResponse.json(), options.debuggingPort);
  const version = await versionResponse.json();
  const match = /^Chrome\/(\d+\.\d+\.\d+\.\d+)$/u.exec(version?.Browser ?? "");
  if (!match) fail("native_preflight_collector_browser_invalid");
  const client = await connect(target.webSocketDebuggerUrl);
  const collector = createNativePreflightBrowserCollector({
    costMicroUsdPerUnusedMs: options.costMicroUsdPerUnusedMs,
    requiredRuns: 100,
  });
  let complete;
  const completion = new Promise((resolve, reject) => { complete = { reject, resolve }; });
  client.onBinding((params) => {
    if (params?.name !== BINDING_NAME || typeof params.payload !== "string") return;
    try {
      const event = JSON.parse(params.payload);
      if (Object.keys(event).sort().join("\u0000") !== "detail\u0000type") fail("native_preflight_collector_binding_invalid");
      if (event.type === "latency") collector.acceptLatency(event.detail);
      else if (event.type === "prepare") {
        const finished = collector.acceptPrepare(event.detail);
        const count = collector.snapshot().runs.length;
        if (count === 1 || count % 10 === 0) process.stderr.write(`native-preflight progress ${count}/100\n`);
        if (finished) complete.resolve();
      }
      else fail("native_preflight_collector_binding_invalid");
    } catch (error) { complete.reject(error); }
  });
  let timeout;
  try {
    await client.send("Runtime.enable");
    await client.send("Runtime.addBinding", { name: BINDING_NAME });
    const installed = await client.send("Runtime.evaluate", { expression: INSTALL_EXPRESSION, returnByValue: true });
    if (installed.exceptionDetails || installed.result?.value !== true) fail("native_preflight_collector_install_failed");
    const timedOut = new Promise((_, reject) => {
      timeout = setTimeout(() => reject(new Error("native_preflight_collector_timeout")), options.timeoutMs);
    });
    await Promise.race([completion, timedOut]);
    const raw = buildRawBenchmark({
      browserVersion: match[1],
      measuredAt: new Date().toISOString(),
      options,
      runs: collector.snapshot().runs,
    });
    const result = evaluateNativePreflightBenchmark(raw);
    await writeArtifacts(paths, raw, result);
    return Object.freeze({ paths, result });
  } finally {
    clearTimeout(timeout);
    await client.send("Runtime.evaluate", { expression: "globalThis.__KOTAE_NATIVE_PREFLIGHT_BENCHMARK__?.cleanup()" }).catch(() => {});
    await client.send("Runtime.removeBinding", { name: BINDING_NAME }).catch(() => {});
    client.close();
  }
}

async function main() {
  const options = parseCollectorArguments(process.argv.slice(2));
  const collected = await collectNativePreflightBenchmark(options);
  process.stdout.write(`${JSON.stringify({ passed: collected.result.passed, failureCodes: collected.result.failureCodes, rawPath: collected.paths.rawPath, resultPath: collected.paths.resultPath })}\n`);
  if (!collected.result.passed) process.exitCode = 1;
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  main().catch((error) => {
    process.stderr.write(`${error?.message ?? "native_preflight_collector_failed"}\n`);
    process.exitCode = 1;
  });
}
