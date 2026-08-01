import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("passkey choices replace the voice UI inside the first viewport", async () => {
  const main = await readFile(new URL("../src/main.rs", import.meta.url), "utf8");
  const css = await readFile(new URL("../assets/main.css", import.meta.url), "utf8");

  assert.match(main, /requires_passkey_choice\(prepared_cloud_state, state_snapshot\)/u);
  assert.match(main, /"conversation-stage conversation-stage--passkey"/u);
  assert.match(main, /"voice-space voice-space--passkey"/u);
  assert.match(main, /role: "region"/u);
  assert.match(main, /aria_labelledby: "passkey-entry-heading"/u);
  assert.match(main, /VoiceState::Error\(message\)[\s\S]*role: "alert"/u);

  const hiddenVoiceStart = main.indexOf("if !passkey_gate_visible {");
  const hiddenVoiceEnd = main.indexOf("if passkey_gate_visible {", hiddenVoiceStart);
  const hiddenVoice = main.slice(hiddenVoiceStart, hiddenVoiceEnd);
  assert.ok(hiddenVoiceStart >= 0);
  assert.ok(hiddenVoiceEnd > hiddenVoiceStart);
  assert.match(hiddenVoice, /class: "orb-field"/u);
  assert.match(hiddenVoice, /class: "voice-status"/u);

  const gateStart = main.indexOf('div { class: "passkey-gate"');
  const gateEnd = main.indexOf("class: if state_snapshot.session_active()", gateStart);
  const gate = main.slice(gateStart, gateEnd);
  assert.equal(main.match(/div \{ class: "passkey-gate"/gu)?.length, 1);
  assert.ok(gate.indexOf("{NEW_PASSKEY_ACCOUNT_ACTION}") >= 0);
  assert.ok(
    gate.indexOf("{NEW_PASSKEY_ACCOUNT_ACTION}") <
      gate.indexOf("{RETURNING_PASSKEY_ACTION}"),
  );
  assert.match(gate, /class: "passkey-entry__actions",\s*role: "group"/u);
  assert.match(gate, /aria_describedby: "new-passkey-account-warning"/u);
  assert.match(
    gate,
    /class: "control-button is-active"[\s\S]*autofocus: true[\s\S]*\{NEW_PASSKEY_ACCOUNT_ACTION\}/u,
  );

  assert.match(
    css,
    /\.conversation-stage--passkey \{[\s\S]*grid-template-columns:\s*minmax\(0, 1fr\)[\s\S]*width:\s*min\(880px, 100%\)/u,
  );
  assert.match(
    css,
    /\.conversation-stage--passkey \.utility-dock \{[\s\S]*display:\s*none/u,
  );
  assert.match(
    css,
    /\.voice-space--passkey \{[\s\S]*grid-template-rows:\s*auto auto[\s\S]*min-height:\s*0/u,
  );

  const panelStart = css.indexOf(".passkey-gate {");
  const entryStart = css.indexOf(".passkey-entry {", panelStart);
  const panel = css.slice(panelStart, entryStart);
  assert.ok(panelStart >= 0);
  assert.ok(entryStart > panelStart);
  assert.match(panel, /grid-template-columns:\s*minmax\(0, 1fr\)/u);
  assert.match(panel, /place-items:\s*start center/u);
  assert.match(panel, /margin-top:\s*clamp\(/u);
  assert.doesNotMatch(panel, /position:\s*(?:absolute|fixed)/u);

  assert.match(
    css,
    /\.passkey-entry__actions \{[\s\S]*grid-template-columns:\s*repeat\(2/u,
  );
  assert.match(
    css,
    /\.passkey-entry__actions \.control-button \{[\s\S]*min-width:\s*0[\s\S]*white-space:\s*normal/u,
  );
  assert.match(
    css,
    /@media \(max-width: 620px\)[\s\S]*\.passkey-entry__actions \{[\s\S]*grid-template-columns:\s*1fr/u,
  );
  assert.match(
    css,
    /@media \(max-height: 540px\)[\s\S]*\.passkey-entry__eyebrow \{[\s\S]*display:\s*none/u,
  );
});
