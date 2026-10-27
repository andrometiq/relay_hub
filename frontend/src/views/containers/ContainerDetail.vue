<script setup>
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { Badge, Button, ErrorMessage, FeatherIcon } from "@relay/ui";
import ChannelDialog from "./ChannelDialog.vue";
import ContainerDialog from "./ContainerDialog.vue";
import { PROVIDERS, adminApi, channelAddress, channelStatus, containerReady, countByProvider, hubURL, plural, provider, siteLabel } from "../../composables/containers";

const props = defineProps({ id: String });
const emit = defineEmits(["title"]);
const container = ref(null), sites = ref([]), busy = ref(false), error = ref(""), copyNotice = ref("");
const tab = ref("WhatsApp"), dialog = ref(""), editing = ref(null), adding = ref(false);
let ticket = 0;

const channels = computed(() => (container.value?.channels || []).filter((row) => row.channel === tab.value));
const counts = computed(() => countByProvider(container.value?.channels));
const current = computed(() => provider(tab.value));
const unlinked = computed(() => channels.value.filter((row) => !row.site).length);
const app = computed(() => container.value?.whatsapp || { config: {}, configured_secrets: [] });
const appSecrets = computed(() => app.value.configured_secrets || []);
const callbackURL = computed(() => hubURL("/webhooks/whatsapp/" + encodeURIComponent(props.id)));

async function refresh() {
  const currentTicket = ++ticket;
  busy.value = true; error.value = "";
  try {
    const [row, siteRows] = await Promise.all([adminApi.container(props.id), adminApi.sites()]);
    if (currentTicket !== ticket) return;
    container.value = row; sites.value = siteRows;
    emit("title", row.name || row.id);
  } catch (exception) {
    if (currentTicket === ticket) error.value = exception.message;
  } finally {
    if (currentTicket === ticket) busy.value = false;
  }
}

async function copy(label, read) {
  copyNotice.value = "";
  try {
    await navigator.clipboard.writeText(await read());
    copyNotice.value = label + " copied.";
  } catch (exception) {
    copyNotice.value = `${label} could not be copied. ${exception.message || ""}`.trim();
  }
}
const copyVerifyToken = () => copy("Verify token", async () => (await adminApi.containerWebhook(props.id)).verify_token);

function openChannel(row) { editing.value = row; adding.value = true; }
function addChannel() { editing.value = null; adding.value = true; }

watch(() => props.id, () => { container.value = null; refresh(); }, { immediate: true });
onBeforeUnmount(() => { ticket++; });
defineExpose({ refresh });
</script>

