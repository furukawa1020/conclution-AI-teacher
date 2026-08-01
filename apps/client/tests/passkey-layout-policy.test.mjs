import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("passkey choices stay in a viewport-fixed first-use panel", async () => {
  const main = await readFile(new URL("../src/main.rs", import.meta.url), "utf8");
  const css = await readFile(new URL("../assets/main.css", import.meta.url), "utf8");

  assert.match(main, /class: "passkey-gate"/u);
  assert.match(main, /role: "dialog"/u);
  assert.match(main, /aria_modal: "true"/u);
  assert.match(main, /aria_labelledby: "passkey-entry-heading"/u);
  assert.match(main, /VoiceState::Error\(message\)[\s\S]*role: "alert"/u);
  assert.match(main, /autofocus: true/u);

  const choices = main.slice(main.indexOf('nav { class: "passkey-entry__actions"'));
  assert.ok(
    choices.indexOf("{NEW_PASSKEY_ACCOUNT_ACTION}") <
      choices.indexOf("{RETURNING_PASSKEY_ACTION}"),
    "the first-use action must remain before returning authentication",
  );
  const hiddenVoiceStart = main.indexOf("if !passkey_gate_visible {");
  const hiddenVoiceEnd = main.indexOf("if passkey_gate_visible {", hiddenVoiceStart);
  const hiddenVoice = main.slice(hiddenVoiceStart, hiddenVoiceEnd);
  assert.ok(hiddenVoiceStart >= 0);
  assert.ok(hiddenVoiceEnd > hiddenVoiceStart);
  assert.match(hiddenVoice, /class: "orb-field"/u);
  assert.match(hiddenVoice, /class: "voice-status"/u);

  const mainGateStart = main.indexOf('div { class: "passkey-gate"');
  const mainGateEnd = main.indexOf("class: if state_snapshot.session_active()", mainGateStart);
  const gateMarkup = main.slice(mainGateStart, mainGateEnd);
  assert.equal(main.match(/div \{ class: "passkey-gate"/gu)?.length, 1);
  assert.ok(gateMarkup.indexOf("{NEW_PASSKEY_ACCOUNT_ACTION}") >= 0);
  assert.ok(
    gateMarkup.indexOf("{NEW_PASSKEY_ACCOUNT_ACTION}") <
      gateMarkup.indexOf("{RETURNING_PASSKEY_ACTION}"),
  );
  assert.match(
    gateMarkup,
    /class: "control-button is-active"[\s\S]*autofocus: true[\s\S]*\{NEW_PASSKEY_ACCOUNT_ACTION\}/u,
  );

  const gateStart = css.indexOf(".passkey-gate {");
  const entryStart = css.indexOf(".passkey-entry {", gateStart);
  const leadStart = css.indexOf(".passkey-entry__lead {", entryStart);
  assert.ok(gateStart >= 0);
  assert.ok(entryStart > gateStart);
  assert.ok(leadStart > entryStart);

  const gate = css.slice(gateStart, entryStart);
  assert.match(gate, /position:\s*fixed/u);
  assert.match(gate, /inset:\s*0/u);
  assert.match(gate, /place-items:\s*center/u);
  assert.match(gate, /overflow-y:\s*auto/u);

  const entry = css.slice(entryStart, leadStart);
  assert.match(entry, /max-height:\s*calc\(100dvh - 24px\)/u);
  assert.match(entry, /margin:\s*0/u);
  assert.match(entry, /overflow-y:\s*auto/u);

  assert.match(css, /body:has\(\.passkey-gate\)[\s\S]*overflow:\s*hidden/u);
  assert.match(css, /\.passkey-entry__actions \{[\s\S]*grid-template-columns:\s*repeat\(2/u);
  assert.match(
    css,
    /\.conversation-stage--passkey > \.utility-dock[\s\S]*display:\s*none/u,
  );
  assert.match(
    css,
    /@media \(max-width: 620px\)[\s\S]*\.passkey-entry__actions \{[\s\S]*grid-template-columns:\s*1fr/u,
  );
});
