import test from 'node:test';
import assert from 'node:assert/strict';
import { ref } from 'vue';
import { useAccountEditor } from '../src/composables/useAccountEditor.js';
import { accountSavePayload } from '../src/composables/accountForm.js';

test('saving WhatsApp preserves hidden channel config and feature flags', () => {
  const other = { channel: 'Telegram', enabled: true, config: { username: 'retained' }, configured_secrets: ['secret'] };
  const account = { id: 'local-only', site: 'https://local.invalid', enabled: true, channels: [other, { channel: 'WhatsApp', enabled: true, config: { groups: true, calls: true } }] };
  const accounts = ref([account]);
  const editor = useAccountEditor(accounts, async () => {});
  editor.edit(accounts.value[0]);
  const payload = accountSavePayload(editor.draft.value);
  assert.deepEqual(payload.channels[0], other);
  assert.equal(payload.channels[1].config.groups, true);
  assert.equal(payload.channels[1].config.calls, true);
  editor.draft.value.preservedChannels[0].config.username = 'edited copy';
  assert.equal(other.config.username, 'retained');
});

test('activation failure retains saved identity; retry only reloads runtime', async t => {
  const requests = []; let fail = true; let refreshes = 0;
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    requests.push({ url, body: JSON.parse(options.body) });
    return url.endsWith('/reload') && fail ? new Response('runtime unavailable', {status:503}) : new Response(null, {status:204});
  });
  const editor = useAccountEditor(ref([]), async () => { refreshes++; });
  editor.edit(null);
  Object.assign(editor.draft.value, { id: 'local-only', site: 'https://local.invalid', enabled: false });
  assert.equal(await editor.save(), false);
  assert.equal(editor.editingID.value, 'local-only');
  assert.equal(editor.activationPending.value, true);
  assert.match(editor.notice.value, /runtime unavailable/);
  fail = false;
  await editor.retryActivation();
  assert.equal(editor.activationPending.value, false);
  assert.equal(requests.filter(row => row.url.endsWith('/accounts')).length, 1);
  assert.equal(refreshes, 2);
});
