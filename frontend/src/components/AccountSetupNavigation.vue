<script setup>
import { Badge, FeatherIcon } from "@relay/ui";

const props = defineProps({
  active: { type: String, required: true },
  errors: { type: Object, default: () => ({}) },
  channelEnabled: Boolean,
  channelReady: Boolean,
});

defineEmits(["select"]);

const steps = [
  { id: "account", label: "Account", description: "Site and account access", icon: "globe", fields: ["id", "site", "enabled"] },
  { id: "meta", label: "Meta connection", description: "Business IDs and token", icon: "link", fields: ["secret", "phone_id", "waba_id", "app_id", "graph_version"] },
  { id: "webhook", label: "Webhook", description: "Callback and verification", icon: "radio", fields: ["app_secret", "verify_token"] },
  { id: "features", label: "Features", description: "Templates and metadata", icon: "settings", fields: [] },
  { id: "delivery", label: "Core delivery", description: "Secure Frappe callbacks", icon: "send", fields: ["core_api_key", "core_api_secret", "callback_secret"] },
];

const countErrors = (step) =>
  step.fields.reduce((count, field) => count + (props.errors[field] ? 1 : 0), 0);
</script>

<template>
  <nav class="account-setup-nav" aria-label="Account setup sections">
    <div class="account-setup-nav__group">
      <span>Account</span>
      <button
        v-for="step in steps.slice(0, 1)"
        :key="step.id"
        type="button"
        :class="{ active: active === step.id, invalid: countErrors(step) }"
        :aria-current="active === step.id ? 'step' : undefined"
        @click="$emit('select', step.id)"
      >
        <span class="account-setup-nav__icon"><FeatherIcon :name="step.icon" /></span>
        <span class="account-setup-nav__copy"><strong>{{ step.label }}</strong><small>{{ step.description }}</small></span>
        <Badge v-if="countErrors(step)" :label="countErrors(step)" theme="red" />
      </button>
    </div>

    <div class="account-setup-nav__group">
      <div class="account-setup-nav__channel-heading">
        <span>Channels</span>
        <Badge :label="channelReady ? 'Verified' : channelEnabled ? 'Setup needed' : 'Disabled'" :theme="channelReady ? 'green' : channelEnabled ? 'orange' : 'gray'" />
      </div>
      <div class="account-setup-nav__channel">
        <div class="account-setup-nav__channel-name">
          <span class="account-setup-nav__channel-mark"><FeatherIcon name="message-circle" /></span><strong>WhatsApp</strong>
        </div>
        <button
          v-for="step in steps.slice(1)"
          :key="step.id"
          type="button"
          :class="{ active: active === step.id, invalid: countErrors(step) }"
          :aria-current="active === step.id ? 'step' : undefined"
          @click="$emit('select', step.id)"
        >
          <span class="account-setup-nav__icon"><FeatherIcon :name="step.icon" /></span>
          <span class="account-setup-nav__copy"><strong>{{ step.label }}</strong><small>{{ step.description }}</small></span>
          <Badge v-if="countErrors(step)" :label="countErrors(step)" theme="red" />
        </button>
      </div>
    </div>
  </nav>
</template>
