<script setup>
import { ref } from "vue";
import { Badge, Button, Dialog, FormControl, FeatherIcon } from "@relay/ui";
import RelayForm from "../../components/RelayForm.vue";
import InfiniteList from "../../components/InfiniteList.vue";
import { securityRequest, searchSecurityOptions } from "../../composables/security";

const props = defineProps({ data: Object });
const accountSearch = searchSecurityOptions("accounts");
const query = ref("");
const list = ref();
const mode = ref("");
const selected = ref(null);
const model = ref({});
const busy = ref(false);
const error = ref("");
const createFields = [
  { name: "username", label: "Username", type: "Data", required: true },
  { name: "role", label: "Role", type: "Select", options: [{ label: "Member", value: "member" }, { label: "Administrator", value: "admin" }], required: true },
  { name: "password", label: "Initial password", type: "Password", minLength: 14, required: true, wide: true, autocomplete: "new-password" },
];

function open(row = null, nextMode = row ? "details" : "create") {
  selected.value = row;
  mode.value = nextMode;
  model.value = nextMode === "create" ? { username: "", password: "", role: "member" } : nextMode === "grant" ? { account: "" } : { password: "" };
  error.value = "";
}
function fields() {
  if (mode.value === "create") return createFields;
  if (mode.value === "grant") return [{ name: "account", label: "Account", type: "Link", search: accountSearch, placeholder: "Search account ID…", required: true, wide: true }];
  return [{ name: "password", label: "New password", type: "Password", required: true, minLength: 14, wide: true, autocomplete: "new-password" }];
}
async function load(params) {
  const data = await securityRequest("users?" + new URLSearchParams(params));
  return { items: data.Users, more: data.More };
}
async function act(values, route = "users", close = true) {
  busy.value = true;
  error.value = "";
  try {
    await securityRequest(route, { csrf: props.data.CSRF, ...values });
    if (close) { mode.value = ""; selected.value = null; }
    list.value?.reload();
    return true;
  } catch (exception) {
    error.value = exception.message;
  } finally {
    busy.value = false;
  }
}
async function toggleEnabled() {
  const row = selected.value;
  if (!row) return;
  if (await act({ action: "enabled", id: row.id, enabled: !row.enabled }, "users", false)) row.enabled = !row.enabled;
}
function submit() {
  if (mode.value === "reset") act({ ...model.value, id: selected.value.id }, "password");
  else act({ ...model.value, action: mode.value, id: selected.value?.id, allow: "true" });
}
</script>

<template>
  <section class="entity-view" aria-label="Users">
    <div class="view-heading"><div><h2>Users</h2><p>Manage Hub access and account assignments.</p></div><Button label="New user" icon-left="plus" variant="solid" @click="open()" /></div>
    <div class="entity-toolbar"><FormControl v-model="query" placeholder="Search users" aria-label="Search users" type="search" autocomplete="off" /></div>
    <InfiniteList ref="list" :load="load" :query="query" :row-height="64">
      <template #default="{ row }">
        <button type="button" class="security-list-row entity-row" @click="open(row)">
          <span class="entity-primary"><span class="entity-icon" aria-hidden="true"><FeatherIcon name="user" class="h-4 w-4" /></span><strong>{{ row.username }}</strong></span>
          <span class="entity-site">{{ row.role === "admin" ? "Administrator" : "Member" }}</span>
          <Badge :label="row.enabled ? 'Active' : 'Disabled'" :theme="row.enabled ? 'green' : 'gray'" />
          <FeatherIcon name="chevron-right" class="h-4 w-4 text-ink-gray-4" />
        </button>
      </template>
    </InfiniteList>

    <Dialog :model-value="!!mode" :options="{ title: mode === 'create' ? 'New user' : mode === 'grant' ? 'Account access' : mode === 'reset' ? 'Reset password' : selected?.username || 'User', size: 'lg' }" :disable-outside-click-to-close="busy" @update:model-value="(value) => !value && (mode = '')">
      <template #body-content>
        <RelayForm v-if="['create','grant','reset'].includes(mode)" v-model="model" :fields="fields()" :error="error" :busy="busy" @submit="submit" />
        <dl v-else-if="selected" class="entity-details"><div><dt>Username</dt><dd>{{ selected.username }}</dd></div><div><dt>Role</dt><dd>{{ selected.role === 'admin' ? 'Administrator' : 'Member' }}</dd></div><div><dt>Status</dt><dd>{{ selected.enabled ? 'Active' : 'Disabled' }}</dd></div></dl>
      </template>
      <template #actions="{ close }">
        <div class="flex justify-end gap-2">
          <template v-if="mode === 'details' && selected"><Button label="Account access" @click="open(selected, 'grant')" /><Button v-if="selected.id !== data.User.id" label="Reset password" @click="open(selected, 'reset')" /><Button :label="selected.enabled ? 'Disable' : 'Enable'" :loading="busy" @click="toggleEnabled" /></template>
          <template v-else><Button label="Cancel" :disabled="busy" @click="close" /><Button v-if="mode === 'grant'" label="Remove access" :disabled="busy || !model.account" @click="act({ action: 'grant', id: selected.id, account: model.account, allow: 'false' })" /></template>
        </div>
      </template>
    </Dialog>
  </section>
</template>
