<script setup>
import { computed, ref } from "vue";
import { Badge, Button, ErrorMessage, FormControl, FeatherIcon } from "@relay/ui";
import DocumentForm from "./DocumentForm.vue";

const props = defineProps({
  channel: String,
  section: { type: String, default: "meta" },
  config: Object,
  configured: { type: Array, default: () => [] },
  enabled: Boolean,
  secret: String,
  errors: { type: Object, default: () => ({}) },
  accountId: String,
});
const emit = defineEmits(["update:config", "update:enabled", "update:secret"]);
const copied = ref(false);

const privateField = (name, label, placeholder = "") => ({
  name,
  label,
  type: "Password",
  autocomplete: "new-password",
  placeholder: props.configured.includes(name) ? "Saved — leave blank to keep" : placeholder,
});
const configured = (name) => props.configured.includes(name);
const available = (name) =>
  configured(name) || !!String(name === "secret" ? props.secret || "" : props.config?.[name] || "").trim();
const ready = computed(() =>
  props.enabled &&
  available("secret") &&
  available("app_secret") &&
  available("verify_token") &&
  /^\d{1,30}$/.test(String(props.config?.phone_id || "")) &&
  /^\d{1,30}$/.test(String(props.config?.waba_id || "")) &&
  /^v\d+\.0$/.test(String(props.config?.graph_version || "")),
);
const connected = computed(() => props.config?.connection_status === "connected");
const status = computed(() => connected.value ? "Connected" : ready.value ? "Ready to verify" : props.enabled ? "Setup needed" : "Disabled");
const statusTheme = computed(() => connected.value ? "green" : props.enabled ? "orange" : "gray");

const metaFields = computed(() => [
  { name: "phone_id", label: "Phone number ID", type: "Data", required: true, placeholder: "From WhatsApp API setup" },
  { name: "waba_id", label: "WhatsApp Business Account ID", type: "Data", required: true, placeholder: "Numeric business account ID" },
  { name: "app_id", label: "Meta app ID", type: "Data", required: !!props.config?.templates, placeholder: "Required for template media" },
  { name: "graph_version", label: "Graph API version", type: "Data", required: true, placeholder: "v25.0" },
]);
const webhookFields = computed(() => [
  privateField("app_secret", "Meta app secret"),
  privateField("verify_token", "Webhook verify token", "Create a private value and use it in Meta"),
]);
const coreFields = computed(() => [
  privateField("core_api_key", "Frappe API key"),
  privateField("core_api_secret", "Frappe API secret"),
  { ...privateField("callback_secret", "Callback signing secret", "At least 32 characters"), wide: true },
]);

const callbackURL = computed(() => {
  const path = window.location.pathname;
  const prefix = path === "/hub" || path.startsWith("/hub/") ? "/hub" : "";
  const id = encodeURIComponent(String(props.accountId || "ACCOUNT_ID").trim() || "ACCOUNT_ID");
  return `${window.location.origin}${prefix}/webhooks/whatsapp/${id}`;
});

function updateConfig(patch) {
  emit("update:config", { ...(props.config || {}), ...patch });
}

async function copyCallback() {
  try {
    await navigator.clipboard.writeText(callbackURL.value);
    copied.value = true;
    window.setTimeout(() => (copied.value = false), 1800);
  } catch {
    copied.value = false;
  }
}
</script>

