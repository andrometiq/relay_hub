<script setup>
import { Badge, Button } from '@relay/ui';
import AccountHealth from '../components/AccountHealth.vue';
import { whatsappReady } from '../composables/accountForm';
defineProps(['accounts', 'total', 'status', 'runtime', 'deliveries']);
defineEmits(['navigate']);
const channel = account => account.channels?.find(row => row.channel === 'WhatsApp');
const coreReady = account => ['core_api_key','core_api_secret','callback_secret'].every(key => channel(account)?.configured_secrets?.includes(key));
</script>
<template>
  <section aria-label="Overview">
    <div class="view-heading"><div><h2>Overview &amp; health</h2><p>Database readiness, WhatsApp runtime and account setup. Refresh for current observations.</p></div></div>
    <div class="metrics">
      <article><span>Database readiness</span><Badge :label="status" :theme="status === 'Ready' ? 'green' : 'red'" /></article>
      <article><span>WhatsApp runtime accounts</span><strong>{{ runtime.whatsapp ?? 'Unknown' }}</strong></article>
      <article><span>Configured accounts</span><strong>{{ total }}</strong></article>
    </div>
    <div class="hub-card mt-6">
      <h3 class="font-medium">Delivery queues · all Hub transports</h3>
      <p class="my-3">{{ deliveries.queued ?? 0 }} queued · {{ deliveries.uncertain ?? 0 }} uncertain · {{ deliveries.failed ?? 0 }} failed</p>
      <Button label="Review deliveries and callbacks" @click="$emit('navigate', 'Deliveries')" />
    </div>
    <div class="view-heading mt-6"><div><h2>Account setup</h2><p>Showing {{ accounts.length }} of {{ total }} accounts. Search all accounts in Accounts.</p></div><Button label="Manage accounts" @click="$emit('navigate', 'Accounts')" /></div>
    <p v-if="!accounts.length" class="entity-prompt">No accounts loaded. Add an account or check the request error above.</p>
    <article v-for="account in accounts" :key="account.id" class="hub-card mb-4">
      <h3 class="font-medium">{{ account.id }}</h3><p class="text-sm text-ink-gray-6 break-all mb-3">{{ account.site }}</p>
      <div class="flex flex-wrap gap-2 mb-4"><Badge :label="whatsappReady(account) ? 'Credentials verified' : 'Meta setup needed'" /><Badge :label="account.enabled && channel(account)?.enabled ? 'Enabled in configuration' : 'Disabled'" /><Badge :label="coreReady(account) ? 'Core credentials saved' : 'Core credentials missing'" /></div>
      <AccountHealth :account="account.id" />
    </article>
  </section>
</template>
