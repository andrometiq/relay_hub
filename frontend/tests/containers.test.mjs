import test from 'node:test';
import assert from 'node:assert/strict';
import { adminApi, assignedRefs, channelKey, channelStatus, hubURL, moveChannel, pickerGroups, siteChannelOptions, siteStatus, siteTokens } from '../src/composables/containers.js';
import { channelForm, channelPayload, validateChannelForm } from '../src/composables/channelForm.js';
import { parseRoute, routePath } from '../src/useViewRoute.js';

const views = ['Overview', 'Containers', 'Sites'];
const whatsapp = (extra = {}) => ({ channel: 'WhatsApp', key: 'sales', container: 'main', label: 'Sales line', enabled: true, site: 'yrp', config: { phone_id: '108', waba_id: '220' }, configured_secrets: ['secret'], ...extra });

test('routes open detail pages and send old account links to sites', () => {
  assert.deepEqual(parseRoute('#/containers/essdee-main', views), { view: 'Containers', detail: 'essdee-main' });
  assert.deepEqual(parseRoute('#/accounts', views, { accounts: 'sites' }), { view: 'Sites', detail: '' });
  assert.equal(parseRoute('#/unknown', views), null);
  assert.deepEqual(parseRoute(routePath('Sites', 'a b/c'), views), { view: 'Sites', detail: 'a b/c' });
  assert.equal(routePath('Containers'), '#/containers');
});

test('channel status reflects enabled state, site link and provider checks', () => {
  assert.equal(channelStatus(whatsapp({ enabled: false })).label, 'Disabled');
  assert.equal(channelStatus(whatsapp({ site: '' })).label, 'Idle');
  assert.equal(channelStatus(whatsapp()).label, 'Setup needed');
  assert.equal(channelStatus(whatsapp({ config: { connection_status: 'connected' } })).theme, 'green');
  assert.equal(channelStatus({ channel: 'Telegram', enabled: true, site: 'yrp', config: {} }).label, 'Not checked');
  assert.equal(channelStatus({ channel: 'Telegram', enabled: true, site: 'yrp', config: { webhook_status: 'registered' } }).label, 'Registered');
  assert.equal(channelStatus({ channel: 'Email', enabled: true, site: 'yrp', config: { connection_status: 'failed' } }).theme, 'red');
  assert.equal(siteStatus({ enabled: true, configured_secrets: ['core_api_key'] }).label, 'Setup needed');
  assert.equal(siteStatus({ enabled: true, configured_secrets: ['core_api_key', 'core_api_secret', 'callback_secret'] }).label, 'Enabled');
});

test('account picker blocks accounts linked to another site and shows that site', () => {
  const containers = [
    { id: 'main', name: 'Essdee Main', channels: [whatsapp({ site: '' }), whatsapp({ key: 'pos', label: 'POS line', site: 'pos', config: { display_number: '+91 94430 55120' } })] },
    { id: 'retail', name: '', channels: [whatsapp({ key: 'dealer', label: 'Dealer line' })] },
  ];
  const sites = [{ id: 'pos', origin: 'https://pos.example.test' }];
  const groups = pickerGroups(containers, 'yrp', '', sites);
  assert.deepEqual(groups.map(group => group.name), ['Essdee Main', 'retail']);
  assert.deepEqual(groups[0].rows.map(row => [row.key, row.state, row.siteName]), [['sales', 'free', ''], ['pos', 'elsewhere', 'pos.example.test']]);
  assert.equal(groups[1].rows[0].state, 'here');
  assert.deepEqual(pickerGroups(containers, 'yrp', '55120', sites).map(group => group.rows.map(row => row.key)), [['pos']]);
});

test('assignment lists add without duplicates and remove one account', () => {
  const linked = [{ channel: 'WhatsApp', key: 'sales', label: 'x' }, { channel: 'Email', key: 'sales' }];
  assert.deepEqual(assignedRefs(linked, [{ channel: 'WhatsApp', key: 'sales' }, { channel: 'Telegram', key: 'bot' }]), [{ channel: 'WhatsApp', key: 'sales' }, { channel: 'Email', key: 'sales' }, { channel: 'Telegram', key: 'bot' }]);
  assert.deepEqual(assignedRefs(linked, [], { channel: 'Email', key: 'sales' }), [{ channel: 'WhatsApp', key: 'sales' }]);
});

test('channel keys are slugs that avoid keys already in use', () => {
  assert.equal(channelKey('Sales Line #1'), 'sales-line-1');
  assert.equal(channelKey('Sales line', ['sales-line', 'sales-line-2']), 'sales-line-3');
  assert.equal(channelKey('***'), 'account');
});

test('moving an account leaves the old site before it joins the new one', async t => {
  const requests = [];
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    requests.push([options.method, url, options.body && JSON.parse(options.body)]);
    if (options.method === 'GET') return Response.json({ id: url.endsWith('/a') ? 'a' : 'b', channels: url.endsWith('/a') ? [{ channel: 'WhatsApp', key: 'sales' }, { channel: 'Email', key: 'desk' }] : [] });
    return new Response(null, { status: 204 });
  });
  await moveChannel({ channel: 'WhatsApp', key: 'sales' }, 'a', 'b');
  assert.deepEqual(requests, [
    ['GET', './api/sites/a', undefined],
    ['PUT', './api/sites/a/channels', { channels: [{ channel: 'Email', key: 'desk' }] }],
    ['GET', './api/sites/b', undefined],
    ['PUT', './api/sites/b/channels', { channels: [{ channel: 'WhatsApp', key: 'sales' }] }],
  ]);
  requests.length = 0;
  await moveChannel({ channel: 'WhatsApp', key: 'sales' }, 'a', 'a');
  assert.equal(requests.length, 0);
});

