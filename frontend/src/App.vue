<script setup>
import ThemeSwitcher from "./components/ThemeSwitcher.vue";
import { onMounted, ref } from "vue";
import { Button, Badge, FeatherIcon, ErrorMessage } from "@relay/ui";
import { useViewRoute } from "./useViewRoute";
import { useHubData } from "./composables/useHubData";
import AccountsView from "./views/AccountsView.vue";
const nav = [["Accounts", "briefcase"]];
const view = useViewRoute(
    nav.map(([name]) => name),
    "Accounts",
  ),
  mobile = ref(false);
const {
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
      <a href="#/accounts" class="hub-brand"><span class="hub-brand-mark"><FeatherIcon name="radio" class="h-4 w-4"/></span><strong class="nav-label">Relay Hub</strong></a><div class="hub-nav-caption nav-label">Administration</div>
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
      <div class="hub-nav-footer"><a href="./auth/users" class="hub-nav-item" title="Users"><FeatherIcon name="users" class="h-4 w-4"/><span class="nav-label">Users</span></a><a href="./auth/tokens" class="hub-nav-item" title="Auth keys"><FeatherIcon name="key" class="h-4 w-4"/><span class="nav-label">Auth keys</span></a></div>
    </aside>
    <main class="min-w-0 flex flex-col min-h-0">
      <header class="hub-header">
        <ThemeSwitcher />
        <button class="hub-mobile-menu" aria-label="Toggle navigation" :aria-expanded="mobile" @click="mobile=!mobile"><FeatherIcon name="menu" class="h-4 w-4"/></button><span class="hub-breadcrumb">Relay Hub</span><span class="hub-breadcrumb text-ink-gray-3">/</span><h1 class="text-base font-medium">{{ view }}</h1>
        <Badge
          :label="status"
          :theme="status === 'Ready' ? 'green' : 'orange'"
        />
        <div class="ml-auto flex gap-2">
          <Button label="Refresh" :loading="busy" @click="refresh" />
        </div>
      </header>
      <div class="hub-page">
        <ErrorMessage :message="error" />
        <AccountsView
          v-if="view === 'Accounts'"
          :accounts="accounts"
          :refresh="refresh"
          :page="accountPage"
          @search="searchAccounts"
          @page="changePage"
        />
      </div>
    </main>
  </div>
</template>
