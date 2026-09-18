import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import { safeVoiceFailureCode, sameTurnCredentials } from '../web/voice-session-policy.mjs';

test('only immutable credentials from the current bounded turn are reusable', () => {
  assert.equal(safeVoiceFailureCode(new Error('authentication_failed')), 'authentication_failed');
  const credentials = Object.freeze({
    appCheckToken: 'app.check.token',
    idToken: 'id.token.value',
  });
  assert.equal(sameTurnCredentials(credentials, 7, 7), credentials);
  assert.equal(sameTurnCredentials(credentials, 7, 8), undefined);
  assert.equal(sameTurnCredentials(credentials, -1, -1), undefined);
  assert.equal(sameTurnCredentials({ ...credentials }, 7, 7), undefined);
  assert.equal(sameTurnCredentials(Object.freeze({ ...credentials, uid: 'other' }), 7, 7), undefined);
  assert.equal(sameTurnCredentials(Object.freeze({ appCheckToken: 'x', idToken: 'has space' }), 7, 7), undefined);
  assert.equal(sameTurnCredentials(Object.freeze({ appCheckToken: 'x', idToken: 'a'.repeat(8193) }), 7, 7), undefined);
});

test('voice start owns credentials until same-turn fallback or cancellation', async () => {
  const bridge = await readFile(new URL('../web/firebase-bridge.js', import.meta.url), 'utf8');
  assert.match(bridge, /const credentials = await secureCredentials\(true\)/u);
  assert.match(bridge, /const recording = createRecording\([\s\S]*?credentials,\s*\);/u);
  assert.match(bridge, /const turnCredentials = sameTurnCredentials\([\s\S]*?recording\.expectedEpoch,[\s\S]*?sessionEpoch/u);
  assert.match(bridge, /turnCredentials === undefined\s*\? secureCredentials\(\)\s*: Promise\.resolve\(turnCredentials\)/u);
  assert.match(bridge, /function rejectRecording\(recording, code\) \{\s*recording\.turnCredentials = undefined/u);
  assert.match(bridge, /recording\.sessionContext = undefined;\s*recording\.turnCredentials = undefined;/u);
});
