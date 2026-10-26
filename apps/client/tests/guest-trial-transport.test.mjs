import assert from "node:assert/strict";
import test from "node:test";
import { createGuestTrialTransport, createGuestTrialVoiceTransport } from "../web/guest-trial-controller.mjs";

const origin = "https://voice.example";
const endpoint = `${origin}/api/v1/guest-trials/control`;
const now = Date.parse("2026-10-20T00:00:00Z");
const snapshot = { version: 1, id: "a".repeat(64), questionId: "rest_activity_v1",
  question: "少し休めるとしたら、何をしたいですか？", generation: 1, assisted: false,
  status: "pending", expiresAt: "2026-10-20T00:15:00Z" };
const credentials = async () => ({ idToken: "id.secret", appCheckToken: "app.secret" });
function response(body = JSON.stringify(snapshot), status = 200) {
  const result = new Response(body, { status, headers: {"Content-Type": "application/json"} });
  Object.defineProperty(result, "url", { value: endpoint });
  return result;
}
function transport(overrides = {}) {
  return createGuestTrialTransport({ origin, credentials, isCurrentGuest: () => true,
    getSessionEpoch:()=>1,
    now: () => now, request: async () => response(), ...overrides });
}
function options() { return {signal: new AbortController().signal}; }

function voiceResponse(overrides = {}) {
  const result = new Response(JSON.stringify({...snapshot, status:"answer_first", generation:2, ...overrides}),
    {headers:{"Content-Type":"application/json"}});
  Object.defineProperty(result,"url",{value:`${origin}/api/v1/guest-trials/voice`});
  return result;
}
function voiceTransport(overrides = {}) {
  return createGuestTrialVoiceTransport({origin, credentials, isCurrentGuest:()=>true,
    getSessionEpoch:()=>1,
    now:()=>now, request:async()=>voiceResponse(), ...overrides});
}
function voiceInput() { return {trial:{...snapshot,state:"v2.sealed"},audio:new Uint8Array([1,2,3,4])}; }

test("native and compatibility PCM encoders preserve every byte and erase the input", async () => {
  for (const native of [true, false]) {
    for (const size of [2, 4, 8192, 960000]) {
      const input = voiceInput();
      input.audio = Uint8Array.from({length: size}, (_, i) => i % 256);
      const expected = Buffer.from(input.audio).toString("base64");
      let nativeCalls = 0;
      input.audio.toBase64 = native ? function() {
        nativeCalls++; return Buffer.from(this).toString("base64");
      } : undefined;
      await voiceTransport({request: async (_, init) => {
        assert.equal(JSON.parse(init.body).pcm16, expected);
        return voiceResponse();
      }})(input, options());
      assert.equal(nativeCalls, Number(native));
      assert.ok(input.audio.every(byte => byte === 0));
    }
  }
});

test("native encoding failure still erases all PCM and never sends", async () => {
  const input = voiceInput();
  input.audio.toBase64 = () => { throw new Error("encoder failed"); };
  await assert.rejects(voiceTransport({request: () => assert.fail("unexpected request")})(input, options()),
    {message: "guest_trial_unavailable"});
  assert.ok(input.audio.every(byte => byte === 0));
});

test("already cancelled or replaced guest never starts PCM encoding or credentials", async () => {
  for (const cancelled of [true, false]) {
    const input = voiceInput();
    input.audio.toBase64 = () => assert.fail("encoding after cancellation");
    const abort = new AbortController();
    if (cancelled) abort.abort();
    await assert.rejects(voiceTransport({isCurrentGuest: () => cancelled,
      credentials: () => assert.fail("credentials after cancellation"),
      request: () => assert.fail("send after cancellation"),
    })(input, {signal: abort.signal}),
    {message: cancelled ? "guest_trial_cancelled" : "guest_trial_session_changed"});
    assert.ok(input.audio.every(byte => byte === 0));
  }
});

test("guest-to-guest replacement cannot send previously acquired credentials",async()=>{
  for(const voice of [false,true]) {
    let epoch=1,release,calls=0;
    const send=(voice?voiceTransport:transport)({getSessionEpoch:()=>epoch,
      credentials:()=>new Promise(resolve=>{release=resolve;}),
      request:async()=>{calls++;return voice?voiceResponse():response();}});
    const pending=send(voice?voiceInput():{action:"begin"},options());
    const rejected=assert.rejects(pending,/session_changed/u);
    epoch=2;release(await credentials());await rejected;assert.equal(calls,0);
  }
});

