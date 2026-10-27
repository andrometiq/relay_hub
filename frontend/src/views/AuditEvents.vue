<script setup>
import { ref, onMounted } from "vue";
import { Badge, Button, Dialog, ErrorMessage, FeatherIcon } from "@relay/ui";
import { usePendingRefresh } from "../composables/usePendingRefresh";
import { hubApi } from "../services/hubApi";

const items = ref([]);
const more = ref(false);
const busy = ref(false);
const error = ref("");
const selected = ref(null);

async function load(append = false) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    const before = append ? items.value.at(-1)?.id : "";
    const data = await hubApi("audit" + (before ? `?before=${encodeURIComponent(before)}` : ""));
    const incoming = data.events || [];
    const known = new Set(items.value.map((row) => row.id));
    items.value = append ? [...items.value, ...incoming.filter((row) => !known.has(row.id))] : incoming;
    more.value = !!data.more;
  } catch (exception) {
    error.value = exception.message;
  } finally {
    busy.value = false;
  }
}

function scrolled(event) {
  const node = event.currentTarget;
  if (more.value && !busy.value && node.scrollHeight - node.scrollTop - node.clientHeight < 180) load(true);
}

onMounted(() => load());
defineExpose({ refresh: usePendingRefresh(busy, load) });
</script>

<template>
  <section aria-label="System activity">
    <div class="entity-toolbar"><span class="text-sm text-ink-gray-6">User, authentication, and runtime events</span><Button class="ml-auto" label="Refresh" icon-left="refresh-cw" :loading="busy" @click="load()" /></div>
    <ErrorMessage :message="error" />
    <div class="event-list infinite-list" aria-label="System activity" @scroll.passive="scrolled">
      <button v-for="row in items" :key="row.id" type="button" class="event-list-row entity-row" @click="selected = row">
        <span class="entity-primary"><span class="entity-icon" aria-hidden="true"><FeatherIcon name="shield" class="h-4 w-4" /></span><span><strong>{{ row.action }}</strong><small>{{ row.occurred_at }}</small></span></span>
        <span class="event-preview">{{ row.account_id || `${row.actor_type}${row.actor_id ? ` #${row.actor_id}` : ''}` }}</span>
        <Badge :label="row.outcome" :theme="row.outcome === 'completed' ? 'green' : 'gray'" />
        <FeatherIcon name="chevron-right" class="h-4 w-4 text-ink-gray-4" />
      </button>
      <p v-if="busy" role="status" class="infinite-list__status">Loading…</p>
      <p v-else-if="!items.length && !error" class="entity-prompt">No system activity yet.</p>
    </div>
    <Button v-if="more" label="Load older activity" :loading="busy" @click="load(true)" />
    <Dialog :model-value="!!selected" :options="{ title: selected?.action || 'System activity', size: 'lg' }" @update:model-value="(value) => !value && (selected = null)">
      <template #body-content><dl v-if="selected" class="entity-details"><div><dt>Time</dt><dd>{{ selected.occurred_at }}</dd></div><div><dt>Outcome</dt><dd>{{ selected.outcome }}</dd></div><div><dt>Site</dt><dd>{{ selected.account_id || 'Hub' }}</dd></div><div><dt>Actor</dt><dd>{{ selected.actor_type }}{{ selected.actor_id ? ` #${selected.actor_id}` : '' }}</dd></div><div v-if="selected.channel"><dt>Channel</dt><dd>{{ selected.channel }}</dd></div><div v-if="selected.error_message"><dt>Issue</dt><dd>{{ selected.error_message }}</dd></div><div v-if="selected.trace_id"><dt>Trace</dt><dd>{{ selected.trace_id }}</dd></div><div v-if="selected.request_id"><dt>Request</dt><dd>{{ selected.request_id }}</dd></div></dl></template>
      <template #actions="{ close }"><Button label="Close" @click="close" /></template>
    </Dialog>
  </section>
</template>
