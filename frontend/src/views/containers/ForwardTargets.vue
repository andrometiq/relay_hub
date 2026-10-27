<script setup>
import { onBeforeUnmount, ref, watch } from "vue";
import { Badge, Button, ErrorMessage, FormControl } from "@relay/ui";
import { adminApi, forwardState } from "../../composables/containers";

// Forward targets get a copy of each verified webhook of this container, bot or mailbox.
const props = defineProps({ provider: String, scopeKey: String });
const targets = ref([]), records = ref([]), counts = ref({}), next = ref(""), address = ref(""), error = ref(""), busy = ref(false);
let ticket = 0;

async function refresh(before = "") {
  const current = ++ticket;
  error.value = "";
  try {
    const data = await adminApi.forwards(props.provider, props.scopeKey, before);
    if (current !== ticket) return;
    targets.value = data.targets || []; counts.value = data.counts || {}; next.value = data.next || "";
    records.value = before ? [...records.value, ...(data.records || [])] : data.records || [];
  } catch (exception) {
    if (current === ticket) error.value = exception.message;
  }
}

async function change(action) {
  busy.value = true; error.value = "";
  try {
    await action();
    await refresh();
    return true;
  } catch (exception) {
    error.value = exception.message;
    return false;
  } finally {
    busy.value = false;
  }
}

async function add() {
  if (await change(() => adminApi.addForward(props.provider, props.scopeKey, address.value.trim()))) address.value = "";
}
function remove(target) {
  if (!window.confirm("Stop forwarding to this URL? Its results are deleted too.")) return;
  change(() => adminApi.removeForward(props.provider, props.scopeKey, target.id));
}

watch(() => [props.provider, props.scopeKey], () => { records.value = []; refresh(); }, { immediate: true });
onBeforeUnmount(() => { ticket++; });
</script>

<template>
  <div class="forward-targets">
    <div class="admin-card-head">
      <div><h3>Forward copies to</h3><p>Each verified webhook is also sent, with the same body and signature headers, to these URLs. Up to 3 tries, results kept 7 days.</p></div>
      <div class="admin-actions"><Button label="Refresh" icon-left="refresh-cw" @click="refresh()" /></div>
    </div>
    <ul v-if="targets.length" class="forward-list">
      <li v-for="target in targets" :key="target.id"><code>{{ target.url }}</code><Button label="Remove" :disabled="busy" @click="remove(target)" /></li>
    </ul>
    <p v-else class="admin-hint">No forward targets. Webhooks are only handled by the Hub.</p>
    <form class="forward-add" @submit.prevent="add">
      <FormControl v-model="address" aria-label="Forward URL" placeholder="https://old-system.example.com/webhooks/meta" autocomplete="off" />
      <Button type="submit" label="Add URL" icon-left="plus" :disabled="busy || !address.trim()" />
    </form>
    <ErrorMessage :message="error" />
    <template v-if="targets.length || records.length">
      <p class="admin-hint">{{ counts.delivered || 0 }} delivered · {{ counts.pending || 0 }} waiting · {{ counts.failed || 0 }} failed</p>
      <div v-if="records.length" class="admin-table">
        <div class="admin-head cols-fw" aria-hidden="true"><span>Received</span><span>Target</span><span>Result</span><span>Tries</span><span>Last error</span></div>
        <div v-for="row in records" :key="row.id" class="admin-row cols-fw">
          <span>{{ new Date(row.created_at).toLocaleString() }}</span>
          <span class="hide-m admin-mono">{{ row.url }}</span>
          <span><Badge :label="forwardState(row).label" :theme="forwardState(row).theme" /><small v-if="row.status_code">HTTP {{ row.status_code }} · {{ row.latency_ms }} ms</small></span>
          <span class="hide-m">{{ row.attempts }}</span>
          <span class="wide-m">{{ row.error }}</span>
        </div>
      </div>
      <Button v-if="next" label="Load older" @click="refresh(next)" />
    </template>
  </div>
</template>