test("guest replacement discards old response even when guest status stays true",async()=>{
  for(const voice of [false,true]) {
    let epoch=1,release;
    const send=(voice?voiceTransport:transport)({getSessionEpoch:()=>epoch,
      request:()=>new Promise(resolve=>{release=resolve;})});
    const pending=send(voice?voiceInput():{action:"begin"},options());
    const rejected=assert.rejects(pending,/session_changed/u);
    while(!release) await Promise.resolve();
    epoch=2;release(voice?voiceResponse():response());await rejected;
  }
});

test("session epoch is required, finite and not sent in the request",async()=>{
  assert.throws(()=>transport({getSessionEpoch:undefined}),/invalid/u);
  for(const epoch of [null,-1,NaN,Infinity,"uid",Number.MAX_SAFE_INTEGER+1]) {
    let calls=0;
    await assert.rejects(transport({getSessionEpoch:()=>epoch,request:async()=>{calls++;return response();}})({action:"begin"},options()),/invalid/u);
    assert.equal(calls,0);
  }
  await transport({getSessionEpoch:()=>42,request:async(_,init)=>{
    assert.equal(init.body,'{"action":"begin"}');
    assert.ok(!Object.keys(init.headers).some(x=>/epoch|session/iu.test(x)));
    return response();
  }})({action:"begin"},options());
});

test("dedicated voice uses guest credentials, consumes PCM and returns only bound completion",async()=>{
  const input=voiceInput();let calls=0;
  const send=voiceTransport({request:async(url,init)=>{
    calls++;
    assert.equal(url,`${origin}/api/v1/guest-trials/voice`);
    assert.equal(init.headers.Authorization,"Bearer id.secret");
    assert.equal(init.headers["X-Firebase-AppCheck"],"app.secret");
    assert.equal(init.redirect,"error");assert.equal(init.credentials,"omit");
    assert.deepEqual(JSON.parse(init.body),{id:snapshot.id,generation:1,state:"v2.sealed",pcm16:"AQIDBA=="});
    assert.deepEqual([...input.audio],[0,0,0,0]);
    return voiceResponse();
  }});
  assert.equal((await send(input,options())).status,"answer_first");assert.equal(calls,1);
});

test("voice rejects foreign, stale, pending or relabelled results",async()=>{
  for(const bad of [{id:"b".repeat(64)},{status:"pending",generation:1},{status:"stopped"},
    {assisted:true,generation:3},{expiresAt:"2026-10-20T00:14:00Z"}]) {
    await assert.rejects(voiceTransport({request:async()=>voiceResponse(bad)})(voiceInput(),options()),/invalid/u);
  }
});

test("voice rejects invalid input and cancellation before sending; consumes rejected audio",async()=>{
  let calls=0;const send=voiceTransport({request:async()=>{calls++;return voiceResponse();}});
  for(const change of [x=>{delete x.trial.state;},x=>{x.audio=new Uint8Array(3);},
    x=>{x.audio=new Uint8Array(960002);},x=>{x.extra=true;},x=>{x.trial.status="stopped";x.trial.generation=2;}]) {
    const input=voiceInput();change(input);
    await assert.rejects(send(input,options()),/invalid/u);
    assert.ok(input.audio.every(x=>x===0));
  }
  const aborted=new AbortController();aborted.abort();
  await assert.rejects(send(voiceInput(),{signal:aborted.signal}),/cancelled/u);
  assert.equal(calls,0);
});

test("voice failure never replays PCM or routes to normal dialogue",async()=>{
  let calls=0;
  const send=voiceTransport({request:async()=>{calls++;throw new Error("private transport URL");}});
  const input=voiceInput();
  await assert.rejects(send(input,options()),/^Error: guest_trial_unavailable$/u);
  assert.equal(calls,1);assert.ok(input.audio.every(x=>x===0));
});

test("control and its voice capability arrive in one authenticated response", async () => {
  const state = "v2." + "a".repeat(3000);
  let calls = 0;
  const send = transport({request:async () => {calls++; return response(JSON.stringify({...snapshot, state}));}});
  assert.equal((await send({action:"begin"}, options())).state, state);
  assert.equal(calls, 1);
});

