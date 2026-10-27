<script setup>
import { computed, ref, watch } from "vue";
import { Button, Dialog, ErrorMessage, FormControl } from "@relay/ui";
import { adminApi, newSecret } from "../../composables/containers";

const props = defineProps({ modelValue: Boolean, mode: { type: String, default: "create" }, container: Object });
const emit = defineEmits(["update:modelValue", "saved"]);
const form = ref({}), errors = ref({}), error = ref(""), saving = ref(false), showToken = ref(false);
const title = computed(() => ({ create: "New container", rename: "Rename container", app: "Meta app and webhook" })[props.mode] + (props.container ? " · " + (props.container.name || props.container.id) : ""));
const saved = (name) => (props.container?.whatsapp?.configured_secrets || []).includes(name);
const secretPlaceholder = (name, blank) => (saved(name) ? "Saved — leave blank to keep" : blank);

watch(() => props.modelValue, (open) => {
  if (!open) return;
  const config = props.container?.whatsapp?.config || {};
  form.value = { id: "", name: props.container?.name || "", enabled: props.container?.enabled ?? true, app_id: config.app_id || "", graph_version: config.graph_version || "v23.0", app_secret: "", verify_token: "", access_token: "" };
  errors.value = {}; error.value = ""; showToken.value = false;
});

function validate() {
  const value = form.value, result = {};
  if (props.mode === "create" && !value.name.trim()) result.name = "Enter a name.";
  if (props.mode === "app") {
    if (value.app_id && !/^\d{1,30}$/.test(value.app_id)) result.app_id = "Enter the numeric Meta app ID.";
    if (!/^v\d+\.0$/.test(value.graph_version)) result.graph_version = "Use a Graph API version such as v23.0.";
    if (!saved("app_secret") && !value.app_secret) result.app_secret = "Enter the Meta app secret.";
    if (!saved("verify_token") && !value.verify_token) result.verify_token = "Enter or generate a verify token.";
  }
  return result;
}

function generateToken() {
  form.value.verify_token = "rl_vt_" + newSecret(24);
  showToken.value = true;
}

async function save() {
  errors.value = validate();
  if (Object.keys(errors.value).length) return;
  const value = form.value;
  saving.value = true; error.value = "";
  try {
    if (props.mode === "create") {
      const created = await adminApi.createContainer({ name: value.name.trim(), enabled: true });
      emit("saved", created.id);
    } else {
      const changes = { name: value.name.trim(), enabled: value.enabled };
      if (props.mode === "app") changes.whatsapp = { config: { app_id: value.app_id, graph_version: value.graph_version, app_secret: value.app_secret, verify_token: value.verify_token }, access_token: value.access_token };
      await adminApi.updateContainer(props.container.id, changes);
      emit("saved", props.container.id);
    }
    emit("update:modelValue", false);
  } catch (exception) {
    error.value = exception.message;
  } finally {
    saving.value = false;
  }
}
</script>

<template>
  <Dialog :model-value="modelValue" :options="{ title, size: 'admin' }" :disable-outside-click-to-close="saving" @update:model-value="emit('update:modelValue', $event)">
    <template #body-content>
      <form id="relay-container-form" class="admin-form" novalidate @submit.prevent="save">
        <template v-if="mode === 'create'">
          <div class="admin-full"><FormControl v-model="form.name" label="Name" required placeholder="Essdee Main" autocomplete="off" /><ErrorMessage :message="errors.name" /></div>
          <p class="admin-hint admin-full">The Hub makes a random callback URL for this container. The name is never part of a public URL.</p>
        </template>
        <template v-else-if="mode === 'rename'">
          <FormControl v-model="form.name" label="Name" class="admin-full" autocomplete="off" />
          <FormControl v-model="form.enabled" type="checkbox" label="Container enabled" class="admin-full" />
        </template>
        <template v-else>
          <div><FormControl v-model="form.app_id" label="App ID" placeholder="Numeric Meta app ID" autocomplete="off" /><ErrorMessage :message="errors.app_id" /></div>
          <div><FormControl v-model="form.graph_version" label="Graph API version" required autocomplete="off" /><ErrorMessage :message="errors.graph_version" /></div>
          <div class="admin-full"><FormControl v-model="form.app_secret" label="App secret" type="password" autocomplete="new-password" :placeholder="secretPlaceholder('app_secret', 'From Meta App Dashboard → App settings → Basic')" /><ErrorMessage :message="errors.app_secret" /></div>
          <div class="admin-full">
            <div class="admin-generate">
              <FormControl v-model="form.verify_token" label="Verify token" :type="showToken ? 'text' : 'password'" autocomplete="new-password" :placeholder="secretPlaceholder('verify_token', 'A private value you also paste in Meta')" />
              <Button label="Generate" icon-left="refresh-cw" @click="generateToken" />
            </div>
            <ErrorMessage :message="errors.verify_token" />
          </div>
          <div class="admin-full"><FormControl v-model="form.access_token" label="System-user access token" type="password" autocomplete="new-password" :placeholder="secretPlaceholder('access_token', 'Optional. Numbers without their own token use it')" /></div>
          <p class="admin-hint admin-full">Secrets are write-only. Leave a saved secret blank to keep it.</p>
        </template>
      </form>
      <ErrorMessage :message="error" />
    </template>
    <template #actions="{ close }">
      <Button label="Cancel" :disabled="saving" @click="close" />
      <Button type="submit" form="relay-container-form" :label="mode === 'create' ? 'Create container' : 'Save'" variant="solid" :loading="saving" />
    </template>
  </Dialog>
</template>
