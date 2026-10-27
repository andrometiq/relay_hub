<script setup>
import { computed, ref, watch } from "vue";
import { Button, Dialog, ErrorMessage, FeatherIcon, FormControl } from "@relay/ui";
import { adminApi, hubURL, moveChannel, siteHost } from "../../composables/containers";
import ForwardTargets from "./ForwardTargets.vue";
import { channelForm, channelPayload, validateChannelForm } from "../../composables/channelForm";

const props = defineProps({ modelValue: Boolean, container: Object, channelName: String, channel: Object, sites: { type: Array, default: () => [] } });
const emit = defineEmits(["update:modelValue", "saved"]);
const form = ref(channelForm("WhatsApp")), errors = ref({}), error = ref(""), notice = ref(""), saving = ref(false);
const stored = ref(null);

const noun = computed(() => ({ WhatsApp: "WhatsApp number", Telegram: "Telegram bot", Email: "mailbox" })[form.value.channel]);
const title = computed(() => (stored.value ? stored.value.label || stored.value.key : "Add " + noun.value) + " · " + (props.container?.name || props.container?.id || ""));
const containerToken = computed(() => (props.container?.whatsapp?.configured_secrets || []).includes("access_token"));
const tokenRequired = computed(() => !containerToken.value && !form.value.configured.includes("secret"));
const siteOptions = computed(() => [{ label: "Not linked", value: "" }, ...props.sites.map((site) => ({ label: siteHost(site.origin), value: site.id }))]);
const smtpPorts = computed(() => withCurrent([{ label: "465 · SSL", value: 465 }, { label: "587 · STARTTLS", value: 587 }, { label: "25 · STARTTLS", value: 25 }], form.value.config.port));
const imapPorts = computed(() => withCurrent([{ label: "993 · SSL", value: 993 }, { label: "143 · STARTTLS", value: 143 }], form.value.config.imap_port));
const webhookURL = computed(() => (stored.value?.webhook_path ? hubURL(stored.value.webhook_path) : ""));
async function copyWebhook() {
  try { await navigator.clipboard.writeText(webhookURL.value); notice.value = "Webhook URL copied."; } catch { notice.value = "Copy failed; select the URL and copy it."; }
}
const placeholder = (name, blank) => (form.value.configured.includes(name) ? "Saved — leave blank to keep" : blank);

function withCurrent(options, value) {
  return value && !options.some((option) => option.value === Number(value)) ? [...options, { label: String(value), value: Number(value) }] : options;
}

watch(() => props.modelValue, (open) => {
  if (!open) return;
  stored.value = props.channel || null;
  form.value = channelForm(props.channel?.channel || props.channelName, props.channel);
  errors.value = {}; error.value = ""; notice.value = "";
});

async function check(channel) {
  try {
    await adminApi.checkChannel(channel, channel.channel === "Telegram" ? "repair" : "test");
    return "";
  } catch (exception) {
    return exception.message;
  }
}

async function save() {
  errors.value = validateChannelForm(form.value, { containerToken: containerToken.value });
  if (Object.keys(errors.value).length) return;
  const taken = (props.container?.channels || []).filter((row) => row.channel === form.value.channel).map((row) => row.key);
  const payload = channelPayload(form.value, taken);
  saving.value = true; error.value = ""; notice.value = "";
  let saved = false;
  try {
    if (stored.value) await adminApi.updateChannel(props.container.id, payload);
    else await adminApi.createChannel(props.container.id, payload);
    saved = true;
    form.value.key = payload.key;
    form.value.secret = "";
    stored.value = { ...(stored.value || {}), ...payload, site: stored.value?.site || "" };
    await moveChannel(payload, stored.value.site, form.value.site);
    stored.value.site = form.value.site;
    const problem = payload.enabled ? await check(payload) : "";
    if (problem) notice.value = "Saved. The connection test failed: " + problem;
    else emit("update:modelValue", false);
  } catch (exception) {
    error.value = saved ? "Saved, but linking to the site failed: " + exception.message : exception.message;
  } finally {
    saving.value = false;
    if (saved) emit("saved");
  }
}
</script>

