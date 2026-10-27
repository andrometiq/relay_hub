<script setup>
import { Badge, Button } from '@relay/ui';
import AccountHealth from '../components/AccountHealth.vue';
import { channelStatus, siteHost, siteStatus } from '../composables/containers';
defineProps(['sites', 'more', 'status', 'runtime', 'deliveries']);
defineEmits(['navigate']);
</script>
<template>
  <section aria-label="Overview">
    <div class="view-heading"><div><h2>Overview &amp; health</h2><p>Database readiness, WhatsApp runtime and account setup. Refresh for current observations.</p></div></div>
    <div class="metrics">
      <article><span>Database readiness</span><Badge :label="status" :theme="status === 'Ready' ? 'green' : 'red'" /></article>
      <article><span>WhatsApp runtime accounts</span><strong>{{ runtime.whatsapp ?? 'Unknown' }}</strong></article>
      <article><span>Sites</span><strong>{{ sites.length }}{{ more ? '+' : '' }}</strong></article>
    </div>
    <div class="hub-card mt-6">
      <h3 class="font-medium">Delivery queues · all Hub transports</h3>
      <p class="my-3">{{ deliveries.queued ?? 0 }} queued · {{ deliveries.uncertain ?? 0 }} uncertain · {{ deliveries.failed ?? 0 }} failed</p>
      <Button label="Review deliveries and callbacks" @click="$emit('navigate', 'Deliveries')" />
    </div>
    <div class="view-heading mt-6"><div><h2>Site setup</h2><p>Showing {{ sites.length }}{{ more ? ' or more' : '' }} sites. Link accounts to sites on the Sites page.</p></div><Button label="Manage sites" @click="$emit('navigate', 'Sites')" /></div>
    <p v-if="!sites.length" class="entity-prompt">No sites loaded. Add a site or check the request error above.</p>
    <article v-for="site in sites" :key="site.id" class="hub-card mb-4">
      <h3 class="font-medium">{{ site.name || site.id }}</h3><p class="text-sm text-ink-gray-6 break-all mb-3">{{ siteHost(site.origin) }}</p>
      <div class="flex flex-wrap gap-2 mb-4"><Badge :label="siteStatus(site).label" :theme="siteStatus(site).theme" /><Badge :label="site.channels?.length ? site.channels.length + ' linked accounts' : 'No linked accounts'" /></div>
      <p v-if="!site.channels?.length" class="text-sm text-ink-gray-6">Link a WhatsApp number, Telegram bot or mailbox to this site.</p>
      <section v-for="channel in site.channels || []" :key="channel.channel + channel.key" class="mb-4">
        <div class="flex flex-wrap items-center gap-2 mb-2"><strong>{{ channel.label || channel.key }}</strong><span class="text-sm text-ink-gray-6">{{ channel.channel }}</span><Badge :label="channelStatus(channel).label" :theme="channelStatus(channel).theme" /></div>
        <AccountHealth :account="channel.key" :channel="channel.channel" />
      </section>
    </article>
  </section>
</template>
