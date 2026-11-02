import test from 'node:test';
import assert from 'node:assert/strict';
import { adminApi, signupRow, tokenPayload, tokenRow, validateTokenForm } from '../src/composables/containers.js';

const form = (extra = {}) => ({ name: 'Acme bench', kind: 'bench', origin_pattern: 'https://*.example.com', max_sites: '', expiry_days: '', ...extra });

test('enrolment token form checks the pattern and the limits of each kind', () => {
  assert.deepEqual(validateTokenForm(form()), {});
  assert.ok(validateTokenForm(form({ origin_pattern: '' })).origin_pattern);
  assert.deepEqual(validateTokenForm(form({ kind: 'setup_code', origin_pattern: '' })), {});
  assert.ok(validateTokenForm(form({ origin_pattern: 'https://*.example.com/path' })).origin_pattern);
  assert.ok(validateTokenForm(form({ name: ' ' })).name);
  assert.ok(validateTokenForm(form({ max_sites: '0' })).max_sites);
  assert.ok(validateTokenForm(form({ kind: 'setup_code', expiry_days: '91' })).expiry_days);
  assert.deepEqual(validateTokenForm(form({ expiry_days: '0' })), {});
});

test('blank number fields leave the Hub defaults', () => {
  assert.deepEqual(tokenPayload(form({ name: ' Bench ' })), { name: 'Bench', kind: 'bench', origin_pattern: 'https://*.example.com' });
  assert.deepEqual(tokenPayload(form({ max_sites: '5', expiry_days: '30' })), { name: 'Acme bench', kind: 'bench', origin_pattern: 'https://*.example.com', max_sites: 5, expiry_days: 30 });
});

test('token rows show usage, expiry and status', () => {
  const bench = tokenRow({ id: 1, name: 'Bench', kind: 'bench', used_sites: 3, max_sites: null, expires_at: null, status: 'active' });
  assert.deepEqual([bench.kindLabel, bench.usage, bench.expiry, bench.statusLabel, bench.theme], ['Bench', '3 sites', 'Never', 'Active', 'green']);
  const code = tokenRow({ id: 2, name: 'Code', kind: 'setup_code', used_sites: 1, max_sites: 1, expires_at: '2026-11-09T00:00:00Z', status: 'used' });
  assert.deepEqual([code.kindLabel, code.usage, code.statusLabel], ['Setup code', '1 of 1', 'Used']);
});

test('signup rows show the first open step and the safe error', () => {
  const row = signupRow({ session_id: 'sus_1', flow: 'auto', flow_actual: 'coexistence', state: 'code_received', created_at: '2026-11-02T10:00:00Z',
    steps: [{ name: 'token', status: 'done' }, { name: 'verify', status: 'skipped' }, { name: 'channel', status: 'failed' }],
    error: { code: 'number_in_use', message: 'This number is linked to another site.' } });
  assert.deepEqual([row.flow, row.state, row.theme, row.step, row.error], ['coexistence', 'code received', 'orange', 'channel', 'This number is linked to another site.']);
  assert.equal(signupRow({ session_id: 'sus_2', flow: 'new', state: 'completed', created_at: '2026-11-02T10:00:00Z', steps: [{ name: 'done', status: 'done' }], error: null }).theme, 'green');
});

test('enrolment admin calls use the token and site signup paths', async t => {
  const requests = [];
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    requests.push([options.method, url, options.body || '']);
    return Response.json({ tokens: [{ id: 4 }] });
  });
  assert.deepEqual(await adminApi.enrolmentTokens(), [{ id: 4 }]);
  await adminApi.createEnrolmentToken({ name: 'Code', kind: 'setup_code' });
  await adminApi.revokeEnrolmentToken(4);
  await adminApi.siteSignups('a b');
  assert.deepEqual(requests, [
    ['GET', './api/enrolment-tokens', ''],
    ['POST', './api/enrolment-tokens', '{"name":"Code","kind":"setup_code"}'],
    ['POST', './api/enrolment-tokens/4/revoke', '{}'],
    ['GET', './api/sites/a%20b/signups', ''],
  ]);
});
