<script setup>
import { onBeforeUnmount, onMounted, ref } from "vue";
import { Badge, Button, ErrorMessage, FeatherIcon, TextInput } from "@relay/ui";
import ContainerDialog from "./ContainerDialog.vue";
import { PROVIDERS, adminApi, countByProvider, plural } from "../../composables/containers";

const emit = defineEmits(["open"]);
const rows = ref([]), query = ref(""), busy = ref(false), error = ref(""), creating = ref(false);
let ticket = 0, timer;

async function refresh() {
  const current = ++ticket;
  busy.value = true; error.value = "";
  try {
    const result = await adminApi.containers(query.value.trim().slice(0, 80));
    if (current === ticket) rows.value = result;
  } catch (exception) {
    if (current === ticket) error.value = exception.message;
  } finally {
    if (current === ticket) busy.value = false;
  }
}
function search() { clearTimeout(timer); timer = setTimeout(refresh, 250); }
const sites = (container) => new Set(container.channels.map((row) => row.site).filter(Boolean)).size;

onMounted(refresh);
onBeforeUnmount(() => { clearTimeout(timer); ticket++; });
defineExpose({ refresh });
</script>

<template>
  <section aria-label="Containers" class="entity-view">
    <div class="view-heading">
      <div><h2>Containers</h2><p>A container holds your provider accounts: WhatsApp numbers, Telegram bots and mailboxes. Link each account to one site on the Sites page.</p></div>
      <Button label="New container" variant="solid" icon-left="plus" @click="creating = true" />
    </div>
    <div class="entity-toolbar">
      <div><TextInput v-model="query" type="search" placeholder="Search containers" aria-label="Search containers" autocomplete="off" @update:model-value="search"><template #prefix><FeatherIcon name="search" /></template></TextInput></div>
      <span>{{ plural(rows.length, "container") }}</span>
    </div>
    <ErrorMessage :message="error" />
    <p v-if="busy && !rows.length" role="status">Loading containers…</p>
    <div v-if="rows.length" class="admin-table">
      <div class="admin-head cols-containers" aria-hidden="true"><span>Container</span><span>Accounts</span><span>Linked to sites</span><span>Status</span><span></span></div>
      <button v-for="row in rows" :key="row.id" type="button" class="admin-row cols-containers" :aria-label="'Open container ' + (row.name || row.id)" @click="emit('open', row.id)">
        <span class="entity-primary"><span class="entity-icon" aria-hidden="true"><FeatherIcon name="box" /></span><span><strong>{{ row.name || row.id }}</strong><small class="admin-mono">{{ row.id }}</small></span></span>
        <span class="wide-m admin-badges">
          <template v-for="item in PROVIDERS" :key="item.channel">
            <span v-if="countByProvider(row.channels)[item.channel]" class="admin-count"><FeatherIcon :name="item.icon" :size="12" :class="'provider-' + item.tone" />{{ countByProvider(row.channels)[item.channel] }} {{ item.channel }}</span>
          </template>
          <span v-if="!row.channels.length" class="admin-muted">No accounts yet</span>
        </span>
        <span class="hide-m">{{ plural(sites(row), "site") }}</span>
        <span class="hide-m admin-stat"><Badge :label="row.enabled ? 'Enabled' : 'Disabled'" :theme="row.enabled ? 'green' : 'gray'" /></span>
        <FeatherIcon name="chevron-right" class="admin-chev" />
      </button>
    </div>
    <p v-else-if="!busy && !error" class="entity-prompt">No containers found. Create a container to add WhatsApp numbers, Telegram bots and mailboxes.</p>
    <ContainerDialog v-model="creating" mode="create" @saved="emit('open', $event)" />
  </section>
</template>
