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
  const accountPage = reactive({
    query: "",
    after: "",
    next: "",
    previous: [],
    total: 0,
    busy: false,
  });
  function applyAccounts(data) {
    accounts.value = data.accounts || [];
    accountPage.next = data.next || "";
    accountPage.total = data.total ?? accounts.value.length;
  }
  const accountPath = (query, after) =>
    "accounts?" + new URLSearchParams({ query, after });
  async function fetchAccounts(query, after, previous) {
    if (accountPage.busy || busy.value) return;
    accountPage.busy = true;
    error.value = "";
    try {
      const data = await hubApi(accountPath(query, after));
      applyAccounts(data);
      accountPage.query = query;
      accountPage.after = after;
      accountPage.previous = previous;
    } catch (e) {
      error.value = e.message;
    } finally {
      accountPage.busy = false;
      if (refreshPending) await refresh();
    }
  }
  const searchAccounts = (query) => fetchAccounts(query, "", []);
  function changePage(direction) {
    if (direction === "next" && accountPage.next)
      return fetchAccounts(accountPage.query, accountPage.next, [
        ...accountPage.previous,
        accountPage.after,
      ]);
    if (direction === "previous" && accountPage.previous.length)
      return fetchAccounts(
        accountPage.query,
        accountPage.previous.at(-1),
        accountPage.previous.slice(0, -1),
      );
  }
  let refreshPending = false;
  async function refresh() {
    if (busy.value || accountPage.busy) { refreshPending = true; return; }
    refreshPending = false;
    busy.value = true;
    error.value = "";
    const results = await Promise.allSettled(
      [
        "ready",
        accountPath(accountPage.query, accountPage.after),
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
    searchAccounts,
    changePage,
  };
}
