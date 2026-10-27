<script setup>
import { Button } from "@relay/ui";
defineProps({ page: Object });
defineEmits(["page"]);
</script>
<template>
  <div
    class="flex items-center justify-between flex-wrap gap-3 mt-4"
    aria-label="Account pages"
  >
    <span class="text-sm text-ink-gray-5"
      >Page {{ page.previous.length + 1 }} · {{ page.total }} accounts in
      total</span
    >
    <div class="flex gap-2">
      <Button
        label="Previous accounts"
        :disabled="page.busy || !page.previous.length"
        @click="$emit('page', 'previous')"
      /><Button
        label="Next accounts"
        :disabled="page.busy || !page.next"
        @click="$emit('page', 'next')"
      />
    </div>
  </div>
</template>
