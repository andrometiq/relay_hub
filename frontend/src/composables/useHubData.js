import { ref, reactive } from "vue";
import { hubApi } from "../services/hubApi";
export function useHubData() {
  const updated = ref("");
  const revision = ref(0);
  const accounts = ref([]),
    runtime = ref({}),
    deliveries = ref({});
  const status = ref("Checking"),
    busy = ref(false),
    error = ref("");
  const accountPage = reactive({ total: 0 });
  function applyAccounts(data) {
    accounts.value = data.accounts || [];
    accountPage.total = data.total ?? accounts.value.length;
  }
  let refreshPending = false;
  async function refresh() {
    if (busy.value) { refreshPending = true; return; }
    refreshPending = false;
    busy.value = true;
    error.value = "";
    const results = await Promise.allSettled(
      [
        "ready",
        "accounts",
        "channels",
        "deliveries",
      ].map((path) => hubApi(path)),
    );
    status.value = results[0].status === "fulfilled" ? "Ready" : "Unavailable";
    if (results[1].status === "fulfilled") applyAccounts(results[1].value);
    if (results[2].status === "fulfilled") runtime.value = results[2].value;
    if (results[3].status === "fulfilled")
      deliveries.value = results[3].value.counts || {};
    error.value =
      results.find((result) => result.status === "rejected")?.reason.message ||
      "";
    updated.value = new Date().toLocaleTimeString();
    revision.value++;
    busy.value = false;
    if (refreshPending) await refresh();
  }
  return {
    updated, revision,
    accounts,
    runtime,
    deliveries,
    status,
    busy,
    error,
    refresh,
    accountPage,
  };
}
