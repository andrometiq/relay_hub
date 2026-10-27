<script setup>
import { subscribeHub } from "./services/liveUpdates";
import ThemeSwitcher from "./components/ThemeSwitcher.vue";
import { onMounted, onUnmounted, ref, watch } from "vue";
import { Button, FeatherIcon, ErrorMessage } from "@relay/ui";
import { useViewRoute } from "./useViewRoute";
import { useHubData } from "./composables/useHubData";
import HubStatus from "./components/HubStatus.vue";
import OverviewView from "./views/OverviewView.vue";
import ChannelsView from "./views/ChannelsView.vue";
import DeliveryView from "./views/DeliveryView.vue";
import EventsView from "./views/EventsView.vue";
import ContainersView from "./views/containers/ContainersView.vue";
import ContainerDetail from "./views/containers/ContainerDetail.vue";
import SitesView from "./views/sites/SitesView.vue";
import SiteDetail from "./views/sites/SiteDetail.vue";
const nav = [["Overview", "activity"], ["Containers", "box"], ["Sites", "globe"], ["Connections", "message-circle"], ["Deliveries", "send"], ["Events", "list"]];
const { view, detail } = useViewRoute(nav.map(([name]) => name), "Overview", { accounts: "sites" });
const mobile = ref(false), detailTitle = ref("");
const content = ref();
watch(detail, () => { detailTitle.value = ""; });
async function refreshView() { await refresh(); await content.value?.refresh?.(); }
const {
  runtime, deliveries, updated, revision,
  accounts,
  status,
  busy,
  error,
  refresh,
  accountPage,
} = useHubData();
function navigate(name, id = "") {
  view.value = name;
  detail.value = id;
  mobile.value = false;
}
let unsubscribe;
onMounted(() => { refresh(); unsubscribe = subscribeHub(refreshView); });
onUnmounted(() => unsubscribe?.());
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
        <button class="hub-mobile-menu" aria-label="Toggle navigation" :aria-expanded="mobile" @click="mobile=!mobile"><FeatherIcon name="menu" class="h-4 w-4"/></button><span class="hub-breadcrumb">Relay Hub</span><span class="hub-breadcrumb text-ink-gray-3">/</span><h1 class="text-base font-medium">{{ detail ? detailTitle || detail : view }}</h1>
        <HubStatus :status="status" :error="error" :updated="updated" />
        <div class="ml-auto flex gap-2">
          <Button label="Refresh" :loading="busy" @click="refreshView" />
          <ThemeSwitcher />
        </div>
      </header>
      <div class="hub-page">
        <ErrorMessage :message="error" />
        <OverviewView :key="revision" v-if="view === 'Overview'" :accounts="accounts" :total="accountPage.total" :status="status" :runtime="runtime" :deliveries="deliveries" @navigate="navigate" />
        <ChannelsView v-else-if="view === 'Connections'" ref="content" @navigate="navigate" />
        <DeliveryView v-else-if="view === 'Deliveries'" ref="content" @changed="refresh" />
        <EventsView v-else-if="view === 'Events'" ref="content" />
        <ContainerDetail v-else-if="view === 'Containers' && detail" :id="detail" ref="content" @title="detailTitle = $event" />
        <ContainersView v-else-if="view === 'Containers'" ref="content" @open="navigate('Containers', $event)" />
        <SiteDetail v-else-if="view === 'Sites' && detail" :id="detail" ref="content" @title="detailTitle = $event" />
        <SitesView v-else-if="view === 'Sites'" ref="content" @open="navigate('Sites', $event)" />
      </div>
    </main>
  </div>
</template>
