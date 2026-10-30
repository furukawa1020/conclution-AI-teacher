import assert from "node:assert/strict";
import test from "node:test";
import {parseGuestTrialServerTiming} from "../web/guest-trial-timing.mjs";

test("server timing accepts only bounded fixed stages", () => {
  assert.deepEqual(parseGuestTrialServerTiming("asr;dur=0, verify;dur=120, pipeline;dur=60000"),
    {asr:0, verify:120, pipeline:60000});
  assert.ok(Object.isFrozen(parseGuestTrialServerTiming("asr;dur=1")));
});

test("malformed or identifying timing headers are ignored as a whole", () => {
  for (const value of [null, "", "asr;dur=1,asr;dur=2", "uid;dur=1", "asr;dur=-1",
    "asr;dur=60001", "asr;dur=NaN", "asr;dur=1.2", "asr;dur=01", "asr;dur=1;desc=private",
    "asr;dur=1,", "asr;dur=1".repeat(50)]) assert.equal(parseGuestTrialServerTiming(value), null);
});
