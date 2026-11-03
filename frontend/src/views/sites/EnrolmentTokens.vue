<script setup>
import { computed, ref, watch } from "vue";
import { Badge, Button, Dialog, ErrorMessage, FormControl } from "@relay/ui";
import { adminApi, tokenPayload, tokenRow, validateTokenForm } from "../../composables/containers";

// One dialog with three modes: the token list, the new token form, and the new secret shown once.
const props = defineProps({ modelValue: Boolean });
const emit = defineEmits(["update:modelValue"]);
const KINDS = [{ label: "Bench token (many sites)", value: "bench" }, { label: "Setup code (one site)", value: "setup_code" }];
const mode = ref("list"), rows = ref([]), busy = ref(false), error = ref(""), revoking = ref(0);
const form = ref({}), errors = ref({}), created = ref(null), notice = ref("");
const title = computed(() => ({ list: "Enrolment tokens", form: "New enrolment token", created: "Token created" })[mode.value]);

async function refresh() {
  busy.value = true; error.value = "";
  try { rows.value = (await adminApi.enrolmentTokens()).map(tokenRow); }
  catch (exception) { error.value = exception.message; }
  finally { busy.value = false; }
}

function newToken() {
  form.value = { name: "", kind: "bench", origin_pattern: "", max_sites: "", expiry_days: "" };
  errors.value = {}; error.value = ""; mode.value = "form";
}

async function create() {
  errors.value = validateTokenForm(form.value);
  if (Object.keys(errors.value).length) return;
  busy.value = true; error.value = ""; notice.value = "";
  try {
    created.value = await adminApi.createEnrolmentToken(tokenPayload(form.value));
    mode.value = "created";
  } catch (exception) {
    error.value = exception.message;
  } finally {
    busy.value = false;
  }
}

async function copySecret() {
  try { await navigator.clipboard.writeText(created.value.message || created.value.secret); notice.value = "Copied."; }
  catch { notice.value = "Copy failed. Select the text and copy it."; }
}

async function revoke(row) {
  revoking.value = row.id; error.value = "";
  try { await adminApi.revokeEnrolmentToken(row.id); await refresh(); }
  catch (exception) { error.value = exception.message; }
  finally { revoking.value = 0; }
}

function back() { created.value = null; mode.value = "list"; refresh(); }

watch(() => props.modelValue, (open) => {
  created.value = null; mode.value = "list";
  if (open) refresh();
});
</script>

<template>
  <Dialog :model-value="modelValue" :options="{ title, size: 'admin' }" :disable-outside-click-to-close="busy || mode !== 'list'" @update:model-value="emit('update:modelValue', $event)">
    <template #body-content>
      <template v-if="mode === 'list'">
        <p class="admin-hint">A site with a token enrols itself and gets its own Hub token. Put a bench token in the bench config. Send a setup code to one site owner.</p>
        <p v-if="busy && !rows.length" role="status">Loading tokens…</p>
        <div v-if="rows.length" class="admin-table admin-spaced">
          <div class="admin-head cols-enrol" aria-hidden="true"><span>Name</span><span>Kind</span><span>Origin pattern</span><span>Sites</span><span>Expires</span><span>Status</span><span></span></div>
          <div v-for="row in rows" :key="row.id" class="admin-row cols-enrol">
            <span class="entity-primary"><strong>{{ row.name }}</strong></span>
            <span class="hide-m">{{ row.kindLabel }}</span>
            <span class="wide-m admin-mono">{{ row.origin_pattern || "Any site" }}</span>
            <span class="hide-m">{{ row.usage }}</span>
            <span class="hide-m">{{ row.expiry }}</span>
            <span class="admin-stat"><Badge :label="row.statusLabel" :theme="row.theme" /></span>
            <span><Button v-if="row.status === 'active'" label="Revoke" variant="ghost" :loading="revoking === row.id" :aria-label="'Revoke ' + row.name" @click="revoke(row)" /></span>
          </div>
        </div>
        <p v-else-if="!busy && !error" class="entity-prompt admin-spaced">No enrolment tokens yet.</p>
      </template>
      <form v-else-if="mode === 'form'" id="relay-enrolment-form" class="admin-form" novalidate @submit.prevent="create">
        <div class="admin-full"><FormControl v-model="form.name" label="Name" required placeholder="Acme bench" autocomplete="off" /><ErrorMessage :message="errors.name" /></div>
        <FormControl v-model="form.kind" type="select" label="Kind" :options="KINDS" class="admin-full" />
        <div class="admin-full"><FormControl v-model="form.origin_pattern" label="Origin pattern" :required="form.kind === 'bench'" placeholder="https://*.example.com" autocomplete="off" /><ErrorMessage :message="errors.origin_pattern" /></div>
        <div><FormControl v-model="form.max_sites" label="Max sites" :placeholder="form.kind === 'setup_code' ? '1' : 'No limit'" autocomplete="off" /><ErrorMessage :message="errors.max_sites" /></div>
        <div><FormControl v-model="form.expiry_days" label="Expiry days" :placeholder="form.kind === 'setup_code' ? '7' : 'No expiry'" autocomplete="off" /><ErrorMessage :message="errors.expiry_days" /></div>
        <p class="admin-hint admin-full">A site must prove it serves its origin. A leaked token cannot enrol an origin that the caller does not control.</p>
      </form>
      <template v-else>
        <p class="admin-hint">Copy this {{ created.token.kind === "setup_code" ? "setup code" : "token" }} now. The Hub does not show it again.</p>
        <div class="admin-secret"><span class="admin-label">{{ created.link ? "Setup link" : created.token.name }}</span><span class="admin-value admin-mono">{{ created.link || created.secret }}</span><Button label="Copy" icon-left="copy" @click="copySecret" /></div>
        <p v-if="created.message" class="admin-hint">{{ created.message }}</p>
        <p v-if="notice" role="status" class="admin-hint">{{ notice }}</p>
      </template>
      <ErrorMessage :message="error" />
    </template>
    <template #actions="{ close }">
      <template v-if="mode === 'list'"><Button label="Close" @click="close" /><Button label="New token" icon-left="plus" variant="solid" @click="newToken" /></template>
      <template v-else-if="mode === 'form'"><Button label="Back" :disabled="busy" @click="mode = 'list'" /><Button type="submit" form="relay-enrolment-form" label="Create token" variant="solid" :loading="busy" /></template>
      <Button v-else label="Done" variant="solid" @click="back" />
    </template>
  </Dialog>
</template>
