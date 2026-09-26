import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("response transport starts without waiting for AudioContext resume", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const start = bridge.indexOf("async function finishTurn(");
  const end = bridge.indexOf("function safeDocumentName", start);
  assert.ok(start >= 0 && end > start);
  const finish = bridge.slice(start, end);

  assert.doesNotMatch(
    finish,
    /await awaitVoiceTurnResult\(audioContext\.resume\(\)\)/u,
  );

  const livePlayback = finish.indexOf("playback = createStreamingPlayback(");
  const liveResume = finish.indexOf("responseAudioContext.resume()", livePlayback);
  const liveCommit = finish.indexOf("liveSession.commit(playback", liveResume);
  const liveJoin = finish.indexOf("Promise.all([", liveResume);
  assert.ok(livePlayback >= 0);
  assert.ok(liveResume > livePlayback);
  assert.ok(liveCommit > liveResume);
  assert.equal(liveJoin, finish.lastIndexOf("Promise.all([", liveCommit));

  const httpPlayback = finish.indexOf(
    "playback = createStreamingPlayback(",
    livePlayback + 1,
  );
  const httpResume = finish.indexOf("responseAudioContext.resume()", httpPlayback);
  const httpFetch = finish.indexOf("const responsePromise = fetch(", httpResume);
  const httpJoin = finish.indexOf("Promise.all([responsePromise, resumeAudioPromise])");
  assert.ok(httpPlayback > livePlayback);
  assert.ok(httpResume > httpPlayback);
  assert.ok(httpFetch > httpResume);
  assert.ok(httpJoin > httpFetch);
  assert.match(finish, /catch \(error\) \{[\s\S]*requestController\?\.abort\(\);/u);
});