<template>
  <section class="channel-pane" aria-label="WhatsApp configuration">
    <header class="channel-pane__header">
      <div class="channel-pane__title">
        <span class="channel-pane__mark"><FeatherIcon name="message-circle" /></span>
        <div>
          <div class="flex items-center gap-2"><h3>WhatsApp</h3><Badge :label="status" :theme="statusTheme" /></div>
          <p>Meta WhatsApp Business Cloud API</p>
        </div>
      </div>
      <FormControl type="checkbox" label="Enable channel" :model-value="enabled" @update:model-value="emit('update:enabled', $event)" />
    </header>

    <div v-if="!enabled" class="channel-pane__disabled">
      <span><FeatherIcon name="message-circle" :size="20" /></span>
      <h4>WhatsApp is disabled</h4>
      <p>Enable the channel to configure its Meta connection and webhook.</p>
      <Button label="Enable WhatsApp" variant="solid" @click="emit('update:enabled', true)" />
    </div>

    <div v-else class="channel-pane__content">
      <template v-if="section === 'meta'">
        <div class="setup-pane-heading">
          <div><h4>Connect the Meta business account</h4><p>Use the permanent system-user token and numeric IDs from Meta Business Manager.</p></div>
          <a href="https://developers.facebook.com/apps/" target="_blank" rel="noreferrer">Open Meta <FeatherIcon name="external-link" :size="14" /></a>
        </div>
        <div class="setup-callout"><FeatherIcon name="info" :size="16" /><p>Relay stores secrets encrypted. Saved values are never returned to the browser; leave a saved secret blank to keep it.</p></div>
        <div class="setup-secret-field">
          <FormControl label="Permanent access token" type="password" autocomplete="new-password" required :placeholder="configured('secret') ? 'Saved — leave blank to keep' : 'Meta system-user token'" :model-value="secret" @update:model-value="emit('update:secret', $event)" />
          <ErrorMessage v-if="errors.secret" :message="errors.secret" class="mt-2" />
        </div>
        <DocumentForm :model-value="config || {}" :fields="metaFields" :errors="errors" @update:model-value="emit('update:config', $event)" />
      </template>

      <template v-else-if="section === 'webhook'">
        <div class="setup-pane-heading"><div><h4>Receive WhatsApp events</h4><p>Register this account-specific callback in Meta, then subscribe to the messages field.</p></div></div>
        <div class="webhook-address">
          <div><span>Callback URL</span><code>{{ callbackURL }}</code></div>
          <Button :label="copied ? 'Copied' : 'Copy'" :icon-left="copied ? 'check' : 'copy'" @click="copyCallback" />
        </div>
        <ol class="webhook-steps">
          <li><span>1</span><p>Paste the callback URL in <strong>Meta → WhatsApp → Configuration → Webhook</strong>.</p></li>
          <li><span>2</span><p>Enter the same private value in Meta’s <strong>Verify token</strong> and below.</p></li>
          <li><span>3</span><p>Verify the callback, then subscribe the WhatsApp Business Account to <strong>messages</strong>.</p></li>
        </ol>
        <DocumentForm :model-value="config || {}" :fields="webhookFields" :errors="errors" @update:model-value="emit('update:config', $event)" />
      </template>

      <template v-else-if="section === 'features'">
        <div class="setup-pane-heading"><div><h4>WhatsApp business features</h4><p>Choose which Meta resources Relay should manage for this channel.</p></div></div>
        <div class="feature-options">
          <div><span class="feature-options__icon"><FeatherIcon name="file-text" /></span><span><strong>Message templates</strong><small>Create, sync and use approved Meta templates.</small></span><FormControl type="checkbox" aria-label="Manage message templates" :model-value="!!config?.templates" @update:model-value="updateConfig({ templates: $event })" /></div>
          <div><span class="feature-options__icon"><FeatherIcon name="refresh-cw" /></span><span><strong>Hourly metadata sync</strong><small>Refresh the business profile and template status every hour.</small></span><FormControl type="checkbox" aria-label="Sync metadata hourly" :model-value="!!config?.sync_metadata" @update:model-value="updateConfig({ sync_metadata: $event })" /></div>
        </div>
        <div v-if="config?.templates && !/^\d{1,30}$/.test(String(config?.app_id || ''))" class="setup-callout setup-callout--warning"><FeatherIcon name="alert-circle" :size="16" /><p>Add the numeric Meta app ID under Meta connection before using template media uploads.</p></div>
      </template>

      <template v-else-if="section === 'delivery'">
        <div class="setup-pane-heading"><div><h4>Deliver events to Relay Core</h4><p>Hub uses these credentials to push incoming messages and status updates to this account’s Frappe site.</p></div></div>
        <div class="setup-callout"><FeatherIcon name="info" :size="16" /><p>Create an API user on the target Frappe site. Enter all three values together, or leave all three empty until Core is ready.</p></div>
        <DocumentForm :model-value="config || {}" :fields="coreFields" :errors="errors" @update:model-value="emit('update:config', $event)" />
      </template>
    </div>
  </section>
</template>
