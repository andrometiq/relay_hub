<script setup>
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { Badge, Button, Dialog, ErrorMessage, FeatherIcon } from "@relay/ui";
import SiteAccountPicker from "./SiteAccountPicker.vue";
import SiteDialog from "./SiteDialog.vue";
import { adminApi, assignedRefs, channelAddress, channelStatus, hubURL, provider, siteStatus, siteTokens, tokenState } from "../../composables/containers";

const props = defineProps({ id: String });
const emit = defineEmits(["title"]);
const site = ref(null), containers = ref([]), sites = ref([]), tokens = ref({ tokens: [], more: false });
const busy = ref(false), error = ref(""), tokenError = ref(""), notice = ref("");
const editing = ref(""), picking = ref(false), removing = ref(null), removeBusy = ref(false), removeError = ref("");
let ticket = 0;

const secrets = computed(() => site.value?.configured_secrets || []);
const containerName = (id) => containers.value.find((row) => row.id === id)?.name || id;
const expiry = (token) => token.Expires === "Never" ? "no expiry" : "until " + new Date(token.Expires).toLocaleDateString();

async function refresh() {
  const current = ++ticket;
  busy.value = true; error.value = ""; tokenError.value = "";
  try {
    const [row, containerRows, siteRows] = await Promise.all([adminApi.site(props.id), adminApi.containers(), adminApi.sites()]);
    if (current !== ticket) return;
    site.value = row; containers.value = containerRows; sites.value = siteRows;
    emit("title", row.name || row.id);
  } catch (exception) {
    if (current === ticket) error.value = exception.message;
  } finally {
    if (current === ticket) busy.value = false;
  }
  try {
    const result = await siteTokens(props.id);
    if (current === ticket) tokens.value = result;
  } catch (exception) {
    if (current === ticket) tokenError.value = exception.message;
  }
}

async function copyKey() {
  notice.value = "";
  try { await navigator.clipboard.writeText(site.value.core_api_key); notice.value = "Core API key copied."; }
  catch { notice.value = "Copy failed. Select the key and copy it."; }
}

async function remove() {
  removeBusy.value = true; removeError.value = "";
  try {
    await adminApi.assignChannels(props.id, assignedRefs(site.value.channels, [], removing.value));
    removing.value = null;
    await refresh();
  } catch (exception) {
    removeError.value = exception.message;
  } finally {
    removeBusy.value = false;
  }
}

watch(() => props.id, () => { site.value = null; refresh(); }, { immediate: true });
onBeforeUnmount(() => { ticket++; });
defineExpose({ refresh });
</script>

