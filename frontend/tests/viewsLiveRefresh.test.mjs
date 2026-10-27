import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { ref, watch, computed, effectScope, nextTick, onScopeDispose } from 'vue';
import { usePendingRefresh } from '../src/composables/usePendingRefresh.js';

for (const view of ['CallbackFailures', 'EventsView', 'AuditEvents']) {
  const source = (await readFile(new URL(`../src/views/${view}.vue`, import.meta.url), 'utf8'))
    .split('<script setup>')[1].split('</script>')[0].replace(/import[\s\S]*?from\s+["'][^"']+["'];/g, '');
  const make = new Function('ref', 'watch', 'computed', 'onMounted', 'onBeforeUnmount', 'defineProps', 'defineEmits', 'defineExpose', 'hubApi', 'usePendingRefresh',
    source + '\nreturn {load, items, busy, account: typeof account === "undefined" ? null : account};');
  for (const append of [false, true]) {
    test(`${view} retains invalidation across ${append ? 'pagination' : 'initial read'}`, async () => {
      const scope = effectScope(), requests = [], mounts = [];
      let exposed;
      const pane = scope.run(() => make(ref, watch, computed, fn => mounts.push(fn), onScopeDispose,
        () => ({ account: 'local', channel: 'Email' }), () => () => {}, value => { exposed = value; },
        path => new Promise(resolve => requests.push({ path, resolve })), usePendingRefresh));
      mounts.forEach(fn => fn());
      if (pane.account) { pane.account.value = 'local'; await nextTick(); }
      const page = state => ({ items: [{ id: 1, state }], events: [{ id: 1, state }], more: true });
      if (append) {
        requests[0].resolve(page('stale'));
        await new Promise(resolve => setImmediate(resolve));
        pane.load(true);
      }
      const count = requests.length;
      exposed.refresh(); exposed.refresh();
      assert.equal(requests.length, count);
      requests.at(-1).resolve(page('stale'));
      await new Promise(resolve => setImmediate(resolve));
      assert.equal(requests.length, count + 1);
      assert.equal(requests.at(-1).path.includes('before='), false);
      requests.at(-1).resolve(page('fresh'));
      await new Promise(resolve => setImmediate(resolve));
      assert.equal(pane.items.value[0].state, 'fresh');
      assert.equal(pane.busy.value, false);
      scope.stop();
    });
  }
}

test('pending refresh watcher stops with its view', async () => {
  const scope = effectScope(), busy = ref(true);
  let calls = 0;
  const refresh = scope.run(() => usePendingRefresh(busy, () => { calls++; }));
  refresh(); scope.stop(); busy.value = false;
  await nextTick();
  assert.equal(calls, 0);
});