<template>
  <Dialog :model-value="modelValue" :options="{ title, size: 'admin' }" :disable-outside-click-to-close="saving" @update:model-value="emit('update:modelValue', $event)">
    <template #body-content>
      <div v-if="form.channel === 'Telegram'" class="admin-note"><FeatherIcon name="info" /><p>Create the bot with @BotFather and paste its token. The Hub checks the token, reads the bot name and registers the webhook.</p></div>
      <form id="relay-channel-form" class="admin-form" novalidate @submit.prevent="save">
        <div><FormControl v-model="form.label" label="Account name" required autocomplete="off" /><ErrorMessage :message="errors.label" /></div>

        <template v-if="form.channel === 'WhatsApp'">
          <FormControl v-model="form.config.display_number" label="Display number" placeholder="+91 94430 55301" autocomplete="off" />
          <div><FormControl v-model="form.config.phone_id" label="Phone number ID" required autocomplete="off" /><ErrorMessage :message="errors.phone_id" /></div>
          <div><FormControl v-model="form.config.waba_id" label="WABA ID" required autocomplete="off" /><ErrorMessage :message="errors.waba_id" /></div>
          <div class="relay-field admin-full">
            <span class="relay-field__label">Access token<span v-if="tokenRequired" aria-hidden="true"> *</span> <small>· system user token with whatsapp_business_messaging</small></span>
            <FormControl v-model="form.secret" type="password" aria-label="Access token" autocomplete="new-password" :placeholder="placeholder('secret', containerToken ? 'Blank uses the container token' : 'EAAG…')" />
            <ErrorMessage :message="errors.secret" />
          </div>
          <div class="admin-sub">Webhook security</div>
          <FormControl :model-value="!form.override" type="checkbox" label="Use the container's Meta app (App secret and verify token)" class="admin-full" @update:model-value="form.override = !$event" />
          <template v-if="form.override">
            <div><FormControl v-model="form.config.app_secret" label="App secret" type="password" autocomplete="new-password" :placeholder="placeholder('app_secret', 'This number\'s Meta app secret')" /><ErrorMessage :message="errors.app_secret" /></div>
            <div><FormControl v-model="form.config.verify_token" label="Verify token" type="password" autocomplete="new-password" :placeholder="placeholder('verify_token', 'Private value for Meta')" /><ErrorMessage :message="errors.verify_token" /></div>
          </template>
          <template v-else>
            <FormControl label="App secret" model-value="From container" disabled />
            <FormControl label="Verify token" model-value="From container" disabled />
          </template>
        </template>

        <template v-else-if="form.channel === 'Telegram'">
          <FormControl label="Bot username" :model-value="form.config.username ? '@' + form.config.username : 'Read from the token'" disabled />
          <div class="admin-full"><FormControl v-model="form.secret" label="Bot token" :required="!form.configured.includes('secret')" type="password" autocomplete="new-password" :placeholder="placeholder('secret', '7712331:AAH…')" /><ErrorMessage :message="errors.secret" /></div>
        </template>

        <template v-else>
          <div><FormControl v-model="form.config.from" label="Email address" required type="email" autocomplete="off" /><ErrorMessage :message="errors.from" /></div>
          <div class="admin-sub">Send (SMTP)</div>
          <div><FormControl v-model="form.config.host" label="SMTP host" required autocomplete="off" /><ErrorMessage :message="errors.host" /></div>
          <div><FormControl v-model="form.config.port" label="Port and security" type="select" :options="smtpPorts" /><ErrorMessage :message="errors.port" /></div>
          <div class="admin-sub">Receive (IMAP)</div>
          <div><FormControl v-model="form.config.imap_host" label="IMAP host" autocomplete="off" placeholder="Blank for webhook ingress only" /><ErrorMessage :message="errors.imap_host" /></div>
          <div class="relay-field">
            <span class="relay-field__label">Port and folder</span>
            <div class="admin-pair"><FormControl v-model="form.config.imap_port" type="select" aria-label="IMAP port" :options="imapPorts" /><FormControl v-model="form.config.imap_folder" aria-label="IMAP folder" autocomplete="off" /></div>
            <ErrorMessage :message="errors.imap_port || errors.imap_folder" />
          </div>
          <div><FormControl v-model="form.config.username" label="Username" autocomplete="off" /><ErrorMessage :message="errors.imap_username" /></div>
          <div><FormControl v-model="form.secret" label="Password or app password" :required="!form.configured.includes('secret')" type="password" autocomplete="new-password" :placeholder="placeholder('secret', '')" /><ErrorMessage :message="errors.secret || errors.imap_password" /></div>
        </template>

        <div class="relay-field admin-full">
          <span class="relay-field__label">Link to site <small>· optional{{ form.channel === 'WhatsApp' ? ', you can link it later on the Sites page' : '' }}</small></span>
          <FormControl v-model="form.site" type="select" aria-label="Link to site" placeholder="Not linked" :options="siteOptions" />
        </div>
        <div v-if="form.channel === 'Email' && webhookURL" class="webhook-address admin-full"><div><span>Inbound webhook URL</span><code>{{ webhookURL }}</code></div><Button label="Copy" icon-left="copy" @click="copyWebhook" /></div>
        <FormControl v-if="stored" v-model="form.enabled" type="checkbox" label="Account enabled" class="admin-full" />
      </form>
      <div v-if="stored && form.channel !== 'WhatsApp'" class="mt-4"><ForwardTargets :provider="form.channel" :scope-key="stored.key" /></div>
      <p v-if="notice" role="status" class="feedback-panel mt-4">{{ notice }}</p>
      <ErrorMessage :message="error" />
    </template>
    <template #actions="{ close }">
      <Button :label="notice ? 'Close' : 'Cancel'" :disabled="saving" @click="close" />
      <Button type="submit" form="relay-channel-form" :label="stored ? 'Test and save' : 'Test and add'" variant="solid" :loading="saving" />
    </template>
  </Dialog>
</template>
