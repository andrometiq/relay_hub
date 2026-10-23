<script setup>
import { ref, watch, onBeforeUnmount } from 'vue';
import { ErrorMessage } from '@relay/ui';
import { hubApi } from '../services/hubApi';
const props = defineProps({ account: String, channel: { type: String, default: "WhatsApp" } });
const health = ref(null), error = ref(''), busy = ref(false);
let generation = 0;
async function refresh() {
  const ticket = ++generation; busy.value = true; error.value = ''; health.value = null;
  try { const result = await hubApi('accounts/health?' + new URLSearchParams({account: props.account, channel: props.channel})); if(ticket === generation) health.value = result; }
  catch(e) { if(ticket === generation) error.value = e.message; }
  finally { if(ticket === generation) busy.value = false; }
}
watch(() => [props.account, props.channel], refresh, {immediate: true});
onBeforeUnmount(() => { generation++; });
defineExpose({refresh});
</script>
<template>
  <p v-if="busy" role="status">Checking webhook and Core delivery…</p>
  <ErrorMessage :message="error" />
  <p v-if="health?.inbound_truncated" role="status">Callback evidence covers the latest {{ health.sample_limit }} incoming updates. Older callbacks may still be pending.</p>
  <p v-if="health?.outbox_truncated" role="status">Outbound counts and retry time cover the latest {{ health.sample_limit }} unresolved deliveries. More deliveries exist.</p>
  <dl v-if="health" class="entity-details">
    <div><dt>{{ channel === "WhatsApp" ? "Webhook received" : "Last incoming update" }}</dt><dd>{{ health.webhook_received_at || 'No reception recorded' }}</dd></div>
    <div><dt>Core delivery confirmed</dt><dd>{{ health.core_confirmed_at || 'Not confirmed yet' }}</dd></div>
    <div><dt>Tracked callback backlog</dt><dd>{{ health.callback_pending }} pending · {{ health.callback_failed }} failed · {{ health.callback_quarantined }} quarantined</dd></div>
    <template v-if="channel !== 'WhatsApp'">
      <div><dt>Outbound deliveries</dt><dd>{{ health.delivery_pending }} pending · {{ health.delivery_uncertain }} uncertain · {{ health.delivery_rejected }} rejected</dd></div>
      <div><dt>Retry state</dt><dd>{{ health.delivery_retrying }} retrying · {{ health.delivery_rate_limited }} rate limited</dd></div>
      <div><dt>Next scheduled retry</dt><dd>{{ health.next_attempt || 'None scheduled' }}</dd></div>
    </template>
  </dl>
  <section v-for="mailbox in health?.mailboxes || []" :key="mailbox.mailbox" class="space-y-2">
    <h4>Mailbox synchronization</h4>
    <dl class="entity-details">
      <div><dt>Last successful sync</dt><dd>{{ mailbox.last_sync || 'Not synced yet' }}</dd></div>
      <div><dt>UIDVALIDITY / last UID</dt><dd>{{ mailbox.uid_validity }} / {{ mailbox.last_uid }}</dd></div>
      <div><dt>Reset snapshot UIDNEXT</dt><dd>{{ mailbox.reset_uid_next || 'No reset snapshot' }}</dd></div>
      <div><dt>Reconnect state</dt><dd>{{ mailbox.backoff ? 'Backing off' : 'Ready' }} · {{ mailbox.failures }} failures</dd></div>
      <div><dt>Next poll</dt><dd>{{ mailbox.next_attempt }}</dd></div>
      <div v-if="mailbox.error"><dt>Last issue</dt><dd>{{ mailbox.error }}</dd></div>
    </dl>
  </section>
</template>
