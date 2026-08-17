import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../../", import.meta.url);

test("published PCM ownership is a dedicated finite Rust/Wasm AudioWorklet boundary", async () => {
  const [
    rust,
    runtime,
    worklet,
    bridge,
    build,
    deploy,
    documentation,
    browserRunner,
  ] = await Promise.all([
    readFile(new URL("crates/pcm_ring/src/lib.rs", root), "utf8"),
    readFile(
      new URL("apps/client/web/pcm-ring-worklet-runtime.js", root),
      "utf8",
    ),
    readFile(
      new URL("apps/client/web/pcm-capture-worklet.js", root),
      "utf8",
    ),
    readFile(new URL("apps/client/web/firebase-bridge.js", root), "utf8"),
    readFile(new URL("scripts/build-web.ps1", root), "utf8"),
    readFile(new URL("scripts/deploy-hosting.ps1", root), "utf8"),
    readFile(new URL("docs/pcm-ring-boundary.md", root), "utf8"),
    readFile(new URL("scripts/test-browser-audio.mjs", root), "utf8"),
  ]);

  assert.match(rust, /const FRAME_BYTES: usize = 640/u);
  assert.match(rust, /const MAXIMUM_CAPACITY: usize = 10_500/u);
  assert.match(rust, /pcm: \[u8; FRAME_BYTES\]/u);
  assert.match(rust, /slots: Vec<Slot>/u);
  assert.match(rust, /generation: u64/u);
  assert.match(rust, /generation != self\.generation/u);
  assert.match(rust, /inner: Option<PcmRing>/u);
  assert.match(rust, /self\.slots\.iter_mut\(\)\.for_each\(Slot::wipe\)/u);
  assert.match(rust, /context_frame <= previous/u);
  assert.match(rust, /pub fn compensate_quiet_frame/u);
  assert.match(rust, /const QUIET_PRE_EMPHASIS: f64 = 0\.28/u);
  assert.match(rust, /const DC_BLOCK_POLE: f64 = 0\.995/u);
  assert.match(rust, /filtered\.zeroize\(\)/u);
  assert.doesNotMatch(rust, /Vec<Option<Entry>>|Box<\[u8; FRAME_BYTES\]>/u);
  assert.doesNotMatch(rust, /JsValue::from_str|Result<WasmPcmRing/u);

  assert.match(runtime, /from "\/wasm\/kotae_pcm_ring\.js"/u);
  assert.match(runtime, /initSync\(\{ module \}\)/u);
  assert.match(runtime, /module instanceof WebAssembly\.Module/u);
  assert.match(runtime, /const MAXIMUM_CAPACITY = 10_500/u);
  assert.match(runtime, /capacity > MAXIMUM_CAPACITY/u);
  assert.match(runtime, /export function createPcmRing/u);
  assert.match(runtime, /verifyGenerationIsolation\(ring, generation\)/u);
  assert.match(runtime, /ring\.push\(staleGeneration, 0, probe\)/u);
  assert.match(runtime, /ring\.compensateQuietFrame\([\s\S]*staleGeneration/u);
  assert.match(runtime, /ring\.quietPhaseIntegrity\(staleGeneration\)/u);
  assert.match(runtime, /observationAddingSelfTest\(\)\s*!==\s*true/u);
  assert.match(runtime, /turnReferenceBoundarySelfTest\(\)\s*!==\s*true/u);
  assert.match(runtime, /ring\.clear\(generation\)/u);

  assert.match(
    worklet,
    /import \{ createPcmRing \} from "\.\/pcm-ring-worklet-runtime\.js";/u,
  );
  assert.match(worklet, /pcmRingModule/u);
  assert.match(worklet, /this\.preConfirmRing = createPcmRing/u);
  assert.match(worklet, /this\.confirmedQueue = createPcmRing/u);
  assert.match(
    worklet,
    /this\.confirmedQueue\.compensateQuietFrame\([\s\S]*new Uint8Array\(entry\.pcm\)/u,
  );
  assert.match(
    worklet,
    /this\.confirmedQueue\.quietPhaseIntegrity\(\s*this\.generation/u,
  );
  assert.match(worklet, /control\.quietConfirmed && control\.aecVerified/u);
  assert.match(bridge, /aecVerified: hasVerifiedEchoCancellation\(stream\)/u);
  assert.doesNotMatch(worklet, /QUIET_GAIN_TARGET_RMS|sample \* applied/u);
  assert.match(worklet, /this\.releaseRings\(\)/u);
  assert.match(worklet, /completed\.byteLength !== 0/u);
  assert.match(worklet, /this\.postError\("capture_invalid"\)/u);
  assert.match(worklet, /registerProcessor\("kotae-pcm-capture"/u);
  assert.doesNotMatch(worklet, /preConfirmRing = new Array/u);
  assert.doesNotMatch(worklet, /confirmedQueue = new Array/u);

  assert.match(bridge, /WebAssembly\.compile\(bytes\)/u);
  assert.match(bridge, /PCM_RING_WASM_MAX_BYTES = 256 \* 1024/u);
  assert.match(bridge, /PCM_RING_FETCH_TIMEOUT_MS = 3_000/u);
  assert.match(bridge, /PCM_CAPTURE_WORKLET_LOAD_TIMEOUT_MS = 3_500/u);
  assert.match(bridge, /signal: controller\.signal/u);
  assert.match(bridge, /load = boundedPcmWorkletLoad\(pending\)/u);
  assert.match(
    bridge,
    /loadPcmRingModule\(\),[\s\S]*addModule\(PCM_CAPTURE_WORKLET_URL\)/u,
  );
  assert.doesNotMatch(bridge, /PCM_RING_WORKLET_RUNTIME_URL/u);
  assert.equal(
    (bridge.match(/maximumQueuedFrames:[\s\S]{0,120}pcmRingModule/g) ?? [])
      .length,
    2,
  );

  for (const releasePolicy of [build, deploy]) {
    for (const artifact of [
      "pcm-capture-worklet.js",
      "wasm/kotae_pcm_ring_bg.wasm",
    ]) {
      assert.ok(
        releasePolicy.replaceAll("\\", "/").includes(`\"${artifact}\"`),
        `release allowlist is missing ${artifact}`,
      );
    }
  }
  assert.doesNotMatch(deploy, /"pcm-ring-worklet-runtime\.js"/u);
  assert.doesNotMatch(deploy, /"wasm[\\/]kotae_pcm_ring\.js"/u);
  assert.match(build, /BEGIN audited wasm-bindgen sync glue/u);
  for (const fragment of [
    "generatedThrowHandler",
    "contentFreeThrowHandler",
    "generatedStringReader",
    "generatedTextDecoder",
  ]) {
    assert.ok(
      build.includes(`$${fragment} = ConvertTo-Lf -Text @'`),
      `${fragment} must ignore checkout line endings`,
    );
  }
  assert.match(build, /PCM Worklet bundle is not a single reviewed synchronous module/u);
  assert.match(browserRunner, /const PROFILE_CLEANUP_ATTEMPTS = 12/u);
  assert.match(browserRunner, /new Set\(\["EACCES", "EBUSY", "ENOTEMPTY", "EPERM"\]\)/u);
  assert.match(browserRunner, /await removeProfileDirectory\(profileDirectory\)/u);
  assert.match(
    browserRunner,
    /browser_audio_profile_cleanup_boundary_invalid/u,
  );
  assert.match(browserRunner, /"--expected-manifest-sha256"/u);
  assert.match(browserRunner, /manifestSha256 !== expectedManifestSha256/u);
  assert.match(browserRunner, /\["\/PID", String\(processId\), "\/T", "\/F"\]/u);
  assert.match(browserRunner, /child\.signalCode !== null/u);
  assert.match(browserRunner, /sameContextReuseFrames/u);
  assert.match(browserRunner, /directWasmGenerationIsolation/u);
  assert.match(browserRunner, /if \(primaryError !== undefined\)/u);
  assert.match(browserRunner, /process\.stdout\.write\(`\$\{passedOutput\}\\n`\)/u);
  assert.match(build, /--package kotae-pcm-ring/u);
  assert.match(documentation, /constructor.*一度確保/u);
  assert.match(documentation, /push、eviction、shiftではheap allocationしません/u);
});
