<script setup>
import { Badge, Button } from "@relay/ui";
defineProps(["accounts", "total", "status", "enabledChannels", "runtime"]);
defineEmits(["navigate"]);
</script>
<template>
  <section aria-label="Overview">
    <h2 class="text-xl font-semibold mb-6">Overview</h2>
    <div class="metrics">
      <article>
        <span>Service</span
        ><Badge :label="status" :theme="status === 'Ready' ? 'green' : 'red'" />
      </article>
      <article>
        <span>Accounts</span><strong>{{ total }}</strong>
      </article>
      <article>
        <span>Enabled channels</span><strong>{{ enabledChannels }}</strong>
      </article>
    </div>
    <section class="hub-card mt-6">
      <h3 class="font-medium mb-5">Communication setup</h3>
      <div class="flex justify-between py-3 border-b">
        <span>Accounts</span
        ><button class="text-blue-600" @click="$emit('navigate', 'Accounts')">
          {{ total }} configured
        </button>
      </div>
      <div class="flex justify-between py-3 border-b">
        <span>Channel credentials</span
        ><span>{{ enabledChannels }} enabled</span>
      </div>
      <div class="flex justify-between py-3">
        <span>Live runtime</span
        ><span>WhatsApp {{ runtime.whatsapp }}</span>
      </div>
    </section>
  </section>
</template>
