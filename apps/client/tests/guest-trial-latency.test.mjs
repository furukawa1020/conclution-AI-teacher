import assert from "node:assert/strict";
import test from "node:test";
import {createGuestTrialLatency} from "../web/guest-trial-latency.mjs";

test("latency has bounded retention and nearest-rank quantiles, no content", () => {
  const stats = createGuestTrialLatency();
  for(let i=1; i<=120; i++) assert.equal(stats.observe({roundtripMs:i,server:{asr:i}}),true);
  const report=stats.snapshot();
  assert.equal(report.retained,100);
  assert.deepEqual(report.stages.roundtrip,{count:100,p50Ms:70,p95Ms:115});
  assert.deepEqual(report.stages.verify,{count:0,p50Ms:null,p95Ms:null});
  stats.clear(); assert.equal(stats.snapshot().retained,0);
  assert.equal(report.retained,100);
});
test("latency rejects invalid values and extra identifying fields atomically", () => {
  const stats=createGuestTrialLatency();
  for(const value of [{roundtripMs:1,server:null,uid:"secret"}, {roundtripMs:NaN,server:null},
    {roundtripMs:1,server:{id:1}}, {roundtripMs:1,server:{asr:60001}}]) assert.equal(stats.observe(value),false);
  assert.equal(stats.snapshot().retained,0);
});
