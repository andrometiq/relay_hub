<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { Badge, Button, ErrorMessage, FeatherIcon, TextInput } from "@relay/ui";
import SiteDialog from "./SiteDialog.vue";
import { PROVIDERS, adminApi, countByProvider, plural, siteStatus } from "../../composables/containers";

const emit = defineEmits(["open"]);
const rows = ref([]), containers = ref([]), query = ref(""), busy = ref(false), error = ref(""), creating = ref(false);
const unlinked = computed(() => containers.value.flatMap((row) => row.channels).filter((row) => !row.site).length);
let ticket = 0, timer;

async function refresh() {
  const current = ++ticket;
  busy.value = true; error.value = "";
  try {
    const [siteRows, containerRows] = await Promise.all([adminApi.sites(query.value.trim().slice(0, 80)), adminApi.containers()]);
    if (current !== ticket) return;
    rows.value = siteRows; containers.value = containerRows;
  } catch (exception) {
    if (current === ticket) error.value = exception.message;
  } finally {
    if (current === ticket) busy.value = false;
  }
}
function search() { clearTimeout(timer); timer = setTimeout(refresh, 250); }

onMounted(refresh);
onBeforeUnmount(() => { clearTimeout(timer); ticket++; });
defineExpose({ refresh });
</script>

<template>
  <section aria-label="Sites" class="entity-view">
    <div class="view-heading">
      <div><h2>Sites</h2><p>A site is one Frappe site with Relay Core. Link the accounts that belong to it. One account belongs to one site only.</p></div>
      <Button label="New site" variant="solid" icon-left="plus" @click="creating = true" />
    </div>
    <div class="entity-toolbar">
      <div><TextInput v-model="query" type="search" placeholder="Search sites" aria-label="Search sites" autocomplete="off" @update:model-value="search"><template #prefix><FeatherIcon name="search" /></template></TextInput></div>
      <span>{{ plural(rows.length, "site") }} · {{ plural(unlinked, "account") }} not linked</span>
    </div>
    <ErrorMessage :message="error" />
    <p v-if="busy && !rows.length" role="status">Loading sites…</p>
    <div v-if="rows.length" class="admin-table">
      <div class="admin-head cols-sites" aria-hidden="true"><span>Site</span><span>Site link</span><span>Linked accounts</span><span>Tokens</span><span>Status</span><span></span></div>
      <button v-for="row in rows" :key="row.id" type="button" class="admin-row cols-sites" :aria-label="'Open site ' + (row.name || row.id)" @click="emit('open', row.id)">
        <span class="entity-primary"><span class="entity-icon" aria-hidden="true"><FeatherIcon name="globe" /></span><span><strong>{{ row.name || row.id }}</strong><small class="admin-mono">{{ row.id }}</small></span></span>
        <span class="hide-m">{{ row.origin }}</span>
        <span class="wide-m admin-badges">
          <template v-for="item in PROVIDERS" :key="item.channel">
            <span v-if="countByProvider(row.channels)[item.channel]" class="admin-count"><FeatherIcon :name="item.icon" :size="12" :class="'provider-' + item.tone" />{{ countByProvider(row.channels)[item.channel] }} {{ item.channel }}</span>
          </template>
          <span v-if="!row.channels.length" class="admin-muted">No accounts linked</span>
        </span>
        <span class="hide-m">{{ row.active_tokens == null ? "—" : row.active_tokens + " active" }}</span>
        <span class="hide-m admin-stat"><Badge :label="siteStatus(row).label" :theme="siteStatus(row).theme" /></span>
        <FeatherIcon name="chevron-right" class="admin-chev" />
      </button>
    </div>
    <p v-else-if="!busy && !error" class="entity-prompt">No sites found. Create a site for each Frappe site that runs Relay Core.</p>
    <SiteDialog v-model="creating" @saved="emit('open', $event)" />
  </section>
</template>
