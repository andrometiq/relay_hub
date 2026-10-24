import { usePendingRefresh } from '../src/composables/usePendingRefresh.js';
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { ref, watch, computed, nextTick, effectScope } from 'vue';
const source = (await readFile(new URL('../src/views/DeliveryView.vue', import.meta.url), 'utf8')).split('<script setup>')[1].split('</script>')[0].replace(/import[\s\S]*?from\s+["'][^"']+["'];/g, '');
const make = new Function('ref','watch','computed','defineEmits','defineExpose','hubApi','usePendingRefresh',source+'\nreturn {account,channel,rows,load,open,change,chatId,providerId,note,busy};');

test('delivery recovery sends selected transport and Telegram evidence',async()=>{
 const scope=effectScope(),calls=[];
 const pane=scope.run(()=>make(ref,watch,computed,()=>()=>{},()=>{},async(path,body)=>{calls.push({path,body});return {deliveries:[{id:9,state:'uncertain'}]};},usePendingRefresh));
 pane.account.value='local';await nextTick();await new Promise(r=>setImmediate(r));
 for(const channel of ['Telegram','Email','WhatsApp']){
  pane.channel.value=channel;await nextTick();await new Promise(r=>setImmediate(r));
  assert.ok(calls.at(-1).path.includes('channel='+channel));
  pane.open({id:9,state:'uncertain'});pane.providerId.value='123';pane.chatId.value='-10001';pane.note.value='Verified provider acceptance';
  await pane.change('resolve');
  const mutation=calls.findLast(c=>c.body);
  assert.equal(mutation.body.channel,channel);
  assert.equal(mutation.body.provider_id,'123');
  assert.equal(mutation.body.chat_id,channel==='Telegram'?-10001:undefined);
 }
 scope.stop();
});

for (const phase of ['initial', 'pagination', 'mutation', 'failed read']) {
 test(`invalidation during ${phase} retains a delivery refresh`, async () => {
  const scope = effectScope(), requests = [];
  let exposed;
  const pane = scope.run(() => make(ref, watch, computed, () => () => {}, value => { exposed = value; }, (path, body) => new Promise((resolve, reject) => requests.push({ path, body, resolve, reject })), usePendingRefresh));
  pane.account.value = 'local';
  await nextTick();
  if (phase === 'pagination' || phase === 'mutation') {
   requests[0].resolve({ deliveries: [{ id: 1, state: 'queued' }], more: true });
   await new Promise(resolve => setImmediate(resolve));
   if (phase === 'pagination') pane.load(true);
   else { pane.open({ id: 1, state: 'queued' }); pane.change('cancel'); }
  }
  const prior = requests.length;
  await exposed.refresh(); await exposed.refresh();
  assert.equal(requests.length, prior);
  if (phase === 'failed read') requests.at(-1).reject(new Error('local read failure'));
  else requests.at(-1).resolve({ deliveries: [{ id: 1, state: 'queued' }], more: true });
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(requests.length, prior + 1);
  assert.equal(new URLSearchParams(requests.at(-1).path.split('?')[1]).get('after'), '');
  requests.at(-1).resolve({ deliveries: [{ id: 1, state: 'submitted' }] });
  await new Promise(resolve => setImmediate(resolve));
  // A mutation's own refresh can also be in flight when invalidation is retained.
  if (requests.length > prior + 1) {
   requests.at(-1).resolve({ deliveries: [{ id: 1, state: 'submitted' }] });
   await new Promise(resolve => setImmediate(resolve));
  }
  assert.equal(pane.rows.value[0].state, 'submitted');
  assert.equal(pane.busy.value, false);
  scope.stop();
 });
}
