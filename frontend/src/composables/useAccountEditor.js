import { ref, toRaw } from "vue";
import { validateFields } from "../components/formValidation";
import { hubApi } from "../services/hubApi";
import {
  activeChannels,
  whatsappReady,
  validateWhatsAppDraft,
} from "./accountForm";

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
  const editing = ref(false),
    editingID = ref(null),
    draft = ref({}),
    secrets = ref({});
  const errors = ref({}),
    notice = ref(""),
    saving = ref(false);

  function edit(account) {
    editingID.value = account?.id || null;
    const channels = account?.channels || [];
    const whatsapp = channels.find((channel) => channel.channel === WHATSAPP);
    draft.value = {
      id: account?.id || "",
      site: account?.site || "",
      enabled: account?.enabled ?? true,
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
      const config = {
        ...(draft.value.channelConfig[WHATSAPP] || {}),
        groups: false,
        calls: false,
      };
      const account = {
        id: draft.value.id.trim(),
        site: new URL(draft.value.site).origin,
        enabled: !!draft.value.enabled,
        channels: [
          {
            channel: WHATSAPP,
            enabled: !!draft.value.channelEnabled[WHATSAPP],
            config,
          },
        ],
      };
      await hubApi("accounts", {
        account,
        secrets: { [WHATSAPP]: secrets.value[WHATSAPP] },
        create: editingID.value === null,
      });
      try {
        await hubApi("accounts/reload", {});
      } catch {
        notice.value =
          "Saved, but WhatsApp could not start. Reopen the account and check the required fields.";
        return false;
      }
      notice.value = "Account saved and WhatsApp is active.";
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
  return {
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
