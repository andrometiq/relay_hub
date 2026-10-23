<script setup>
import { computed, ref, toRef } from "vue";
import { Badge, Button, Dialog, FormControl, FeatherIcon } from "@relay/ui";
import AccountSetupNavigation from "../components/AccountSetupNavigation.vue";
import ChannelCredentials from "../components/ChannelCredentials.vue";
import DocumentForm from "../components/DocumentForm.vue";
import AccountPagination from "../components/AccountPagination.vue";

import { useAccountEditor, accountFields, whatsappReady } from "../composables/useAccountEditor";

const props = defineProps({ accounts: Array, refresh: Function, page: Object, busy: Boolean, error: String });
const emit = defineEmits(["search", "page"]);
const query = ref(props.page.query || "");

const activeStep = ref("account");
const steps = ["account", "meta", "webhook", "features", "delivery"];
const stepFields = {
  account: ["id", "site", "enabled"],
  meta: ["secret", "phone_id", "waba_id", "app_id", "graph_version"],
  webhook: ["app_secret", "verify_token"],
  features: ["messages_per_second"],
  delivery: ["core_api_key", "core_api_secret", "callback_secret"],
};

const reload = async () => {
  await props.refresh();

};
const {
  editing,
  editingID,
  draft,
  secrets,
  errors,
  notice,
  saving,
  edit: beginEdit,
  close,
  save, retryActivation, activationPending,
} = useAccountEditor(toRef(props, "accounts"), reload);

const fields = computed(() =>
  accountFields.map((field) => ({ ...field, readOnly: field.name === "id" && !!editingID.value })),
);
const configured = (row) => whatsappReady(row);
const draftChannel = computed(() => draft.value.channelConfig?.WhatsApp || {});
const draftChannelEnabled = computed(() => !!draft.value.channelEnabled?.WhatsApp);
const draftChannelReady = computed(() => draftChannelEnabled.value && draftChannel.value.connection_status === "connected");
const stepIndex = computed(() => steps.indexOf(activeStep.value));

function openAccount(account) {
  beginEdit(account);
  activeStep.value = account && !configured(account) ? "meta" : "account";
}

function firstInvalidStep() {
  return steps.find((step) => stepFields[step].some((field) => errors.value[field]));
}

async function submitAccount() {
  const saved = await save();
  if (!saved) activeStep.value = firstInvalidStep() || activeStep.value;
}

function moveStep(offset) {
  const next = Math.min(steps.length - 1, Math.max(0, stepIndex.value + offset));
  activeStep.value = steps[next];
}

</script>

<template>
  <section aria-label="Accounts" class="entity-view">
    <div class="view-heading">
      <div>
        <h2>Accounts</h2>
        <p>Configure each site’s WhatsApp credentials, webhook and Core callbacks.</p>
      </div>
      <Button label="New account" variant="solid" icon-left="plus" @click="openAccount(null)" />
    </div>

    <p v-if="notice && !editing" role="status" class="feedback-panel">{{ notice }}</p>

    <div class="entity-toolbar">
      <FormControl v-model="query" type="search" placeholder="Search by account or site" aria-label="Search accounts" autocomplete="off" />
      <Button label="Search" :loading="busy || page.busy" @click="emit('search', query)" />
      <span>{{ page.total }} account{{ page.total === 1 ? "" : "s" }}</span>
    </div>

    <div class="entity-list-head" aria-hidden="true">
      <span>Account</span><span>Site</span><span>WhatsApp</span><span>Status</span><span></span>
    </div>
    <p v-if="busy || page.busy" role="status">Loading accounts…</p>
    <div class="account-list">
        <button v-for="row in accounts" :key="row.id" type="button" class="entity-row" :aria-label="'Open account ' + row.id" @click="openAccount(row)">
          <span class="entity-primary"><span class="entity-icon" aria-hidden="true"><FeatherIcon name="briefcase" class="h-4 w-4" /></span><strong>{{ row.id }}</strong></span>
          <span class="entity-site">{{ row.site }}</span>
          <span><Badge :label="configured(row) ? 'Credentials verified' : 'Setup needed'" :theme="configured(row) ? 'green' : 'orange'" /></span>
          <span><Badge :label="row.enabled ? 'Enabled' : 'Disabled'" :theme="row.enabled ? 'green' : 'gray'" /></span>
          <FeatherIcon name="chevron-right" class="h-4 w-4 text-ink-gray-4" />
        </button>
    </div>
    <p v-if="!busy && !page.busy && !error && !accounts.length" class="entity-prompt">No accounts found. Create an account to begin WhatsApp setup.</p>
    <AccountPagination :page="{ ...page, busy: busy || page.busy }" @page="emit('page', $event)" />

    <Dialog
      v-model="editing"
      :disable-outside-click-to-close="saving"
      :options="{ title: editingID ? `Account · ${editingID}` : 'New account', size: 'xl' }"
      @close="close"
    >
      <template #body-content>
        <form id="relay-account-form" class="account-editor" novalidate @submit.prevent="submitAccount">
          <aside class="account-editor__sidebar">
            <div class="account-editor__summary">
              <span class="account-editor__avatar">{{ (draft.id || "N").charAt(0).toUpperCase() }}</span>
              <div><strong>{{ draft.id || "New account" }}</strong><small>{{ draft.site || "Add a Frappe site" }}</small></div>
            </div>
            <AccountSetupNavigation
              :active="activeStep"
              :errors="errors"
              :channel-enabled="draftChannelEnabled"
              :channel-ready="draftChannelReady"
              @select="activeStep = $event"
            />
          </aside>

          <main class="account-editor__main">
            <section v-if="activeStep === 'account'" class="account-pane">
              <div class="setup-pane-heading">
                <div><h4>Account details</h4><p>The Hub uses this identity to route WhatsApp events to the correct Frappe site.</p></div>
              </div>
              <DocumentForm v-model="draft" :fields="fields" :errors="errors" />
              <div class="account-route-preview">
                <span><FeatherIcon name="globe" /></span>
                <div><strong>One site, one WhatsApp connection</strong><p>Messages stay scoped to this account with its own credentials, webhook and Core callback configuration.</p></div>
              </div>
            </section>
            <ChannelCredentials
              v-else
              channel="WhatsApp"
              :section="activeStep"
              :account-id="draft.id"
              :configured="draft.configured?.WhatsApp || []"
              :errors="errors"
              v-model:config="draft.channelConfig.WhatsApp"
              v-model:enabled="draft.channelEnabled.WhatsApp"
              v-model:secret="secrets.WhatsApp"
            />
          </main>
        </form>
      </template>
      <template #actions="{ close: closeDialog }">
        <div class="w-full"><p v-if="notice" role="alert" class="feedback-panel">{{ notice }}</p>
        <div class="account-editor__actions">
          <Button label="Cancel" :disabled="saving" @click="closeDialog" />
          <span class="account-editor__action-spacer"></span>
          <Button v-if="stepIndex > 0" label="Back" icon-left="arrow-left" :disabled="saving" @click="moveStep(-1)" />
          <Button v-if="stepIndex < steps.length - 1" label="Next" icon-right="chevron-right" :disabled="saving" @click="moveStep(1)" />
          <Button v-if="activationPending" label="Retry activation" :loading="saving" @click="retryActivation" />
          <Button type="submit" form="relay-account-form" label="Save account" variant="solid" :loading="saving" />
        </div></div>
      </template>
    </Dialog>
  </section>
</template>
