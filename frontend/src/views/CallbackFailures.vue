<script setup>
import { onMounted, ref } from 'vue';
import { Badge, Button, Dialog, ErrorMessage, FormControl } from '@relay/ui';
import { hubApi } from '../services/hubApi';
const props = defineProps({ account: String });
const emit = defineEmits(['changed']);
const items = ref([]), more = ref(false), busy = ref(false), error = ref('');
const selected = ref(null), action = ref('replay'), note = ref('');
async function load(append = false) {
  if (busy.value) return;
  busy.value = true; error.value = '';
  try {
    const data = await hubApi('callback-failures?' + new URLSearchParams({ account: props.account, channel: 'WhatsApp', ...(append ? { before: items.value.at(-1).id } : {}) }));
    items.value = append ? [...items.value, ...data.items] : data.items || [];
    more.value = !!data.more;
  } catch (e) { error.value = e.message; } finally { busy.value = false; }
}
function open(row) { selected.value = row; action.value = 'replay'; note.value = ''; error.value = ''; }
async function recover() {
  if (busy.value) return;
  busy.value = true; error.value = '';
  try {
    await hubApi('callback-failures/retry', { account: props.account, channel: 'WhatsApp', id: String(selected.value.id), lane: selected.value.lane, action: action.value, note: note.value });
    selected.value = null; busy.value = false; await load(); emit('changed');
  } catch (e) { error.value = e.message; } finally { busy.value = false; }
}
onMounted(load);
defineExpose({ refresh: load });
</script>
<template>
  <section class="mt-6" aria-label="Callback failures">
    <div class="view-heading"><div><h2>Core callbacks</h2><p>Failed acknowledgements and quarantined events. Forwarded acknowledgements are shown in Events.</p></div><Button label="Refresh callbacks" :loading="busy" @click="load()" /></div>
    <ErrorMessage :message="error" />
    <p v-if="busy" role="status">Loading callbacks…</p>
    <p v-else-if="!items.length && !error" class="entity-prompt">No failed callbacks for this account.</p>
    <button v-for="row in items" :key="`${row.id}:${row.lane}`" class="entity-row callback-row" @click="open(row)">
      <strong>#{{ row.id }} · {{ row.lane }}</strong><span>{{ row.reason }}</span><Badge :label="row.quarantined ? 'Quarantined' : 'Retrying'" /><span>{{ row.failures }} failures</span>
    </button>
    <Button v-if="more" label="Load more callbacks" :loading="busy" @click="load(true)" />
    <Dialog :model-value="!!selected" :options="{title: 'Recover Core callback', size: 'lg'}" :disable-outside-click-to-close="busy" @update:model-value="value => !value && (selected = null)">
      <template #body-content><div v-if="selected" class="space-y-4">
        <dl class="entity-details"><div><dt>Event / lane</dt><dd>#{{ selected.id }} / {{ selected.lane }}</dd></div><div><dt>Failure</dt><dd>{{ selected.reason }}</dd></div><div><dt>Failures</dt><dd>{{ selected.failures }}</dd></div><div><dt>Next attempt</dt><dd>{{ selected.next_attempt || 'Not scheduled' }}</dd></div><div><dt>Updated</dt><dd>{{ selected.created_at }}</dd></div></dl>
        <FormControl v-model="action" type="select" label="Recovery action" :options="[{ label: 'Replay callback to Core', value: 'replay' }, { label: 'Quarantine callback', value: 'quarantine' }]" />
        <p class="text-sm text-ink-gray-6">Replay schedules this event for Core again; it does not resend the WhatsApp message. Quarantine pauses this callback for review. Record the reason before confirming.</p>
        <FormControl v-model="note" type="textarea" label="Recovery reason" />
        <ErrorMessage :message="error" />
      </div></template>
      <template #actions="{close}"><Button label="Close" :disabled="busy" @click="close" /><Button label="Confirm recovery" :loading="busy" :disabled="note.trim().length < 10 || note.length > 2000" @click="recover" /></template>
    </Dialog>
  </section>
</template>
