<script setup>
import { computed, ref, watch } from "vue";
import { Button, Dialog, ErrorMessage, FormControl } from "@relay/ui";
import { SITE_SECRETS, adminApi, newSecret } from "../../composables/containers";

// Without a site it creates one. With "only" it replaces one Core credential.
const props = defineProps({ modelValue: Boolean, site: Object, only: String });
const emit = defineEmits(["update:modelValue", "saved"]);
const LABELS = { core_api_key: "Core API key", core_api_secret: "Core API secret", callback_secret: "Callback secret" };
const form = ref({}), errors = ref({}), error = ref(""), saving = ref(false), copied = ref(false);
const fields = computed(() => (props.only ? [props.only] : SITE_SECRETS));
const title = computed(() => (props.only ? (props.only === "callback_secret" ? "Rotate " : "Replace ") + LABELS[props.only].toLowerCase() : props.site ? "Edit site" : "New site"));
const saved = (name) => (props.site?.configured_secrets || []).includes(name);

watch(() => props.modelValue, (open) => {
  if (!open) return;
  form.value = { id: "", origin: props.site?.origin || "", enabled: props.site?.enabled ?? true, core_api_key: "", core_api_secret: "", callback_secret: props.only === "callback_secret" ? newSecret(36) : "" };
  errors.value = {}; error.value = ""; copied.value = false;
});

function validate() {
  const value = form.value, result = {};
  if (!props.site && !/^[A-Za-z0-9_-]{1,100}$/.test(value.id)) result.id = "Use letters, numbers, hyphens or underscores.";
  if (!props.only) {
    try { if (!/^https?:$/.test(new URL(value.origin).protocol)) throw Error(); } catch { result.origin = "Enter the site link, such as https://yrp.example.com."; }
  }
  const present = SITE_SECRETS.map((name) => saved(name) || !!value[name]);
  if (present.some(Boolean) && !present.every(Boolean)) SITE_SECRETS.forEach((name, index) => { if (!present[index]) result[name] = "Complete all three Core credentials."; });
  if (value.callback_secret && value.callback_secret.length < 32) result.callback_secret = "Use at least 32 characters.";
  if (props.only && !value[props.only]) result[props.only] = "Enter a value.";
  return result;
}

async function copySecret() {
  try { await navigator.clipboard.writeText(form.value.callback_secret); copied.value = true; }
  catch { error.value = "Copy failed. Select the value and copy it."; }
}

async function save() {
  errors.value = validate();
  if (Object.keys(errors.value).length) return;
  const value = form.value;
  const secrets = Object.fromEntries(fields.value.filter((name) => value[name]).map((name) => [name, value[name]]));
  saving.value = true; error.value = "";
  try {
    if (props.site) {
      await adminApi.updateSite(props.site.id, { origin: props.only ? props.site.origin : new URL(value.origin).origin, enabled: props.only ? props.site.enabled : value.enabled, secrets });
      emit("saved", props.site.id);
    } else {
      await adminApi.createSite({ id: value.id, origin: new URL(value.origin).origin, enabled: true, secrets });
      emit("saved", value.id);
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
      <form id="relay-site-form" class="admin-form" novalidate @submit.prevent="save">
        <template v-if="!only">
          <div v-if="!site"><FormControl v-model="form.id" label="Site ID" required placeholder="yrp" autocomplete="off" /><ErrorMessage :message="errors.id" /></div>
          <div :class="{ 'admin-full': site }"><FormControl v-model="form.origin" label="Site link" required type="url" placeholder="https://yrp.example.com" autocomplete="off" /><ErrorMessage :message="errors.origin" /></div>
          <FormControl v-if="site" v-model="form.enabled" type="checkbox" label="Site enabled" class="admin-full" />
          <div class="admin-sub">Core connection</div>
        </template>
        <p v-if="only === 'callback_secret'" class="admin-hint admin-full">Paste this new secret in Relay Core → Settings on this site, then save. Callbacks fail until both sides use the same secret.</p>
        <div v-for="name in fields" :key="name" :class="{ 'admin-full': name === 'callback_secret' || only }">
          <div v-if="name === 'callback_secret'" class="admin-generate">
            <FormControl v-model="form.callback_secret" :label="LABELS[name]" :type="only ? 'text' : 'password'" autocomplete="new-password" :placeholder="saved(name) ? 'Saved — leave blank to keep' : 'At least 32 characters'" />
            <Button v-if="only" :label="copied ? 'Copied' : 'Copy'" :icon-left="copied ? 'check' : 'copy'" @click="copySecret" />
            <Button v-else label="Generate" icon-left="refresh-cw" @click="form.callback_secret = newSecret(36)" />
          </div>
          <FormControl v-else v-model="form[name]" :label="LABELS[name]" :type="name === 'core_api_key' ? 'text' : 'password'" autocomplete="new-password" :placeholder="saved(name) ? 'Saved — leave blank to keep' : 'From the Frappe API user'" />
          <ErrorMessage :message="errors[name]" />
        </div>
        <p v-if="!only" class="admin-hint admin-full">Enter all three Core values together. Saved values are write-only; leave one blank to keep it.</p>
      </form>
      <ErrorMessage :message="error" />
    </template>
    <template #actions="{ close }">
      <Button label="Cancel" :disabled="saving" @click="close" />
      <Button type="submit" form="relay-site-form" :label="site ? 'Save' : 'Create site'" variant="solid" :loading="saving" />
    </template>
  </Dialog>
</template>
