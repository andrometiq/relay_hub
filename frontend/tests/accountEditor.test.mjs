import test from 'node:test';
import assert from 'node:assert/strict';
import { ref } from 'vue';
import { useAccountEditor } from '../src/composables/useAccountEditor.js';
import { accountSavePayload } from '../src/composables/accountForm.js';

test('mixed-channel editing preserves configs, feature flags and stored secret presence', () => {
  const other = { channel: 'Telegram', enabled: true, config: { username: 'retained' }, configured_secrets: ['secret'] };
  const account = { id: 'local-only', site: 'https://local.invalid', enabled: true, channels: [other, { channel: 'WhatsApp', enabled: true, config: { groups: true, calls: true } }] };
  const accounts = ref([account]);
  const editor = useAccountEditor(accounts, async () => {});
  editor.edit(accounts.value[0]);
  const payload = accountSavePayload(editor.draft.value);
  assert.deepEqual(payload.channels.find(row => row.channel === 'Telegram'), { channel: 'Telegram', enabled: true, config: other.config });
  assert.equal(payload.channels[0].config.groups, true);
  assert.equal(payload.channels[0].config.calls, true);
  assert.deepEqual(editor.draft.value.configured.Telegram, ['secret']);
  editor.draft.value.channelConfig.Telegram.username = 'edited copy';
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

test('mixed-channel save preserves blank secrets and clears replacements even when reload fails', async t => {
  const requests = [];
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    requests.push({url, body: JSON.parse(options.body)});
    return url.endsWith('/reload') ? new Response('unavailable', {status:503}) : new Response(null, {status:204});
  });
  const account = {id:'mixed', site:'https://core.invalid', enabled:true, channels:[
    {channel:'Telegram',enabled:true,config:{},configured_secrets:['secret']},
    {channel:'Email',enabled:true,config:{host:'smtp.invalid',port:587,from:'sender@example.invalid',username:'sender',imap_host:'imap.invalid'},configured_secrets:['secret','imap_password']},
  ]};
  const editor = useAccountEditor(ref([account]), async()=>{});
  editor.edit(account);
  assert.equal(editor.draft.value.channelEnabled.WhatsApp, false);
  editor.draft.value.channelConfig.Email.trusted_authserv_id = 'mx.example.invalid';
  editor.draft.value.channelConfig.Email.imap_password = 'new-local-password';
  assert.equal(await editor.save(), false);
  assert.equal(requests.length, 2);
  assert.equal(requests[0].body.secrets.Telegram, '');
  assert.equal(requests[0].body.secrets.Email, '');
  assert.equal(requests[0].body.account.channels.find(c=>c.channel==='Email').config.imap_password, 'new-local-password');
  assert.equal(editor.draft.value.channelConfig.Email.imap_password, undefined);
  assert.ok(editor.draft.value.configured.Email.includes('imap_password'));
  editor.close();
  assert.deepEqual(editor.secrets.value, {});
});
