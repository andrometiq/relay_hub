import { ref, toRaw } from "vue";
import { validateFields } from "../components/formValidation.js";
import { hubApi } from "../services/hubApi.js";
import {
  accountSavePayload,
  activeChannels,
  whatsappReady,
  validateWhatsAppDraft,
} from "./accountForm.js";

const WHATSAPP = "WhatsApp";
export { activeChannels, whatsappReady, validateWhatsAppDraft };

export const accountFields = [
  { name: "id", label: "Account ID", type: "Data", required: true },
  {
    name: "site",
    label: "Frappe site URL",
    type: "Data",
    required: true,
    url: true,
    placeholder: "https://erp.example.com",
    wide: true,
  },
  { name: "enabled", label: "Enable this account", type: "Check", wide: true },
];

export function useAccountEditor(accounts, saved) {
  const activationPending = ref(false);
  const editing = ref(false),
    editingID = ref(null),
    draft = ref({}),
    secrets = ref({});
  const errors = ref({}),
    notice = ref(""),
    saving = ref(false);

  function edit(account) {
    activationPending.value = false;
    editingID.value = account?.id || null;
    const channels = toRaw(account?.channels || []);
    const whatsapp = channels.find((channel) => channel.channel === WHATSAPP);
    draft.value = {
      id: account?.id || "",
      site: account?.site || "",
      enabled: account?.enabled ?? true,
      preservedChannels: structuredClone(toRaw(channels.filter(channel => channel.channel !== WHATSAPP))),
      channels: [WHATSAPP],
      channelConfig: {
        [WHATSAPP]: {
          graph_version: "v25.0",
          templates: true,
          sync_metadata: true,
          groups: false,
          calls: false,
          ...structuredClone(toRaw(whatsapp?.config || {})),
        },
      },
      channelEnabled: { [WHATSAPP]: whatsapp?.enabled ?? true },
      configured: {
        [WHATSAPP]: whatsapp?.configured_secrets || [],
      },
    };
    secrets.value = { [WHATSAPP]: "" };
    errors.value = {};
    notice.value = "";
    editing.value = true;
  }

  function close() {
    if (saving.value) return;
    editing.value = false;
    errors.value = {};
  }

  async function save() {
    if (saving.value) return false;
    errors.value = {
      ...validateFields(accountFields, draft.value),
      ...validateWhatsAppDraft(draft.value, {
        ...draft.value.channelConfig?.[WHATSAPP],
        secret: secrets.value[WHATSAPP],
      }),
    };
    if (
      accounts.value.some(
        (account) =>
          account.id.toLowerCase() === draft.value.id.trim().toLowerCase() &&
          account.id !== editingID.value,
      )
    )
      errors.value.id = "An account with this ID already exists.";
    if (Object.keys(errors.value).length) return false;
    saving.value = true;
    notice.value = "";
    try {
      const account = accountSavePayload(draft.value);
      await hubApi("accounts", {
        account,
        secrets: { [WHATSAPP]: secrets.value[WHATSAPP] },
        create: editingID.value === null,
      });
      editingID.value = account.id;
      activationPending.value = true;
      // Saved credentials are write-only. Keep their presence, clear replacement values.
      const config = draft.value.channelConfig[WHATSAPP];
      const privateKeys = ["app_secret", "verify_token", "core_api_key", "core_api_secret", "callback_secret"];
      draft.value.configured[WHATSAPP] = [...new Set([...draft.value.configured[WHATSAPP], ...privateKeys.filter(key => config[key]), ...(secrets.value[WHATSAPP] ? ["secret"] : [])])];
      privateKeys.forEach(key => delete config[key]);
      secrets.value[WHATSAPP] = "";
      try { await hubApi("accounts/reload", {}); }
      catch (error) {
        notice.value = `Account saved. Activation failed: ${error.message} Use Retry activation.`;
        await saved();
        return false;
      }
      activationPending.value = false;
      notice.value = account.enabled ? "Account saved and runtime reloaded. Check Connections for webhook and Core delivery confirmation." : "Disabled account saved. Runtime reloaded.";
      await saved();
      editing.value = false;
      secrets.value = {};
      draft.value = {};
      return true;
    } catch (error) {
      notice.value = error.message;
      return false;
    } finally {
      saving.value = false;
    }
  }
  async function retryActivation() {
    if (saving.value || !activationPending.value) return;
    saving.value = true;
    try {
      await hubApi("accounts/reload", {});
      activationPending.value = false;
      notice.value = "Runtime reloaded. Check Connections for webhook and Core delivery confirmation.";
      await saved();
    } catch (error) { notice.value = `Activation failed: ${error.message}`; }
    finally { saving.value = false; }
  }
  return {
    retryActivation, activationPending,
    editing,
    editingID,
    draft,
    secrets,
    errors,
    notice,
    saving,
    edit,
    close,
    save,
  };
}
