<script setup>
import { ref, computed } from "vue";
import { Badge, Button, Dialog, FormControl, FeatherIcon, ErrorMessage } from "@relay/ui";
import RelayForm from "../../components/RelayForm.vue";
import InfiniteList from "../../components/InfiniteList.vue";
import { securityRequest, searchSecurityOptions } from "../../composables/security";

const props = defineProps({ data: Object });
const siteSearch = searchSecurityOptions("grants");
const query = ref("");
const state = row => row.Revoked ? 'Revoked' : row.Expires !== 'Never' && Date.parse(row.Expires) <= Date.now() ? 'Expired' : 'Active';
const list = ref();
const selected = ref(null);
const creating = ref(false);
const issuedToken = ref("");
const model = ref({ name: "", account: "", days: "0", scopes: [] });
const busy = ref(false);
const error = ref("");
const fields = computed(() => [
  { name: "name", label: "Key name", type: "Data", required: true },
  { name: "account", label: "Site", type: "Link", search: siteSearch, placeholder: "Search site…", required: true },
  { name: "days", label: "Expiry", type: "Select", options: [{ label: "No expiry — revoke manually", value: "0" }, { label: "30 days", value: "30" }, { label: "90 days", value: "90" }], required: true },
  { name: "scopes", label: "Permissions", type: "MultiSelect", options: props.data.Scopes, required: true, wide: true },
]);
async function load(params) {
  const data = await securityRequest("tokens?" + new URLSearchParams(params));
  return { items: data.Tokens, more: data.More };
}
function newKey() {
  model.value = { name: "", account: "", days: "0", scopes: [] };
  issuedToken.value = "";
  error.value = "";
  creating.value = true;
}
async function act(values) {
  busy.value = true;
  error.value = "";
  try {
    const result = await securityRequest("tokens", { csrf: props.data.CSRF, ...values });
    issuedToken.value = result.Token || "";
    if (!issuedToken.value) { creating.value = false; selected.value = null; }
    list.value?.reload();
  } catch (exception) {
    error.value = exception.message;
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <section class="entity-view" aria-label="Auth keys">
    <div class="view-heading"><div><h2>Auth keys</h2><p>Issue scoped credentials for a site and revoke them when no longer needed.</p></div><Button label="New auth key" icon-left="plus" variant="solid" @click="newKey" /></div>
    <div class="entity-toolbar"><FormControl v-model="query" placeholder="Search auth keys" aria-label="Search auth keys" type="search" autocomplete="off" /></div>
    <InfiniteList ref="list" :load="load" :query="query" :row-height="72">
      <template #default="{ row }">
        <button type="button" class="security-list-row entity-row" @click="selected = row; error = ''">
          <span class="entity-primary"><span class="entity-icon" aria-hidden="true"><FeatherIcon name="key" class="h-4 w-4" /></span><strong>{{ row.Name }}</strong></span>
          <span class="entity-site">{{ row.Account }} · {{ row.Expires === 'Never' ? 'No expiry' : row.Expires }}</span>
          <Badge :label="state(row)" :theme="state(row) === 'Active' ? 'green' : 'gray'" />
          <FeatherIcon name="chevron-right" class="h-4 w-4 text-ink-gray-4" />
        </button>
      </template>
    </InfiniteList>

    <Dialog :model-value="creating || !!selected" :options="{ title: creating ? (issuedToken ? 'Auth key created' : 'New auth key') : selected?.Name || 'Auth key', size: 'lg' }" :disable-outside-click-to-close="busy || !!issuedToken" @update:model-value="(value) => { if (!value && !issuedToken) { creating = false; selected = null; } }">
      <template #body-content>
        <ErrorMessage v-if="!creating" :message="error" />
        <div v-if="issuedToken" class="space-y-3"><p class="text-sm text-ink-gray-6">Copy this key now. It is shown only once.</p><code class="auth-key-value">{{ issuedToken }}</code></div>
        <RelayForm v-else-if="creating" v-model="model" :fields="fields" :error="error" :busy="busy" @submit="act(model)" />
        <dl v-else-if="selected" class="entity-details"><div><dt>Site</dt><dd>{{ selected.Account }}</dd></div><div><dt>Expiry</dt><dd>{{ selected.Expires === 'Never' ? 'No expiry' : selected.Expires }}</dd></div><div><dt>Status</dt><dd>{{ state(selected) }}</dd></div><div><dt>Permissions</dt><dd>{{ selected.Scopes }}</dd></div></dl>
      </template>
      <template #actions="{ close }"><div class="flex justify-end gap-2"><Button v-if="issuedToken" label="I copied the key" variant="solid" @click="issuedToken = ''; creating = false; close()" /><template v-else-if="creating"><Button label="Cancel" :disabled="busy" @click="creating = false; close()" /></template><template v-else><Button label="Close" @click="close" /><Button v-if="selected && !selected.Revoked" label="Revoke key" :loading="busy" @click="act({ action: 'revoke', id: selected.ID })" /></template></div></template>
    </Dialog>
  </section>
</template>
