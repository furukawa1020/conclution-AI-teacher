import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("passkey choices replace the voice UI inside the first viewport", async () => {
  const main = await readFile(new URL("../src/main.rs", import.meta.url), "utf8");
  const css = await readFile(new URL("../assets/main.css", import.meta.url), "utf8");

  assert.match(main, /requires_passkey_choice\(effective_cloud_state, state_snapshot\)/u);
  assert.match(main, /"conversation-stage conversation-stage--passkey"/u);
  assert.match(main, /"voice-space voice-space--passkey"/u);
  assert.match(main, /aria_labelledby: voice_heading_id/u);
  assert.match(main, /VoiceState::Error\(message\)[\s\S]*role: "alert"/u);

  const hiddenVoiceStart = main.indexOf("if !passkey_gate_visible {");
  const hiddenVoiceEnd = main.indexOf("if passkey_gate_visible {", hiddenVoiceStart);
  const hiddenVoice = main.slice(hiddenVoiceStart, hiddenVoiceEnd);
  assert.ok(hiddenVoiceStart >= 0);
  assert.ok(hiddenVoiceEnd > hiddenVoiceStart);
  assert.match(hiddenVoice, /class: "orb-field"/u);
  assert.match(hiddenVoice, /class: "voice-status"/u);
  assert.match(main, /id: "voice-start-action"/u);
  assert.match(
    main,
    /id: "passkey-setup-status",[\s\S]*role: "status",[\s\S]*aria_live: "polite",[\s\S]*PasskeySetupFeedback::Success\(message\)/u,
  );
  assert.match(
    main,
    /use_effect\(move \|\|[\s\S]*passkey_focus_target[\s\S]*cloud::focus_element\(target\.element_id\(\)\)/u,
  );

  const gateStart = main.indexOf('div { class: "passkey-gate"');
  const gateEnd = main.indexOf("class: if state_snapshot.session_active()", gateStart);
  const gate = main.slice(gateStart, gateEnd);
  const newActionAt = gate.indexOf("{NEW_PASSKEY_ACCOUNT_ACTION}");
  const returningActionAt = gate.indexOf("{RETURNING_PASSKEY_ACTION}");
  const newButtonStart = gate.lastIndexOf("button {", newActionAt);
  const returningButtonStart = gate.lastIndexOf("button {", returningActionAt);
  const newButton = gate.slice(newButtonStart, returningButtonStart);
  const returningButton = gate.slice(
    returningButtonStart,
    gate.indexOf('id: "new-passkey-account-warning"', returningButtonStart),
  );
  assert.equal(main.match(/div \{ class: "passkey-gate"/gu)?.length, 1);
  assert.ok(newActionAt >= 0);
  assert.ok(returningActionAt > newActionAt);
  assert.ok(newButtonStart >= 0);
  assert.ok(returningButtonStart > newButtonStart);
  assert.match(gate, /class: "passkey-entry__actions",\s*role: "group"/u);
  assert.match(gate, /aria_describedby: "new-passkey-account-warning"/u);
  assert.match(
    gate,
    /class: "control-button is-active"[\s\S]*\{NEW_PASSKEY_ACCOUNT_ACTION\}/u,
  );
  assert.match(newButton, /id: "new-passkey-account-action"/u);
  assert.match(returningButton, /id: "returning-passkey-account-action"/u);
  assert.doesNotMatch(gate, /autofocus:/u);
  assert.match(
    newButton,
    /disabled: passkey_setup_is_busy \|\| passkey_registration_recovery_required/u,
  );
  assert.doesNotMatch(gate, /role: "region"/u);
  assert.doesNotMatch(gate, /section \{\s*class: "passkey-entry"/u);
  assert.match(
    gate,
    /PasskeySetupFeedback::Error\(message\)[\s\S]*class: "passkey-entry__error", role: "alert"/u,
  );
  assert.match(
    gate,
    /if !passkey_setup_is_busy && passkey_setup_feedback_snapshot\.is_none\(\)[\s\S]*VoiceState::Error\(message\)/u,
  );
  assert.doesNotMatch(gate, /PasskeySetupFeedback::Success\(message\)/u);
  assert.match(
    gate,
    /if passkey_registration_recovery_required \{[\s\S]*PASSKEY_REGISTRATION_RECOVERY_REQUIRED_COPY/u,
  );
  assert.match(main, /if passkey_gate_visible \{[\s\S]*声の本人確認ではない/u);

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
  assert.match(panel, /min-width:\s*0/u);
  assert.match(panel, /max-width:\s*100%/u);
  assert.match(panel, /place-items:\s*start center/u);
  assert.match(panel, /margin-top:\s*clamp\(/u);
  assert.doesNotMatch(panel, /position:\s*(?:absolute|fixed)/u);

  const entryEnd = css.indexOf(".passkey-entry__eyebrow {", entryStart);
  const entry = css.slice(entryStart, entryEnd);
  assert.match(entry, /width:\s*100%/u);
  assert.match(entry, /max-width:\s*580px/u);

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
    /\.control-button:not\(:disabled\):hover \{/u,
  );
  assert.match(
    css,
    /\.control-button:disabled \{[\s\S]*cursor:\s*not-allowed[\s\S]*opacity:\s*0\.52/u,
  );
  assert.match(
    css,
    /@media \(max-width: 620px\)[\s\S]*\.passkey-entry__actions \{[\s\S]*grid-template-columns:\s*1fr/u,
  );
  assert.match(
    css,
    /\.context-line \{[\s\S]*flex-wrap:\s*wrap/u,
  );
  assert.match(
    css,
    /\.passkey-setup-status:not\(:empty\) \{[\s\S]*flex-basis:\s*100%[\s\S]*overflow-wrap:\s*anywhere/u,
  );
  const shortViewportStart = css.indexOf("@media (max-height: 540px)");
  const shortViewport = css.slice(shortViewportStart);
  assert.ok(shortViewportStart >= 0);
  assert.match(
    shortViewport,
    /\.conversation-stage--passkey \.context-line \{[\s\S]*min-height:\s*0/u,
  );
  assert.match(
    shortViewport,
    /\.passkey-entry \{[\s\S]*max-height:\s*calc\(100svh - 148px\)[\s\S]*overflow-y:\s*auto/u,
  );
  assert.doesNotMatch(
    shortViewport,
    /\.conversation-stage--passkey \.context-line \{[\s\S]*display:\s*none/u,
  );
  assert.doesNotMatch(
    shortViewport,
    /\.passkey-entry__eyebrow \{[\s\S]*display:\s*none/u,
  );
  assert.doesNotMatch(
    shortViewport,
    /\.passkey-entry__actions \{[\s\S]*grid-template-columns:\s*repeat\(2/u,
  );
});
