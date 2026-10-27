import test from 'node:test';
import assert from 'node:assert/strict';
import { subscribeHub } from '../src/services/liveUpdates.js';

test('Socket.IO reconnects refresh and bursts coalesce without polling', async () => {
  globalThis.window = { location: { pathname: '/hub/' } };
  const listeners = new Map(); let options, disconnected = false, calls = 0, finish;
  const socket = { on: (name, fn) => listeners.set(name, fn), off: name => listeners.delete(name), disconnect: () => { disconnected = true; } };
  const stop = subscribeHub(() => { calls++; return new Promise(resolve => { finish = resolve; }); }, opts => { options = opts; return socket; });
  assert.deepEqual(options, { path: '/hub/socket.io', transports: ['websocket'] });
  const pending = listeners.get('connect')();
  listeners.get('hub:invalidate')(); listeners.get('hub:invalidate')();
  assert.equal(calls, 1);
  finish(); await new Promise(resolve => setImmediate(resolve));
  assert.equal(calls, 2); finish(); await pending;
  const reconnect = listeners.get('connect')(); assert.equal(calls, 3); finish(); await reconnect;
  stop(); assert.equal(disconnected, true); assert.equal(listeners.size, 0);
  delete globalThis.window;
});

// Exercise the real data composable, including an invalidation during a read.
import { ref, reactive } from 'vue';
import { readFile } from 'node:fs/promises';
const source = (await readFile(new URL('../src/composables/useHubData.js', import.meta.url), 'utf8')).replace(/^import .*$/gm, '').replace('export function', 'function');
const makeData = new Function('ref', 'reactive', 'hubApi', source+'; return useHubData();');
test('busy data reads retain one pending live refresh', async () => {
  let resolves = [], calls = 0;
  const data = makeData(ref, reactive, () => { calls++; return new Promise(resolve => resolves.push(resolve)); });
  const first = data.refresh(); await data.refresh(); await data.refresh();
  assert.equal(calls, 4);
  resolves.splice(0).forEach(resolve => resolve({ accounts: [], counts: {} }));
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(calls, 8);
  resolves.splice(0).forEach(resolve => resolve({ accounts: [], counts: {} }));
  await first; assert.equal(data.busy.value, false); assert.equal(data.revision.value, 2);
});