test("trial transport sends one bounded authenticated POST without redirects or cookies", async () => {
  let calls = 0;
  const send = transport({ request: async (url, init) => {
    calls++;
    assert.equal(url, endpoint);
    assert.equal(init.headers.Authorization, "Bearer id.secret");
    assert.equal(init.headers["X-Firebase-AppCheck"], "app.secret");
    assert.equal(init.credentials, "omit");
    assert.equal(init.redirect, "error");
    assert.equal(init.cache, "no-store");
    assert.equal(init.mode, "cors");
    assert.equal(init.referrerPolicy, "no-referrer");
    assert.equal(init.body, '{"action":"begin"}');
    return response();
  }});
  assert.deepEqual(await send({action:"begin"}, options()), snapshot);
  assert.equal(calls, 1);
});

test("expired authorization and control conflicts do not retry or disclose response contents", async () => {
  for (const status of [401, 403, 409, 410, 429, 503]) {
    let calls = 0;
    const send = transport({request: async () => { calls++; return response("private detail", status); }});
    await assert.rejects(send({action:"begin"}, options()), error =>
      error.message.startsWith("guest_trial_") && !error.message.includes("private"));
    assert.equal(calls, 1);
  }
});

test("wrong destinations, completion claims and invalid credentials fail before network", async () => {
  for (const destination of ["http://voice.example", `${origin}/`, `${origin}/path`, `${origin}?x`, "https://user:secret@voice.example"]) {
    assert.throws(() => transport({origin:destination}), /invalid/u);
  }
  let calls = 0;
  const request = async () => {calls++; return response();};
  for (const payload of [{action:"complete"}, {action:"begin", id:"a".repeat(64)}, {action:"help", id:"a".repeat(64), generation:3}]) {
    await assert.rejects(transport({request})(payload, options()), /invalid/u);
  }
  for (const token of ["", "secret\n", "é", "x".repeat(16385)]) {
    await assert.rejects(transport({request, credentials:async () => ({idToken:token, appCheckToken:"app"})})({action:"begin"}, options()), /authentication_failed/u);
  }
  assert.equal(calls, 0);
});

test("cancellation or account change while acquiring credentials cannot send a late mutation", async () => {
  for (const mode of ["cancel", "account"]) {
    let release;
    let current = true;
    let calls = 0;
    const controller = new AbortController();
    const send = transport({
      credentials: () => new Promise(resolve => {release = resolve;}),
      isCurrentGuest: () => current,
      request:async () => {calls++; return response();},
    });
    const task = send({action:"begin"}, {signal:controller.signal});
    const rejected = assert.rejects(task, /cancelled|session_changed/u);
    if (mode === "cancel") controller.abort(); else current = false;
    release(await credentials());
    await rejected;
    await Promise.resolve();
    assert.equal(calls, 0);
  }
});

test("response byte limit cancels streamed bodies without buffering arbitrary content", async () => {
  let cancelled = false;
  const body = new ReadableStream({
    start(c) { c.enqueue(new Uint8Array(2048 + 16384 + 1)); },
    cancel() { cancelled = true; },
  });
  await assert.rejects(transport({request:async () => response(body)})({action:"begin"}, options()), /invalid/u);
  assert.equal(cancelled, true);
});

test("late fetch response is disposed after cancellation instead of restoring the trial", async () => {
  let deliver;
  let cancelled = false;
  const controller = new AbortController();
  const send = transport({ request: () => new Promise(resolve => {deliver = resolve;}) });
  const pending = send({action:"begin"}, {signal:controller.signal});
  const rejected = assert.rejects(pending, /cancelled/u);
  while (!deliver) await Promise.resolve();
  controller.abort();
  await rejected;
  deliver(response(new ReadableStream({cancel() {cancelled = true;}})));
  await Promise.resolve(); await Promise.resolve();
  assert.equal(cancelled, true);
});

test("stalled credentials and stalled response body time out; no mutation retry", async () => {
  await assert.rejects(transport({timeoutMs:5, credentials:() => new Promise(() => {})})({action:"begin"}, options()), /cancelled/u);
  let cancelled = false;
  const body = new ReadableStream({cancel() {cancelled = true;}});
  await assert.rejects(transport({timeoutMs:5, request:async () => response(body)})({action:"begin"}, options()), /cancelled/u);
  assert.equal(cancelled, true);
});

test("malformed bodies and fetch exceptions become content-free errors", async () => {
  for (const body of ["private body", JSON.stringify({...snapshot, caption:"private"}), new Uint8Array([0xff])]) {
    await assert.rejects(transport({request:async () => response(body)})({action:"begin"}, options()), /^Error: guest_trial_(invalid|unavailable)$/u);
  }
  await assert.rejects(transport({request:async () => {throw new Error("private URL?token=secret");}})({action:"begin"}, options()), /^Error: guest_trial_unavailable$/u);
});
