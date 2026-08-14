import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../../", import.meta.url);

test("guest A-first SLO is bound to real capture and first-audible boundaries", async () => {
  const [bridge, build, browser, fixture, deploy] = await Promise.all([
    readFile(new URL("apps/client/web/firebase-bridge.js", root), "utf8"),
    readFile(new URL("scripts/build-web.ps1", root), "utf8"),
    readFile(new URL("scripts/test-browser-audio.mjs", root), "utf8"),
    readFile(
      new URL("apps/client/tests/browser/pcm-ring-audio-worklet.fixture.mjs", root),
      "utf8",
    ),
    readFile(new URL("scripts/deploy-hosting.ps1", root), "utf8"),
  ]);

  assert.match(bridge, /from "\.\/guest-a-first-slo-policy\.mjs";/u);
  assert.match(
    bridge,
    /activeRecording = recording;[\s\S]*guestAFirstSprintSlo\.markListening\(performance\.now\(\)\)/u,
  );
  assert.match(
    bridge,
    /function disarmVoiceStartDeadline\(audibleAt\)[\s\S]*markGuestAFirstResponseStarted\(audibleAt\)[\s\S]*markGuestAFirstQuestionEnded\(recording\.lastVoiceAt\)[\s\S]*createStreamingPlayback\(/u,
  );
  assert.match(
    bridge,
    /new CustomEvent\("kotae:guest-a-first-slo",\s*\{[\s\S]*detail: observation/u,
  );
  assert.doesNotMatch(
    bridge.slice(
      bridge.indexOf("function dispatchGuestAFirstSlo"),
      bridge.indexOf("function finalizeGuestAFirstSloResult"),
    ),
    /fetch\(|localStorage|sessionStorage|console\./u,
  );

  for (const source of [build, browser, fixture, deploy]) {
    assert.match(source, /guest-a-first-slo-policy\.mjs/u);
  }
  assert.match(fixture, /validateGuestAFirstSloBatch\(observations\)/u);
  assert.match(fixture, /aiOutputBeforeAnswer: true/u);
  assert.match(browser, /guestAFirstSprintSloValidated/u);
  assert.match(deploy, /\$result\.guestAFirstSprintSloValidated/u);
});

test("guest A-first observation never exposes transcript, raw time, or UID fields", async () => {
  const policy = await readFile(
    new URL("apps/client/web/guest-a-first-slo-policy.mjs", root),
    "utf8",
  );
  const eventBlock = policy.slice(
    policy.indexOf("function immutableEvent"),
    policy.indexOf("export function createGuestAFirstSprintSlo"),
  );
  assert.doesNotMatch(
    eventBlock,
    /transcript|caption|audio|uid|startedAt|observedAt|completionMs/iu,
  );
  assert.match(policy, /events\.length !== 100/u);
  assert.match(policy, /listeningOnTarget >= 95 && responseOnTarget >= 95/u);
});
