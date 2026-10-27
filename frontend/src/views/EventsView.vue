<script setup>
import { ref, computed, watch, onBeforeUnmount } from "vue";
import { Badge, Button, Dialog, ErrorMessage, FeatherIcon, FormControl } from "@relay/ui";
import AccountPicker from "../components/AccountPicker.vue";
import AuditEvents from "./AuditEvents.vue";
import { hubApi } from "../services/hubApi";
import { usePendingRefresh } from "../composables/usePendingRefresh";

const audit = ref();
const query = ref("");
const visible = computed(() => items.value.filter(row => `${row.id} ${row.kind} ${row.preview} ${row.callback}`.toLowerCase().includes(query.value.toLowerCase())));
const mode = ref("messages");
const account = ref("");
const items = ref([]);
const more = ref(false);
const busy = ref(false);
const error = ref("");
const selected = ref(null);
const detail = ref(null);
const detailBusy = ref(false);
let generation = 0;
let detailGeneration = 0;

async function load(append = false) {
  if (!account.value || busy.value) return;
  const ticket = append ? generation : ++generation;
  if (!append) {
    items.value = [];
    more.value = false;
  }
  busy.value = true;
  error.value = "";
  try {
    const before = append ? String(items.value.at(-1)?.id || "") : "";
    const data = await hubApi(
      "event-inspector?" +
        new URLSearchParams({
          account: account.value,
          channel: "WhatsApp",
          ...(before ? { before } : {}),
        }),
    );
    if (ticket !== generation) return;
    const incoming = data.items || [];
    const known = new Set(items.value.map((row) => row.id));
    items.value = append
      ? [...items.value, ...incoming.filter((row) => !known.has(row.id))]
      : incoming;
    more.value = !!data.more;
  } catch (exception) {
    if (ticket === generation) error.value = exception.message;
  } finally {
    if (ticket === generation) busy.value = false;
  }
}

function scrolled(event) {
  const node = event.currentTarget;
  if (more.value && !busy.value && node.scrollHeight - node.scrollTop - node.clientHeight < 180)
    load(true);
}

async function inspect(row) {
  const ticket = ++detailGeneration;
  selected.value = row;
  detail.value = null;
  detailBusy.value = true;
  error.value = "";
  try {
    const result = await hubApi(
      "event-inspector?" +
        new URLSearchParams({ account: account.value, channel: "WhatsApp", id: String(row.id) }),
    );
    if (ticket !== detailGeneration) return;
    let formatted = result.raw;
    if (!result.truncated) {
      try { formatted = JSON.stringify(JSON.parse(formatted), null, 2); } catch {}
    }
    detail.value = { ...result, truncated: result.truncated || formatted.length > 65536, text: formatted.slice(0, 65536) };
  } catch (exception) {
    if (ticket === detailGeneration) error.value = exception.message;
  } finally {
    if (ticket === detailGeneration) detailBusy.value = false;
  }
}

watch(account, () => {
  ++detailGeneration;
  selected.value = null;
  detail.value = null;
  load();
});
onBeforeUnmount(() => { ++generation; ++detailGeneration; });
const refresh = usePendingRefresh(busy, load);
defineExpose({ refresh: () => mode.value === "system" ? audit.value?.refresh() : refresh() });
</script>

<template>
  <section aria-label="Events" class="entity-view">
    <div class="view-heading">
      <div><h2>Events</h2><p>Inspect WhatsApp provider events and Hub runtime activity.</p></div>
    </div>
    <div class="entity-tabs" role="tablist" aria-label="Event views">
      <button type="button" role="tab" :aria-selected="mode === 'messages'" :class="{ active: mode === 'messages' }" @click="mode = 'messages'">WhatsApp events</button>
      <button type="button" role="tab" :aria-selected="mode === 'system'" :class="{ active: mode === 'system' }" @click="mode = 'system'">System activity</button>
    </div>
    <AuditEvents ref="audit" v-if="mode === 'system'" />
    <template v-else>
      <div class="entity-toolbar">
        <AccountPicker v-model="account" :disabled="busy" />
        <FormControl v-model="query" label="Search loaded events" type="search" placeholder="ID, preview, type or forwarding state" />
        <Button label="Refresh" icon-left="refresh-cw" :loading="busy" :disabled="!account" @click="load()" />
      </div>
      <ErrorMessage :message="error" />
      <p v-if="!account" class="entity-prompt">Select an account to inspect its WhatsApp events.</p>
      <div v-else class="event-list infinite-list" aria-label="WhatsApp events" @scroll.passive="scrolled">
        <button v-for="row in visible" :key="row.id" type="button" class="event-list-row entity-row" @click="inspect(row)">
          <span class="entity-primary"><span class="entity-icon" aria-hidden="true"><FeatherIcon name="activity" class="h-4 w-4" /></span><span><strong>{{ row.kind || `Event #${row.id}` }}</strong><small>{{ row.time }}</small></span></span>
          <span class="event-preview">{{ row.preview || "Provider event" }}</span>
          <Badge :label="row.callback || 'Pending'" />
          <FeatherIcon name="chevron-right" class="h-4 w-4 text-ink-gray-4" />
        </button>
        <p v-if="busy" role="status" class="infinite-list__status">Loading…</p>
        <p v-else-if="!visible.length && !error" class="entity-prompt">No matching events in the loaded page. Load older events to search more history.</p>
      </div>
      <Button v-if="more" label="Load older events" :loading="busy" @click="load(true)" />
    </template>

    <Dialog :model-value="!!selected" :options="{ title: selected ? `WhatsApp event #${selected.id}` : 'WhatsApp event', size: 'xl' }" @update:model-value="(value) => !value && (selected = null)">
      <template #body-content>
        <div v-if="selected" class="space-y-5">
          <ErrorMessage :message="error" />
          <dl class="entity-details">
            <div><dt>Type</dt><dd>{{ selected.kind }}</dd></div>
            <div><dt>Received</dt><dd>{{ selected.time }}</dd></div>
            <div><dt>Core callback</dt><dd>{{ selected.callback }}</dd></div>
          </dl>
          <p class="text-sm text-ink-gray-6">A forwarded callback confirms Core stored the event. Customer delivery and read state appear separately.</p>
          <p v-if="detailBusy" role="status" class="text-sm text-ink-gray-5">Loading payload…</p>
          <template v-else-if="detail">
            <p v-if="detail.truncated" role="status" class="text-sm text-ink-gray-6">Showing the first 65,536 characters. Hub retains the complete payload.</p>
            <pre class="event-payload">{{ detail.text }}</pre>
          </template>
        </div>
      </template>
      <template #actions="{ close }"><Button label="Close" @click="close" /></template>
    </Dialog>
  </section>
</template>
