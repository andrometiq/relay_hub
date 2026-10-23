<script setup>
import ThemeSwitcher from "./components/ThemeSwitcher.vue";
import { onMounted, ref } from "vue";
import { Button, FeatherIcon, ErrorMessage } from "@relay/ui";
import { useViewRoute } from "./useViewRoute";
import { useHubData } from "./composables/useHubData";
import HubStatus from "./components/HubStatus.vue";
import OverviewView from "./views/OverviewView.vue";
import ChannelsView from "./views/ChannelsView.vue";
import DeliveryView from "./views/DeliveryView.vue";
import EventsView from "./views/EventsView.vue";
import AccountsView from "./views/AccountsView.vue";
const nav = [["Overview", "activity"], ["Accounts", "briefcase"], ["Connections", "message-circle"], ["Deliveries", "send"], ["Events", "list"]];
const view = useViewRoute(
    nav.map(([name]) => name),
    "Overview",
  ),
  mobile = ref(false);
const content = ref();
async function refreshView() { await refresh(); await content.value?.refresh?.(); }
const {
  runtime, deliveries, updated, revision,
  accounts,
  status,
  busy,
  error,
  refresh,
  accountPage,
  searchAccounts,
  changePage,
} = useHubData();
function navigate(name) {
  view.value = name;
  mobile.value = false;
}
onMounted(refresh);
</script>
<template>
  <div class="hub-shell" @keydown.esc="mobile=false"><button v-if="mobile" class="hub-nav-backdrop" aria-label="Close navigation" @click="mobile=false"/>
    <aside
      class="hub-nav"
      :class="{ 'mobile-nav-open': mobile }"
      aria-label="Main navigation"
    >
      <a href="#/overview" class="hub-brand"><span class="hub-brand-mark"><FeatherIcon name="radio" class="h-4 w-4"/></span><strong class="nav-label">Relay Hub</strong></a><div class="hub-nav-caption nav-label">Administration</div>
      <button
        v-for="[name, icon] in nav"
        :key="name"
        class="hub-nav-item"
        :class="{ selected: view === name }"
        :aria-label="name"
        :title="name"
        :aria-current="view === name ? 'page' : undefined"
        @click="navigate(name)"
      >
        <FeatherIcon :name="icon" class="h-4 w-4" /><span class="nav-label">{{
          name
        }}</span>
      </button>
      <div class="hub-nav-footer"><a href="./auth/users" class="hub-nav-item" title="Users"><FeatherIcon name="users" class="h-4 w-4"/><span class="nav-label">Users</span></a><a href="./auth/tokens" class="hub-nav-item" title="Auth keys"><FeatherIcon name="key" class="h-4 w-4"/><span class="nav-label">Auth keys</span></a><a href="./auth/sessions" class="hub-nav-item">Sessions</a><a href="./auth/password" class="hub-nav-item">Password</a></div>
    </aside>
    <main class="min-w-0 flex flex-col min-h-0">
      <header class="hub-header">
        <ThemeSwitcher />
        <button class="hub-mobile-menu" aria-label="Toggle navigation" :aria-expanded="mobile" @click="mobile=!mobile"><FeatherIcon name="menu" class="h-4 w-4"/></button><span class="hub-breadcrumb">Relay Hub</span><span class="hub-breadcrumb text-ink-gray-3">/</span><h1 class="text-base font-medium">{{ view }}</h1>
        <HubStatus :status="status" :error="error" :updated="updated" />
        <div class="ml-auto flex gap-2">
          <Button label="Refresh" :loading="busy" @click="refreshView" />
        </div>
      </header>
      <div class="hub-page">
        <ErrorMessage :message="error" />
        <OverviewView :key="revision" v-if="view === 'Overview'" :accounts="accounts" :total="accountPage.total" :status="status" :runtime="runtime" :deliveries="deliveries" @navigate="navigate" />
        <ChannelsView v-else-if="view === 'Connections'" ref="content" @navigate="navigate" />
        <DeliveryView v-else-if="view === 'Deliveries'" ref="content" @changed="refresh" />
        <EventsView v-else-if="view === 'Events'" ref="content" />
        <AccountsView
          v-if="view === 'Accounts'"
          :accounts="accounts"
          :busy="busy"
          :error="error"
          :refresh="refresh"
          :page="accountPage"
          @search="searchAccounts"
          @page="changePage"
        />
      </div>
    </main>
  </div>
</template>
