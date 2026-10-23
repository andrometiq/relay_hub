<script setup>
import { ref, watch } from "vue";
import {
  Badge,
  Button,
  Dialog,
  ErrorMessage,
  FormControl,
  FeatherIcon,
} from "@relay/ui";
import AccountPicker from "../components/AccountPicker.vue";
import { hubApi } from "../services/hubApi";

defineProps(["deliveries"]);
const account = ref("");
const rows = ref([]);
const more = ref(false);
const busy = ref(false);
const error = ref("");
const selected = ref(null);
const resolving = ref(false);
const outcome = ref("submitted");
const note = ref("");
const providerId = ref("");
let sequence = 0;

async function load(append = false) {
  if (!account.value || busy.value) return;
  const current = ++sequence;
  busy.value = true;
  error.value = "";
  const cursor = append ? String(rows.value.at(-1)?.id || "") : "";
  try {
    const result = await hubApi(
      "delivery-queue?" +
        new URLSearchParams({
          account: account.value,
          channel: "WhatsApp",
          after: cursor,
        }),
    );
    if (current === sequence) {
      const incoming = result.deliveries || [];
      const known = new Set(rows.value.map((row) => String(row.id)));
      rows.value = append
        ? [...rows.value, ...incoming.filter((row) => !known.has(String(row.id)))]
        : incoming;
      more.value = !!result.more;
    }
  } catch (exception) {
    if (current === sequence) error.value = exception.message;
  } finally {
    if (current === sequence) busy.value = false;
  }
}
watch(account, () => {
  ++sequence;
  selected.value = null;
  rows.value = [];
  more.value = false;
  load();
});
function scrolled(event) {
  const node = event.currentTarget;
  if (
    more.value &&
    !busy.value &&
    node.scrollHeight - node.scrollTop - node.clientHeight < 180
  )
    load(true);
}
function open(row) {
  selected.value = row;
  resolving.value = false;
  outcome.value = "submitted";
  note.value = "";
  providerId.value = "";
}
async function change(action) {
  if (!selected.value || busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    await hubApi("delivery-queue", {
      account: account.value,
      channel: "WhatsApp",
      action,
      id: String(selected.value.id),
      ...(action === "resolve"
        ? {
            outcome: outcome.value,
            note: note.value,
            provider_id: providerId.value,
          }
        : {}),
    });
    selected.value = null;
    await load();
  } catch (exception) {
    error.value = exception.message;
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <section aria-label="Deliveries" class="entity-view">
    <div class="view-heading">
      <div>
        <h2>Deliveries</h2>
        <p>Inspect queued WhatsApp sends and resolve uncertain outcomes.</p>
      </div>
      <Button
        label="Refresh"
        icon-left="refresh-cw"
        :disabled="!account"
        :loading="busy"
        @click="load()"
      />
    </div>
    <div class="metrics">
      <article>
        <span>Queued</span><strong>{{ deliveries.queued || 0 }}</strong>
      </article>
      <article>
        <span>Submitted</span><strong>{{ deliveries.submitted || 0 }}</strong>
      </article>
      <article>
        <span>Needs review</span><strong>{{ deliveries.uncertain || 0 }}</strong>
      </article>
    </div>
    <div class="entity-toolbar mt-6">
      <AccountPicker v-model="account" :disabled="busy" />
    </div>
    <ErrorMessage :message="error" />
    <p v-if="!account" class="entity-prompt">
      Select an account to view its WhatsApp delivery queue.
    </p>
    <template v-else>
      <div class="delivery-list-head entity-list-head" aria-hidden="true">
        <span>Delivery</span><span>Message</span><span>State</span
        ><span>Provider</span><span></span>
      </div>
      <div
        class="delivery-list infinite-list"
        aria-label="WhatsApp deliveries"
        @scroll.passive="scrolled"
      >
        <button
          v-for="row in rows"
          :key="row.id"
          type="button"
          class="delivery-list-row entity-row"
          :aria-label="'Open delivery ' + row.id"
          @click="open(row)"
        >
          <span class="entity-primary">
            <span class="entity-icon" aria-hidden="true"
              ><FeatherIcon name="send" class="h-4 w-4"
            /></span>
            <strong>#{{ row.id }}</strong>
          </span>
          <span class="entity-site">{{ row.operation || "WhatsApp message" }}</span>
          <span><Badge :label="row.state" /></span>
          <span><Badge :label="row.delivery_status || 'Pending'" /></span>
          <FeatherIcon name="chevron-right" class="h-4 w-4 text-ink-gray-4" />
        </button>
        <p v-if="busy" class="infinite-list__status" role="status">Loading…</p>
        <p v-else-if="!rows.length" class="entity-prompt">
          No deliveries for this account.
        </p>
      </div>
    </template>

    <Dialog
      :model-value="!!selected"
      :options="{ title: selected ? 'Delivery #' + selected.id : 'Delivery', size: 'lg' }"
      :disable-outside-click-to-close="busy"
      @update:model-value="(value) => !value && (selected = null)"
    >
      <template #body-content>
        <div v-if="selected" class="space-y-5">
          <dl class="entity-details">
            <div><dt>Account</dt><dd>{{ account }}</dd></div>
            <div><dt>Channel</dt><dd>WhatsApp</dd></div>
            <div><dt>Message type</dt><dd>{{ selected.operation || "Message" }}</dd></div>
            <div><dt>Queue state</dt><dd>{{ selected.state }}</dd></div>
            <div><dt>Provider status</dt><dd>{{ selected.delivery_status || "Pending" }}</dd></div>
            <div v-if="selected.error"><dt>Issue</dt><dd>{{ selected.error }}</dd></div>
          </dl>
          <form v-if="resolving" class="space-y-4" @submit.prevent="change('resolve')">
            <p class="text-sm text-ink-gray-6">
              Record the outcome only after checking it in Meta. This action
              never resends the message.
            </p>
            <label class="block text-xs text-ink-gray-6"
              >Verified outcome
              <select v-model="outcome" class="hub-select mt-1">
                <option value="submitted">Provider accepted the message</option>
                <option value="rejected">Provider rejected the message</option>
              </select>
            </label>
            <FormControl
              v-if="outcome === 'submitted'"
              v-model="providerId"
              label="Provider message ID"
            />
            <FormControl
              v-model="note"
              type="textarea"
              label="Verification evidence"
            />
          </form>
        </div>
      </template>
      <template #actions="{ close }">
        <div class="flex justify-end gap-2">
          <Button label="Close" :disabled="busy" @click="close" />
          <Button
            v-if="selected?.state === 'queued'"
            label="Cancel queued send"
            :loading="busy"
            @click="change('cancel')"
          />
          <Button
            v-if="selected?.state === 'uncertain' && !resolving"
            label="Resolve outcome"
            variant="solid"
            @click="resolving = true"
          />
          <Button
            v-if="resolving"
            label="Record outcome"
            variant="solid"
            :loading="busy"
            :disabled="note.trim().length < 10 || note.length > 2000"
            @click="change('resolve')"
          />
        </div>
      </template>
    </Dialog>
  </section>
</template>
