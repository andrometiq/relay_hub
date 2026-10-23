<script setup>
import { ref, watch } from 'vue';
import { ErrorMessage } from '@relay/ui';
import { hubApi } from '../services/hubApi';
const props = defineProps({ account: String });
const health = ref(null), error = ref(''), busy = ref(false);
let generation = 0;
async function refresh() {
  const ticket = ++generation; busy.value = true; error.value = ''; health.value = null;
  try { const result = await hubApi('accounts/health?' + new URLSearchParams({account: props.account})); if(ticket === generation) health.value = result; }
  catch(e) { if(ticket === generation) error.value = e.message; }
  finally { if(ticket === generation) busy.value = false; }
}
watch(() => props.account, refresh, {immediate: true});
defineExpose({refresh});
</script>
<template>
  <p v-if="busy" role="status">Checking webhook and Core delivery…</p>
  <ErrorMessage :message="error" />
  <dl v-if="health" class="entity-details">
    <div><dt>Webhook received</dt><dd>{{ health.webhook_received_at || 'Not received yet' }}</dd></div>
    <div><dt>Core delivery confirmed</dt><dd>{{ health.core_confirmed_at || 'Not confirmed yet' }}</dd></div>
    <div><dt>Tracked callback backlog</dt><dd>{{ health.callback_pending }} pending · {{ health.callback_failed }} failed · {{ health.callback_quarantined }} quarantined</dd></div>
  </dl>
</template>
