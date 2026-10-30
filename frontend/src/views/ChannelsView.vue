<script setup>
import { computed, ref } from "vue";
import { Badge, Button, Dialog, ErrorMessage, FeatherIcon, FormControl } from "@relay/ui";
import ChannelConnection from "../components/ChannelConnection.vue";
import AccountHealth from "../components/AccountHealth.vue";
import InfiniteList from "../components/InfiniteList.vue";
import { hubApi } from "../services/hubApi";
import { adminApi, channelStatus, connectionRows, provider } from "../composables/containers";
import { whatsappHealth } from "../composables/whatsappHealth";

defineEmits(["navigate"]);
const list = ref();
const query = ref("");
const selected = ref(null);
const metadata = ref(null);
const metadataError = ref("");
const metadataBusy = ref(false);
const config = computed(() => selected.value?.config || {});
let request = 0;

async function loadChannels({ direction, q }) {
  if (direction === "before") return { items: [], more: false };
  const [containers, sites] = await Promise.all([adminApi.containers(), adminApi.sites()]);
  return { items: connectionRows(containers, sites, q), more: false };
}

async function open(row) {
  selected.value = row;
  metadata.value = null;
  metadataError.value = "";
  const current = ++request;
  metadataBusy.value = false;
  if (row.channel !== "WhatsApp") return;
  metadataBusy.value = true;
  try {
    const result = await hubApi("accounts/metadata?" + new URLSearchParams({ account: row.key }));
    if (current === request) metadata.value = result;
  } catch (error) {
    if (current === request) metadataError.value = error.message;
  } finally {
    if (current === request) metadataBusy.value = false;
  }
}
defineExpose({ refresh: () => list.value?.reload() });
</script>

<template>
  <section aria-label="Channel connections" class="entity-view">
    <div class="view-heading">
      <div>
        <h2>Connections</h2>
        <p>Provider readiness and metadata sync for every number, bot and mailbox.</p>
      </div>
      <Button label="Manage containers" @click="$emit('navigate', 'Containers')" />
    </div>
    <div class="entity-toolbar">
      <FormControl v-model="query" type="search" placeholder="Search connections" aria-label="Search connections" autocomplete="off" />
    </div>
    <div class="channel-list-head entity-list-head" aria-hidden="true">
      <span>Channel</span><span>Site</span><span>Connection</span><span>Container</span><span></span>
    </div>
    <InfiniteList ref="list" :load="loadChannels" :query="query" :row-height="64">
      <template #default="{ row }">
        <button type="button" class="channel-list-row entity-row" :aria-label="'Open ' + row.channel + ' connection ' + (row.label || row.key)" @click="open(row)">
          <span class="entity-primary">
            <span class="entity-icon" aria-hidden="true"><FeatherIcon :name="provider(row.channel).icon" class="h-4 w-4" /></span>
            <span><strong>{{ row.label || row.key }}</strong><small class="block text-ink-gray-5">{{ row.address || row.key }}</small></span>
          </span>
          <span class="entity-site">{{ row.siteName || "Not linked" }}</span>
          <span><Badge :label="channelStatus(row).label" :theme="channelStatus(row).theme" /></span>
          <span><Badge :label="row.containerName" theme="gray" /></span>
          <FeatherIcon name="chevron-right" class="h-4 w-4 text-ink-gray-4" />
        </button>
      </template>
    </InfiniteList>

    <Dialog
      :model-value="!!selected"
      :options="{ title: (selected?.label || selected?.key) + ' · Connections', size: 'lg' }"
      @update:model-value="(value) => !value && (selected = null)"
      @close="++request; selected = null;"
    >
      <template #body-content>
        <div v-if="selected" class="space-y-5">
          <ChannelConnection v-if="selected.channel !== 'WhatsApp'" :key="selected.id" :account="selected.key" :channel="selected.channel" />
          <template v-else>
            <dl class="entity-details">
              <div><dt>Credentials verified</dt><dd>{{ config.connection_status === "connected" ? config.verified_at || "Verified" : "Not verified" }}</dd></div>
              <div><dt>Site</dt><dd>{{ selected.siteName || "Not linked" }}</dd></div>
              <div><dt>Container</dt><dd>{{ selected.containerName }}</dd></div>
              <div><dt>Automatic sync</dt><dd>{{ config.sync_metadata ? "Every hour" : "Off" }}</dd></div>
            </dl>
            <AccountHealth :account="selected.key" />
            <ErrorMessage :message="metadataError" />
            <p v-if="metadataBusy" role="status" class="text-sm text-ink-gray-5">Checking Meta sync…</p>
            <dl v-else-if="metadata" class="entity-details">
              <div><dt>Display number</dt><dd>{{ metadata.profile?.display_phone_number || "Not cached yet" }}</dd></div>
              <div><dt>Verified name</dt><dd>{{ metadata.profile?.verified_name || "Not cached yet" }}</dd></div>
              <div v-for="row in whatsappHealth(metadata.profile)" :key="row.name"><dt>{{ row.name }}</dt><dd><Badge v-if="row.theme" :label="row.label" :theme="row.theme" /><template v-else>{{ row.text }}</template></dd></div>
              <div><dt>Sync status</dt><dd>{{ metadata.status }}</dd></div>
              <div><dt>Last successful sync</dt><dd>{{ metadata.last_success || "Not synced yet" }}</dd></div>
              <div><dt>Cached templates</dt><dd>{{ metadata.templates?.length || 0 }}{{ metadata.truncated ? " (cache limit reached)" : "" }}</dd></div>
              <div v-if="metadata.last_error"><dt>Last issue</dt><dd>{{ metadata.last_error }}</dd></div>
            </dl>
          </template>
        </div>
      </template>
      <template #actions="{ close }">
        <div class="flex justify-end gap-2">
          <Button label="Close" @click="close" />
          <Button label="Open containers" variant="solid" @click="close(); $emit('navigate', 'Containers');" />
        </div>
      </template>
    </Dialog>
  </section>
</template>
