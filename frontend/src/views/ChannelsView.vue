<script setup>
import { ref } from "vue";
import {
  Badge,
  Button,
  Dialog,
  ErrorMessage,
  FormControl,
  FeatherIcon,
} from "@relay/ui";
import ChannelConnection from "../components/ChannelConnection.vue";
import AccountHealth from "../components/AccountHealth.vue";
import InfiniteList from "../components/InfiniteList.vue";
import { hubApi } from "../services/hubApi";
import { CHANNELS } from "../composables/accountForm";

defineEmits(["navigate"]);
const list = ref();
const query = ref("");
const selected = ref(null);
const selectedChannel = ref("WhatsApp");
const metadata = ref(null);
const metadataError = ref("");
const metadataBusy = ref(false);
let request = 0;

async function loadChannels({ cursor, direction, q }) {
  const parameters = new URLSearchParams({ query: q.slice(0, 80) });
  if (cursor)
    parameters.set(direction === "before" ? "before" : "after", cursor);
  const data = await hubApi("accounts?" + parameters);
  return {
    items: (data.accounts || []).map((account) => {
      const channel = (account.channels || []).find(
        (item) => item.channel === "WhatsApp",
      );
      return {
        channels: (account.channels || []).filter((item) => CHANNELS.includes(item.channel)),
        id: account.id,
        account: account.id,
        site: account.site,
        enabled: !!account.enabled && (account.channels || []).some(item => item.enabled && CHANNELS.includes(item.channel)),
        configured: !!channel,
        sync: !!channel?.config?.sync_metadata,
        verified: channel?.config?.connection_status === "connected",
        verifiedAt: channel?.config?.verified_at,
      };
    }),
    more: direction === "before" ? !!data.previous : !!data.next,
  };
}

async function open(row) {
  selected.value = row;
  selectedChannel.value = row.channels.find(channel => channel.enabled)?.channel || row.channels[0]?.channel || "WhatsApp";
  metadata.value = null;
  metadataError.value = "";
  const current = ++request;
  metadataBusy.value = false;
  if (!row.configured) return;
  metadataBusy.value = true;
  try {
    const result = await hubApi(
      "accounts/metadata?" + new URLSearchParams({ account: row.account }),
    );
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
        <p>Provider readiness and metadata sync for every Relay account.</p>
      </div>
      <Button label="Manage accounts" @click="$emit('navigate', 'Accounts')" />
    </div>
    <div class="entity-toolbar">
      <FormControl
        v-model="query"
        type="search"
        placeholder="Search connections"
        aria-label="Search connections"
        autocomplete="off"
      />
    </div>
    <div class="channel-list-head entity-list-head" aria-hidden="true">
      <span>Account</span><span>Site</span><span>Connection</span
      ><span>Channels</span><span></span>
    </div>
    <InfiniteList ref="list" :load="loadChannels" :query="query" :row-height="64">
      <template #default="{ row }">
        <button
          type="button"
          class="channel-list-row entity-row"
          :aria-label="'Open connection ' + row.account"
          @click="open(row)"
        >
          <span class="entity-primary">
            <span class="entity-icon" aria-hidden="true"
              ><FeatherIcon name="message-circle" class="h-4 w-4"
            /></span>
            <strong>{{ row.account }}</strong>
          </span>
          <span class="entity-site">{{ row.site }}</span>
          <span
            ><Badge
              :label="
                row.enabled
                  ? 'Enabled'
                  : row.channels.length
                    ? 'Disabled'
                    : 'Not configured'
              "
              :theme="row.enabled ? 'green' : 'gray'"
          /></span>
          <span
            ><Badge
              :label="row.channels.map(item => item.channel).join(' · ') || 'None'"
              theme="gray"
          /></span>
          <FeatherIcon name="chevron-right" class="h-4 w-4 text-ink-gray-4" />
        </button>
      </template>
    </InfiniteList>

    <Dialog
      :model-value="!!selected"
      :options="{ title: selected?.account + ' · Connections', size: 'lg' }"
      @update:model-value="(value) => !value && (selected = null)"
      @close="
        ++request;
        selected = null;
      "
    >
      <template #body-content>
        <div v-if="selected" class="space-y-5">
          <FormControl v-if="selected.channels.length > 1" label="Channel" type="select" :options="selected.channels.map(item => item.channel)" v-model="selectedChannel" />
          <ChannelConnection v-if="selectedChannel !== 'WhatsApp'" :key="selected.account + selectedChannel" :account="selected.account" :channel="selectedChannel" />
          <template v-else>
          <dl class="entity-details">
            <div><dt>Credentials verified</dt><dd>{{ selected.verified ? selected.verifiedAt || "Verified" : "Not verified" }}</dd></div>
            <div><dt>Frappe site</dt><dd>{{ selected.site }}</dd></div>
            <div>
              <dt>Connection</dt>
              <dd>{{ selected.enabled ? "Enabled" : "Disabled" }}</dd>
            </div>
            <div>
              <dt>Automatic sync</dt>
              <dd>{{ selected.sync ? "Every hour" : "Off" }}</dd>
            </div>
          </dl>
          <AccountHealth :account="selected.account" />
          <ErrorMessage :message="metadataError" />
          <p v-if="metadataBusy" role="status" class="text-sm text-ink-gray-5">
            Checking Meta sync…
          </p>
          <dl v-else-if="metadata" class="entity-details">
            <div><dt>Display number</dt><dd>{{ metadata.profile?.display_phone_number || 'Not cached yet' }}</dd></div>
            <div><dt>Verified name</dt><dd>{{ metadata.profile?.verified_name || 'Not cached yet' }}</dd></div>
            <div><dt>Quality rating</dt><dd>{{ metadata.profile?.quality_rating || 'Not cached yet' }}</dd></div>
            <div><dt>Sync status</dt><dd>{{ metadata.status }}</dd></div>
            <div>
              <dt>Last successful sync</dt>
              <dd>{{ metadata.last_success || "Not synced yet" }}</dd>
            </div>
            <div>
              <dt>Cached templates</dt>
              <dd>
                {{ metadata.templates?.length || 0
                }}{{ metadata.truncated ? " (cache limit reached)" : "" }}
              </dd>
            </div>
            <div v-if="metadata.last_error">
              <dt>Last issue</dt><dd>{{ metadata.last_error }}</dd>
            </div>
          </dl>
          <p v-else-if="!selected.configured" class="text-sm text-ink-gray-5">
            Open Accounts to configure WhatsApp for this site.
          </p>
          </template>
        </div>
      </template>
      <template #actions="{ close }">
        <div class="flex justify-end gap-2">
          <Button label="Close" @click="close" />
          <Button
            label="Edit account"
            variant="solid"
            @click="
              close();
              $emit('navigate', 'Accounts');
            "
          />
        </div>
      </template>
    </Dialog>
  </section>
</template>
