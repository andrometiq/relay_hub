import test from 'node:test';
import assert from 'node:assert/strict';
import { hubApi, safeServerError } from '../src/services/hubApi.js';
test('safe server details survive JSON and Go text responses', async t => {
  for (const [body, status] of [['Callback remains quarantined',409], ['{"error":{"message":"Select an account"}}',400], ['Administrator required',403]]) {
    t.mock.method(globalThis, 'fetch', async () => new Response(body,{status}));
    await assert.rejects(hubApi('fixture'), new RegExp(status === 400 ? 'Select an account' : body));
  }
});
test('errors are bounded, redacted and do not expose HTML', () => {
  assert.equal(safeServerError('<html>proxy diagnostics</html>', 'Unavailable'), 'Unavailable');
  assert.equal(safeServerError('Bearer abc.secret api_secret=xyz', ''), 'Bearer [redacted] api_secret=[redacted]');
  assert.equal(safeServerError('x'.repeat(2000), '').length, 800);
});
test('successful reads retain CSRF for subsequent mutations', async t => {
  let request;
  t.mock.method(globalThis, 'fetch', async (_, options) => { request = options; return options.method === 'GET' ? Response.json({csrf:'local-only'}) : new Response(null,{status:204}); });
  await hubApi('accounts'); await hubApi('accounts/reload', {});
  assert.equal(request.headers['X-Relay-CSRF'], 'local-only');
});