<template>
  <section :aria-label="'Container ' + id" class="entity-view">
    <ErrorMessage :message="error" />
    <p v-if="busy && !container" role="status">Loading container…</p>
    <template v-if="container">
      <div class="admin-heading">
        <span class="admin-big-icon" aria-hidden="true"><FeatherIcon name="box" :size="20" /></span>
        <div><h2>{{ container.name || container.id }}</h2><p><span class="admin-mono">{{ container.id }}</span> · {{ plural(container.channels.length, "account") }} · <Badge :label="container.enabled ? 'Enabled' : 'Disabled'" :theme="container.enabled ? 'green' : 'gray'" /></p></div>
        <div class="admin-actions"><Button label="Rename" icon-left="pencil" @click="dialog = 'rename'" /></div>
      </div>

      <div class="entity-tabs admin-tabs" role="tablist" aria-label="Account types">
        <button v-for="item in PROVIDERS" :key="item.channel" type="button" role="tab" :aria-selected="tab === item.channel" :class="{ active: tab === item.channel }" @click="tab = item.channel">
          <FeatherIcon :name="item.icon" :class="'provider-' + item.tone" />{{ item.channel }}<span class="admin-tab-count">{{ counts[item.channel] }}</span>
        </button>
      </div>

      <div v-if="tab === 'WhatsApp'" class="admin-card">
        <div class="admin-card-head">
          <div><h3>Meta app and webhook</h3><p>Paste this URL and verify token in Meta App Dashboard → WhatsApp → Configuration. All numbers below use it.</p></div>
          <div class="admin-actions"><Badge :label="containerReady(container) ? 'Webhook ready' : 'Setup needed'" :theme="containerReady(container) ? 'green' : 'orange'" /><Button label="Edit" @click="dialog = 'app'" /></div>
        </div>
        <dl class="admin-kv">
          <div><dt>App ID</dt><dd class="admin-mono">{{ app.config.app_id || "Not set" }}</dd></div>
          <div><dt>App secret</dt><dd>{{ appSecrets.includes("app_secret") ? "Set" : "Not set" }}</dd></div>
          <div><dt>Graph API version</dt><dd>{{ app.config.graph_version || "Not set" }}</dd></div>
          <div><dt>Last event</dt><dd>{{ app.last_event_at ? new Date(app.last_event_at).toLocaleString() : "Not reported" }}</dd></div>
        </dl>
        <div class="webhook-address"><div><span>Callback URL</span><code>{{ callbackURL }}</code></div><Button label="Copy" icon-left="copy" @click="copy('Callback URL', () => callbackURL)" /></div>
        <div class="webhook-address"><div><span>Verify token</span><code>{{ appSecrets.includes("verify_token") ? "••••••••••••••••" : "Not set" }}</code></div><Button label="Copy" icon-left="copy" :disabled="!appSecrets.includes('verify_token')" @click="copyVerifyToken" /></div>
        <p v-if="copyNotice" role="status" class="admin-hint">{{ copyNotice }}</p>
      </div>
      <div v-else-if="tab === 'Telegram'" class="admin-card">
        <div class="admin-card-head"><div><h3>Telegram webhooks</h3><p>The Hub registers each bot's webhook with Telegram when you save the bot. Nothing to paste.</p></div></div>
        <div class="webhook-address"><div><span>Webhook URL pattern</span><code>{{ hubURL("/webhooks/telegram/{bot key}") }}</code></div></div>
      </div>
      <div v-else class="admin-card">
        <div class="admin-card-head"><div><h3>Inbound mail</h3><p>The Hub reads each mailbox by IMAP. If your mail provider can forward by webhook, use the mailbox URL below instead.</p></div></div>
        <div class="webhook-address"><div><span>Inbound webhook URL pattern</span><code>{{ hubURL("/webhooks/email/{mailbox key}") }}</code></div></div>
      </div>

      <div class="admin-section">
        <h3>{{ tab === "Email" ? "Mailboxes" : tab + " " + current.plural }}</h3>
        <span>{{ plural(channels.length, current.noun, current.plural) }}<template v-if="unlinked"> · {{ unlinked }} not linked</template></span>
        <Button :label="current.add" icon-left="plus" variant="solid" @click="addChannel" />
      </div>
      <div v-if="channels.length" class="admin-table">
        <div v-if="tab === 'WhatsApp'" class="admin-head cols-wa" aria-hidden="true"><span>Account</span><span>Number</span><span>Phone ID · WABA</span><span>Linked site</span><span>Status</span><span></span></div>
        <div v-else-if="tab === 'Telegram'" class="admin-head cols-tg" aria-hidden="true"><span>Account</span><span>Bot</span><span>Webhook</span><span>Linked site</span><span>Status</span><span></span></div>
        <div v-else class="admin-head cols-em" aria-hidden="true"><span>Mailbox</span><span>SMTP (send)</span><span>IMAP (receive)</span><span>Linked site</span><span>Status</span><span></span></div>
        <button v-for="row in channels" :key="row.key" type="button" class="admin-row" :class="'cols-' + current.tone" :aria-label="'Open ' + (row.label || row.key)" @click="openChannel(row)">
          <span class="entity-primary">
            <span class="entity-icon" aria-hidden="true"><FeatherIcon :name="current.icon" :class="'provider-' + current.tone" /></span>
            <span v-if="tab === 'Email'"><strong>{{ row.config.from || row.address }}</strong><small>{{ row.label }}</small></span>
            <strong v-else>{{ row.label || row.key }}</strong>
          </span>
          <template v-if="tab === 'WhatsApp'">
            <span>{{ channelAddress(row) || "No display number" }}<small v-if="row.config.verified_name">{{ row.config.verified_name }}</small></span>
            <span class="hide-m admin-mono">{{ row.config.phone_id }}<small>WABA {{ row.config.waba_id }}</small></span>
          </template>
          <template v-else-if="tab === 'Telegram'">
            <span>{{ row.config.username ? "@" + row.config.username : "Username not read yet" }}<small v-if="row.address">Bot ID {{ row.address }}</small></span>
            <span class="hide-m admin-mono">/webhooks/telegram/{{ row.key }}</span>
          </template>
          <template v-else>
            <span class="hide-m">{{ row.config.host }}:{{ row.config.port }}<small>{{ row.config.username || "No login" }}</small></span>
            <span class="hide-m">{{ row.config.imap_host ? row.config.imap_host + ":" + (row.config.imap_port || 993) : "Not configured" }}<small v-if="row.config.imap_host">{{ row.config.imap_folder || "INBOX" }}</small></span>
          </template>
          <span class="wide-m">
            <span v-if="row.site" class="admin-pill"><FeatherIcon name="globe" :size="12" />{{ siteLabel(row.site, sites) }}</span>
            <span v-else class="admin-pill admin-pill--none">Not linked to a site</span>
          </span>
          <span class="hide-m admin-stat"><Badge :label="channelStatus(row).label" :theme="channelStatus(row).theme" /></span>
          <FeatherIcon name="chevron-right" class="admin-chev" />
        </button>
      </div>
      <p v-else class="entity-prompt">No {{ current.plural }} in this container yet.</p>

      <ChannelDialog v-model="adding" :container="container" :channel-name="tab" :channel="editing" :sites="sites" @saved="refresh" />
      <ContainerDialog :model-value="!!dialog" :mode="dialog || 'rename'" :container="container" @update:model-value="dialog = $event ? dialog : ''" @saved="refresh" />
    </template>
  </section>
</template>
