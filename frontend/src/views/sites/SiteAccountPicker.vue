<script setup>
import { computed, ref, watch } from "vue";
import { Badge, Button, Dialog, ErrorMessage, FeatherIcon, TextInput } from "@relay/ui";
import { adminApi, assignedRefs, channelAddress, pickerGroups, provider } from "../../composables/containers";

const props = defineProps({ modelValue: Boolean, site: Object, containers: { type: Array, default: () => [] }, sites: { type: Array, default: () => [] } });
const emit = defineEmits(["update:modelValue", "saved"]);
const query = ref(""), picked = ref([]), error = ref(""), saving = ref(false);
const groups = computed(() => pickerGroups(props.containers, props.site?.id, query.value, props.sites));
const id = (row) => row.channel + "/" + row.key;
const isPicked = (row) => picked.value.some((item) => id(item) === id(row));

watch(() => props.modelValue, (open) => { if (open) { query.value = ""; picked.value = []; error.value = ""; } });

function toggle(row) {
  if (row.state !== "free") return;
  picked.value = isPicked(row) ? picked.value.filter((item) => id(item) !== id(row)) : [...picked.value, row];
}

async function save() {
  saving.value = true; error.value = "";
  try {
    await adminApi.assignChannels(props.site.id, assignedRefs(props.site.channels, picked.value));
    emit("saved");
    emit("update:modelValue", false);
  } catch (exception) {
    error.value = exception.message;
  } finally {
    saving.value = false;
  }
}
</script>

<template>
  <Dialog :model-value="modelValue" :options="{ title: 'Add accounts to ' + (site?.name || site?.id || ''), size: 'admin' }" :disable-outside-click-to-close="saving" @update:model-value="emit('update:modelValue', $event)">
    <template #body-content>
      <TextInput v-model="query" type="search" placeholder="Search number, bot or mailbox" aria-label="Search accounts" autocomplete="off"><template #prefix><FeatherIcon name="search" /></template></TextInput>
      <div v-if="groups.length" class="admin-pick-list" role="group" aria-label="Accounts">
        <template v-for="group in groups" :key="group.id">
          <div class="admin-pick-group">{{ group.name }}</div>
          <label v-for="row in group.rows" :key="id(row)" class="admin-pick-row" :class="{ 'is-disabled': row.state !== 'free' }">
            <input type="checkbox" class="admin-pick-box" :checked="isPicked(row)" :disabled="row.state !== 'free'" :aria-label="row.label || row.key" @change="toggle(row)" />
            <FeatherIcon :name="provider(row.channel).icon" :class="'provider-' + provider(row.channel).tone" />
            <span>{{ row.label || row.key }}<small>{{ channelAddress(row) }}</small></span>
            <Badge v-if="row.state === 'here'" label="Already here" theme="blue" />
            <Badge v-else-if="row.state === 'elsewhere'" :label="'On ' + row.siteName" />
            <Badge v-else label="Not linked" />
          </label>
        </template>
      </div>
      <p v-else class="entity-prompt">No accounts match. Add numbers, bots and mailboxes on the Containers page.</p>
      <p class="admin-hint">An account linked to another site cannot be picked. Remove it from that site first.</p>
      <ErrorMessage :message="error" />
    </template>
    <template #actions="{ close }">
      <Button label="Cancel" :disabled="saving" @click="close" />
      <Button :label="'Add ' + (picked.length === 1 ? '1 account' : picked.length + ' accounts')" variant="solid" :disabled="!picked.length" :loading="saving" @click="save" />
    </template>
  </Dialog>
</template>
