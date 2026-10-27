import test from 'node:test';
import assert from 'node:assert/strict';
import { ref } from 'vue';
import { useAccountEditor } from '../src/composables/useAccountEditor.js';
import { accountSavePayload } from '../src/composables/accountForm.js';

test('WhatsApp-only editing preserves other channels, feature flags and stored secret presence', () => {
  const other = { channel: 'Telegram', enabled: true, config: { username: 'retained' }, configured_secrets: ['secret'] };
  const account = { id: 'local-only', site: 'https://local.invalid', enabled: true, channels: [other, { channel: 'WhatsApp', enabled: true, config: { groups: true, calls: true }, configured_secrets: ['secret'] }] };
  const editor = useAccountEditor(ref([account]), async () => {});
  editor.edit(account);
  const payload = accountSavePayload(editor.draft.value);
  assert.deepEqual(payload.channels.find(row => row.channel === 'Telegram'), other);
  assert.equal(payload.channels.find(row => row.channel === 'WhatsApp').config.groups, true);
  assert.equal(payload.channels.find(row => row.channel === 'WhatsApp').config.calls, true);
  assert.deepEqual(editor.draft.value.configured.WhatsApp, ['secret']);
  assert.equal(editor.draft.value.channelConfig.Telegram, undefined);
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

test('saving a Telegram and Email account sends those channels unchanged without their secrets', async t => {
  const requests = [];
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    requests.push({url, body: JSON.parse(options.body)});
    return url.endsWith('/reload') ? new Response('unavailable', {status:503}) : new Response(null, {status:204});
  });
  const email = {channel:'Email',enabled:true,config:{host:'smtp.invalid',port:587,from:'sender@example.invalid',username:'sender',imap_host:'imap.invalid'},configured_secrets:['secret','imap_password']};
  const telegram = {channel:'Telegram',enabled:true,config:{},configured_secrets:['secret']};
  const account = {id:'mixed', site:'https://core.invalid', enabled:true, channels:[telegram, email]};
  const editor = useAccountEditor(ref([account]), async()=>{});
  editor.edit(account);
  assert.equal(editor.draft.value.channelEnabled.WhatsApp, false);
  assert.equal(await editor.save(), false);
  assert.equal(requests.length, 2);
  assert.deepEqual(requests[0].body.secrets, { WhatsApp: '' });
  assert.deepEqual(requests[0].body.account.channels.filter(c => c.channel !== 'WhatsApp'), [telegram, email]);
  editor.close();
  assert.deepEqual(editor.secrets.value, {});
});
