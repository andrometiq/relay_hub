<script setup>
import { ref, watch, onBeforeUnmount } from 'vue';
import { Button, ErrorMessage, FormControl } from '@relay/ui';
import AccountHealth from './AccountHealth.vue';
import { hubApi } from '../services/hubApi';
const props = defineProps({ account: String, channel: String });
const connection = ref(null), ingress = ref(null), error = ref(''), busy = ref(false), copied = ref(false);
const health = ref();
let generation = 0;
watch(() => [props.account, props.channel], () => { generation++; connection.value = null; ingress.value = null; error.value = ''; busy.value = false; copied.value = false; });
onBeforeUnmount(() => { generation++; });
async function run(action) {
  if (busy.value) return;
  const ticket = ++generation;
  busy.value = true; error.value = ''; copied.value = false;
  try {
    const path = `accounts/${encodeURIComponent(props.account)}/${props.channel.toLowerCase()}`;
    if (action === 'ingress') {
      const result = await hubApi(`${path}/webhook`);
      if (ticket === generation) ingress.value = result;
    } else {
      connection.value = null;
      const result = await hubApi(`${path}/connection`, action === 'status' ? undefined : { action });
      if (ticket === generation) { connection.value = result; health.value?.refresh(); }
    }
  } catch (e) { if (ticket === generation) error.value = e.message; }
  finally { if (ticket === generation) busy.value = false; }
}
function webhookURL(path) {
  const prefix = window.location.pathname === '/hub' || window.location.pathname.startsWith('/hub/') ? '/hub' : '';
  return window.location.origin + prefix + path;
}
async function copySecret() {
  error.value = '';
  try { await navigator.clipboard.writeText(ingress.value.secret_token); copied.value = true; }
  catch { error.value = 'Copy failed. Reveal and select the ingress secret to copy it.'; }
}
const revealed = ref(false);
watch(ingress, () => { revealed.value = false; });
</script>

<template>
  <section class="space-y-4 min-w-0" :aria-label="channel + ' connection health'">
    <div class="setup-pane-heading"><div><h4>Connection health</h4><p>Checks use saved, activated settings. Save any edits before testing.</p></div></div>
    <div class="flex flex-wrap gap-2">
      <Button :label="channel === 'Telegram' ? 'Check webhook' : 'Test connection'" :loading="busy" @click="run(channel === 'Telegram' ? 'status' : 'test')" />
      <Button v-if="channel === 'Telegram'" label="Register / repair webhook" :disabled="busy" @click="run('repair')" />
      <Button v-if="channel === 'Email'" label="Show ingress setup" :disabled="busy" @click="run('ingress')" />
      <Button label="Refresh health" :disabled="busy" @click="health?.refresh()" />
    </div>
    <ErrorMessage :message="error" />
    <dl v-if="connection" class="entity-details" aria-live="polite">
      <template v-if="channel === 'Telegram'">
        <div><dt>Webhook registration</dt><dd>{{ connection.matches ? 'Registered to this Hub' : connection.registered ? 'Registered elsewhere — repair needed' : 'Not registered' }}</dd></div>
        <div><dt>Provider pending updates</dt><dd>{{ connection.pending_updates }}</dd></div>
        <div><dt>Last provider webhook error</dt><dd>{{ connection.last_error_at ? new Date(connection.last_error_at * 1000).toLocaleString() : 'None reported' }}</dd></div>
      </template>
      <template v-else>
        <div><dt>SMTP</dt><dd>{{ connection.smtp === 'connected' ? 'TLS and authentication passed' : 'Failed — check SMTP settings, TLS and credentials' }}</dd></div>
        <div><dt>IMAP</dt><dd>{{ connection.imap === 'connected' ? 'TLS, authentication and folder access passed' : connection.imap === 'not_configured' ? 'Not configured' : 'Failed — check IMAP settings, TLS and folder access' }}</dd></div>
      </template>
    </dl>
    <div v-if="ingress" class="space-y-3 min-w-0">
      <div class="webhook-address"><div><span>Ingress URL</span><code>{{ webhookURL(ingress.path) }}</code></div></div>
      <p class="text-sm text-ink-gray-6">Send normalized JSON with the X-Relay-Email-Secret header. This secret is specific to this account.</p>
      <FormControl label="Ingress secret" :type="revealed ? 'text' : 'password'" readonly :model-value="ingress.secret_token" autocomplete="off" />
      <div class="flex flex-wrap gap-2">
        <Button :label="revealed ? 'Hide secret' : 'Reveal secret'" @click="revealed = !revealed" />
        <Button :label="copied ? 'Copied' : 'Copy secret'" @click="copySecret" />
        <Button label="Close ingress setup" @click="ingress = null" />
      </div>
    </div>
    <AccountHealth ref="health" :account="account" :channel="channel" />
  </section>
</template>
