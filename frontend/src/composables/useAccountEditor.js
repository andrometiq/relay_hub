import { ref, toRaw } from "vue";
import { validateFields } from "../components/formValidation.js";
import { hubApi } from "../services/hubApi.js";
import {
  CHANNELS, PRIVATE_KEYS, validateChannelDraft,
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
      preservedChannels: structuredClone(toRaw(channels.filter(channel => !CHANNELS.includes(channel.channel)))),
      channels: CHANNELS,
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
    for (const name of CHANNELS.filter(name => name !== WHATSAPP)) {
      const channel = channels.find(item => item.channel === name);
      draft.value.channelConfig[name] = { ...(name === "Email" ? { port: 587, imap_port: 993, imap_folder: "INBOX" } : {}), ...structuredClone(toRaw(channel?.config || {})) };
      draft.value.channelEnabled[name] = channel?.enabled ?? false;
      draft.value.configured[name] = [...(channel?.configured_secrets || [])];
    }
    // Existing non-WhatsApp accounts do not acquire an enabled WhatsApp channel.
    if (account && !whatsapp) draft.value.channelEnabled.WhatsApp = false;
    secrets.value = Object.fromEntries(CHANNELS.map(name => [name, ""]));
    errors.value = {};
    notice.value = "";
    editing.value = true;
  }

  function close() {
    if (saving.value) return;
    editing.value = false;
    errors.value = {};
    secrets.value = {};
    draft.value = {};
  }

  async function save() {
    if (saving.value) return false;
    errors.value = {
      ...validateFields(accountFields, draft.value),
      ...Object.fromEntries(CHANNELS.flatMap(channel => Object.entries(validateChannelDraft(draft.value, channel, secrets.value[channel])).map(([key, value]) => [channel === WHATSAPP ? key : `${channel}.${key}`, value]))),
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
        secrets: { ...secrets.value },
        create: editingID.value === null,
      });
      editingID.value = account.id;
      activationPending.value = true;
      // Saved credentials are write-only. Keep their presence, clear replacement values.
      for (const channel of CHANNELS) {
        const config = draft.value.channelConfig[channel];
        draft.value.configured[channel] = [...new Set([...draft.value.configured[channel], ...PRIVATE_KEYS.filter(key => config[key]), ...(secrets.value[channel] ? ["secret"] : [])])];
        PRIVATE_KEYS.forEach(key => delete config[key]);
        secrets.value[channel] = "";
      }
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