test('WhatsApp numbers use the container app unless the number overrides it', () => {
  const form = channelForm('WhatsApp');
  form.label = 'Dispatch line'; form.config.phone_id = '108927364710'; form.config.waba_id = 'x';
  assert.deepEqual(Object.keys(validateChannelForm(form)).sort(), ['secret', 'waba_id']);
  form.config.waba_id = '220119002';
  assert.deepEqual(validateChannelForm(form, { containerToken: true }), {});
  form.override = true;
  assert.deepEqual(Object.keys(validateChannelForm(form, { containerToken: true })).sort(), ['app_secret', 'verify_token']);
  form.config.app_secret = 'number-secret'; form.config.verify_token = 'number-token';
  assert.equal(channelPayload(form).config.app_secret, 'number-secret');
  assert.equal('use_container_app' in channelPayload(form), false);
  form.override = false;
  const payload = channelPayload(form, ['dispatch-line']);
  assert.equal(payload.key, 'dispatch-line-2');
  assert.equal('app_secret' in payload.config || 'verify_token' in payload.config, false);
  assert.equal(payload.use_container_app, true);
});

test('editing keeps stored config and saved secrets', () => {
  const form = channelForm('WhatsApp', whatsapp({ config: { phone_id: '108', waba_id: '220', templates: true, messages_per_second: 20 }, configured_secrets: ['secret', 'app_secret', 'verify_token'] }));
  assert.equal(form.override, true);
  assert.deepEqual(validateChannelForm(form), {});
  const payload = channelPayload(form);
  assert.equal(payload.key, 'sales');
  assert.equal(payload.config.templates, true);
  assert.equal(payload.config.messages_per_second, 20);
  assert.equal(payload.secret, '');
});

test('mailbox and bot checks reuse the channel rules', () => {
  const mail = channelForm('Email');
  Object.assign(mail, { label: 'Orders desk', secret: 'app-password' });
  Object.assign(mail.config, { from: 'Orders@Example.test', host: 'smtp.example.test', port: '587', username: 'orders@example.test', imap_host: 'https://bad' });
  assert.deepEqual(Object.keys(validateChannelForm(mail)), ['imap_host']);
  mail.config.imap_host = 'imap.example.test';
  assert.deepEqual(validateChannelForm(mail), {});
  const payload = channelPayload(mail);
  assert.equal(payload.config.from, 'orders@example.test');
  assert.equal(payload.config.port, 587);
  assert.equal(payload.config.imap_port, 993);
  const bot = channelForm('Telegram');
  Object.assign(bot, { label: 'Dealer bot', secret: 'not-a-token' });
  assert.deepEqual(Object.keys(validateChannelForm(bot)), ['secret']);
});

test('site tokens show only keys for that site', async t => {
  t.mock.method(globalThis, 'fetch', async url => {
    assert.equal(url, 'https://hub.example.test/hub/auth/tokens?query=yrp');
    return Response.json({ Tokens: [{ ID: 1, Account: 'yrp' }, { ID: 2, Account: 'yrp-b' }], More: false });
  });
  globalThis.window = { location: { origin: 'https://hub.example.test', pathname: '/hub/' } };
  try {
    assert.deepEqual((await siteTokens('yrp')).tokens.map(row => row.ID), [1]);
  } finally {
    delete globalThis.window;
  }
  assert.equal(hubURL('/webhooks/whatsapp/main', { origin: 'https://x.test', pathname: '/' }), 'https://x.test/webhooks/whatsapp/main');
});

test('delivery and event pickers list one channel type of each site', () => {
  const sites = [
    { id: 'yrp', name: 'YRP', origin: 'https://yrp.example.test', channels: [whatsapp(), { channel: 'Telegram', key: 'bot', label: 'Bot' }] },
    { id: 'pos', name: '', origin: 'https://pos.example.test', channels: [whatsapp({ key: 'pos-line', label: '' })] },
  ];
  assert.deepEqual(siteChannelOptions(sites).map((row) => [row.key, row.site, row.label]), [['sales', 'yrp', 'YRP · Sales line'], ['pos-line', 'pos', 'pos.example.test · pos-line']]);
  assert.deepEqual(siteChannelOptions(sites, 'Telegram').map((row) => row.key), ['bot']);
  assert.deepEqual(siteChannelOptions(sites, 'Email'), []);
});

test('turning off an old address deletes it for the container or one channel', async t => {
  const requests = [];
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    requests.push([options.method, url]);
    return new Response(null, { status: 204 });
  });
  await adminApi.turnOffOldAddress('ct 1');
  await adminApi.turnOffOldAddress('ct 1', { channel: 'Telegram', key: 'sales/bot' });
  assert.deepEqual(requests, [
    ['DELETE', './api/containers/ct%201/old-address'],
    ['DELETE', './api/containers/ct%201/channels/Telegram/sales%2Fbot/old-address'],
  ]);
});
