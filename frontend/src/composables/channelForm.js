import { validateChannelDraft } from "./accountForm.js";
import { channelKey } from "./containers.js";

const DEFAULTS = {
  WhatsApp: {},
  Telegram: {},
  Email: { port: 465, imap_port: 993, imap_folder: "INBOX" },
};
const OVERRIDE_KEYS = ["app_secret", "verify_token"];

// The form copies stored public config so saving keeps fields this dialog does not show.
export function channelForm(channelName, channel = null) {
  const configured = channel?.configured_secrets || [];
  return {
    channel: channelName,
    key: channel?.key || "",
    label: channel?.label || "",
    enabled: channel ? channel.enabled : true,
    site: channel?.site || "",
    secret: "",
    configured,
    override: OVERRIDE_KEYS.some((name) => configured.includes(name)),
    config: { ...DEFAULTS[channelName], ...(channel?.config || {}), app_secret: "", verify_token: "" },
  };
}

export function validateChannelForm(form, { containerToken = false } = {}) {
  const errors = {};
  if (!form.label.trim()) errors.label = "Enter an account name.";
  if (form.channel !== "WhatsApp") {
    const draft = { enabled: true, channelEnabled: { [form.channel]: true }, channelConfig: { [form.channel]: form.config }, configured: { [form.channel]: form.configured } };
    return { ...errors, ...validateChannelDraft(draft, form.channel, form.secret) };
  }
  const has = (name) => form.configured.includes(name) || !!String(name === "secret" ? form.secret : form.config[name] || "").trim();
  if (!/^\d{1,30}$/.test(String(form.config.phone_id || ""))) errors.phone_id = "Enter the numeric phone number ID from Meta.";
  if (!/^\d{1,30}$/.test(String(form.config.waba_id || ""))) errors.waba_id = "Enter the numeric WhatsApp Business Account ID.";
  if (!containerToken && !has("secret")) errors.secret = "Enter the access token, or add a system-user token to the container.";
  if (form.override) {
    for (const name of OVERRIDE_KEYS) if (!has(name)) errors[name] = "Enter a value, or use the container's Meta app.";
  }
  return errors;
}

export function channelPayload(form, takenKeys = []) {
  const config = { ...form.config };
  for (const name of OVERRIDE_KEYS) if (!form.override || !config[name]) delete config[name];
  if (form.channel === "Email") {
    config.from = String(config.from || "").trim().toLowerCase();
    for (const name of ["port", "imap_port"]) if (config[name] !== "" && config[name] != null) config[name] = Number(config[name]);
  }
  return {
    channel: form.channel,
    key: form.key || channelKey(form.label, takenKeys),
    label: form.label.trim(),
    enabled: form.enabled,
    config,
    secret: form.secret,
  };
}