<template>
  <section :aria-label="'Site ' + id" class="entity-view">
    <ErrorMessage :message="error" />
    <p v-if="busy && !site" role="status">Loading site…</p>
    <template v-if="site">
      <div class="admin-heading">
        <span class="admin-big-icon" aria-hidden="true"><FeatherIcon name="globe" :size="20" /></span>
        <div><h2>{{ site.name || site.id }}</h2><p>{{ site.origin }} · <Badge :label="siteStatus(site).label" :theme="siteStatus(site).theme" /></p></div>
        <div class="admin-actions"><Button label="Edit site" icon-left="pencil" @click="editing = 'site'" /></div>
      </div>

      <div class="admin-section"><h3>Linked accounts</h3><span>Messages to these accounts go to this site</span><Button label="Add account" icon-left="plus" variant="solid" @click="picking = true" /></div>
      <div v-if="site.channels.length" class="admin-table admin-spaced">
        <div class="admin-head cols-linked" aria-hidden="true"><span></span><span>Account</span><span>Number / bot / mailbox</span><span>Container</span><span>Status</span><span></span></div>
        <div v-for="row in site.channels" :key="row.channel + row.key" class="admin-row cols-linked">
          <span class="hide-m entity-icon admin-type" aria-hidden="true"><FeatherIcon :name="provider(row.channel).icon" :class="'provider-' + provider(row.channel).tone" /></span>
          <span class="entity-primary"><strong>{{ row.label || row.key }}</strong></span>
          <span class="wide-m">{{ channelAddress(row) || row.key }}</span>
          <span class="hide-m admin-muted">{{ containerName(row.container) }}</span>
          <span class="hide-m admin-stat"><Badge :label="channelStatus(row).label" :theme="channelStatus(row).theme" /></span>
          <span><Button label="Remove" icon-left="unlink" variant="ghost" :aria-label="'Remove ' + (row.label || row.key)" @click="removing = row; removeError = ''" /></span>
        </div>
      </div>
      <p v-else class="entity-prompt admin-spaced">No accounts linked. Add a WhatsApp number, Telegram bot or mailbox from a container.</p>

      <div class="admin-grid2">
        <div class="admin-card">
          <div class="admin-card-head"><div><h3>Core connection</h3><p>Paste these in Relay Core → Settings on this site.</p></div></div>
          <div class="admin-secret"><span class="admin-label">Site link</span><span class="admin-value">{{ site.origin }}</span></div>
          <div class="admin-secret">
            <span class="admin-label">Core API key</span>
            <span class="admin-value" :class="{ 'admin-mono': site.core_api_key }">{{ site.core_api_key || (secrets.includes("core_api_key") ? "Set" : "Not set") }}</span>
            <Button v-if="site.core_api_key" label="Copy" icon-left="copy" @click="copyKey" />
            <Button v-else label="Replace" icon-left="refresh-cw" @click="editing = 'core_api_key'" />
          </div>
          <div class="admin-secret"><span class="admin-label">Core API secret</span><span class="admin-value">{{ secrets.includes("core_api_secret") ? "Set" : "Not set" }}</span><Button label="Replace" icon-left="refresh-cw" @click="editing = 'core_api_secret'" /></div>
          <div class="admin-secret"><span class="admin-label">Callback secret</span><span class="admin-value">{{ secrets.includes("callback_secret") ? "Set" : "Not set" }}</span><Button label="Rotate" icon-left="refresh-cw" @click="editing = 'callback_secret'" /></div>
          <div class="admin-secret"><span class="admin-label">Last callback</span><span class="admin-value">{{ site.last_callback_at ? new Date(site.last_callback_at).toLocaleString() : "Not reported" }}</span></div>
          <p v-if="notice" role="status" class="admin-hint">{{ notice }}</p>
        </div>
        <div class="admin-card">
          <div class="admin-card-head"><div><h3>Access tokens</h3><p>MCP and API tokens. A token sees only this site's accounts.</p></div><div class="admin-actions"><a :href="hubURL('/auth/tokens')" class="relay-button relay-button--subtle relay-button--gray relay-button--sm"><FeatherIcon name="plus" /><span class="relay-button__label">New token</span></a></div></div>
          <ErrorMessage :message="tokenError" />
          <div v-for="token in tokens.tokens" :key="token.ID" class="admin-secret">
            <span class="admin-label">{{ token.Name }}</span>
            <span class="admin-value">{{ tokenState(token) === "Revoked" ? "revoked" : expiry(token) }}</span>
            <Badge :label="tokenState(token)" :theme="tokenState(token) === 'Active' ? 'green' : 'gray'" />
          </div>
          <p v-if="!tokens.tokens.length && !tokenError" class="admin-hint">You have no auth keys for this site.</p>
          <p v-if="tokens.more" class="admin-hint">More keys exist. Open Auth keys to see all.</p>
        </div>
      </div>

      <SiteDialog :model-value="!!editing" :site="site" :only="editing === 'site' ? '' : editing" @update:model-value="editing = $event ? editing : ''" @saved="refresh" />
      <SiteAccountPicker v-model="picking" :site="site" :containers="containers" :sites="sites" @saved="refresh" />
      <Dialog :model-value="!!removing" :options="{ title: 'Remove account', size: 'sm' }" :disable-outside-click-to-close="removeBusy" @update:model-value="removing = $event ? removing : null">
        <template #body-content>
          <p class="text-sm">Remove <strong>{{ removing?.label || removing?.key }}</strong> from {{ site.name || site.id }}? New messages to it stay in the Hub as unassigned until you link it to a site.</p>
          <ErrorMessage :message="removeError" />
        </template>
        <template #actions="{ close }"><Button label="Cancel" :disabled="removeBusy" @click="close" /><Button label="Remove" variant="solid" :loading="removeBusy" @click="remove" /></template>
      </Dialog>
    </template>
  </section>
</template>
