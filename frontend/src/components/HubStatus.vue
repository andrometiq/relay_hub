<script setup>
import { ref } from 'vue';
import { Badge, Button } from '@relay/ui';
defineProps({ status: String, error: String, updated: String });
const open = ref(false);
</script>
<template>
  <div class="hub-status">
    <Badge :label="status" :theme="status === 'Ready' ? 'green' : 'orange'" />
    <Button :label="`Notifications${error ? ' (1)' : ''}`" icon-left="bell" :aria-expanded="open" @click="open = !open" />
    <section v-if="open" class="hub-notifications" aria-label="Notifications">
      <strong>Hub status</strong>
      <p v-if="error" role="alert">{{ error }}</p><p v-else>No issues reported by the latest request.</p>
      <p>{{ updated ? `Last checked ${updated}` : 'Not checked yet' }}. Refresh to check again; Hub has no live transport.</p>
    </section>
  </div>
</template>
