// Channels this admin UI offers. Saving an account keeps other channels unchanged.
export const CHANNELS = ["WhatsApp"];
export const EXTRA_CHANNELS = CHANNELS.filter((channel) => channel !== "WhatsApp");
export const PRIVATE_KEYS = ["app_secret", "verify_token", "core_token", "core_api_key", "core_api_secret", "callback_secret", "imap_password"];
const WHATSAPP = "WhatsApp";

export function activeChannels(account) {
  return (account?.channels || []).filter((channel) => channel.enabled);
}

export function whatsappReady(account) {
  const channel = activeChannels(account).find(
    (candidate) => candidate.channel === WHATSAPP,
  );
  if (!channel) return false;
  const secrets = channel.configured_secrets || [];
  const config = channel.config || {};
  return (
    config.connection_status === "connected" &&
    ["secret", "app_secret", "verify_token"].every((name) =>
      secrets.includes(name),
    ) &&
    /^\d{1,30}$/.test(String(config.phone_id || "")) &&
    /^\d{1,30}$/.test(String(config.waba_id || "")) &&
    /^v\d+\.0$/.test(String(config.graph_version || ""))
  );
}

function hasSecret(draft, secrets, name) {
  return (
    !!String(secrets?.[name] || "").trim() ||
    (draft.configured?.[WHATSAPP] || []).includes(name)
  );
}

export function validateWhatsAppDraft(draft, secrets = {}) {
  const errors = {};
  if (!draft.enabled || !draft.channelEnabled?.[WHATSAPP]) return errors;
  const config = draft.channelConfig?.[WHATSAPP] || {};
  if (!hasSecret(draft, secrets, "secret"))
    errors.secret = "Enter the permanent Meta access token.";
  if (!/^\d{1,30}$/.test(String(config.phone_id || "")))
    errors.phone_id = "Enter the numeric phone number ID from Meta.";
  if (!/^\d{1,30}$/.test(String(config.waba_id || "")))
    errors.waba_id = "Enter the numeric WhatsApp Business Account ID.";
  if (!/^v\d+\.0$/.test(String(config.graph_version || "")))
    errors.graph_version = "Use a Graph API version such as v25.0.";
  if (!hasSecret(draft, secrets, "app_secret"))
    errors.app_secret = "Enter the Meta app secret to verify webhook signatures.";
  if (!hasSecret(draft, secrets, "verify_token"))
    errors.verify_token = "Enter the same verify token used in Meta Webhooks.";
  if (config.templates && !/^\d{1,30}$/.test(String(config.app_id || "")))
    errors.app_id = "Enter the numeric Meta app ID to manage templates.";
  else if (config.app_id && !/^\d{1,30}$/.test(String(config.app_id)))
    errors.app_id = "Enter the numeric Meta app ID.";

  const callbackKeys = ["core_api_key", "core_api_secret", "callback_secret"];
  const callbackPresent = callbackKeys.map((name) =>
    hasSecret(draft, secrets, name),
  );
  if (callbackPresent.some(Boolean) && !callbackPresent.every(Boolean)) {
    callbackKeys.forEach((name, index) => {
      if (!callbackPresent[index])
        errors[name] = "Complete all three Core delivery credentials.";
    });
  }
  const callbackSecret = String(secrets.callback_secret || "");
  if (callbackSecret && callbackSecret.length < 32)
    errors.callback_secret =
      "Use at least 32 characters for the callback signing secret.";
  return errors;
}

export function accountSavePayload(draft) {
  return {
    id: draft.id.trim(),
    site: new URL(draft.site).origin,
    enabled: !!draft.enabled,
    channels: [...(draft.preservedChannels || []), ...(draft.channels || ["WhatsApp"]).map(channel => ({
      channel,
      enabled: !!draft.channelEnabled[channel],
      config: { ...(draft.channelConfig[channel] || {}) },
    }))],
  };
}

function mailHost(value) {
  const host = String(value || "");
  if (/^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$/i.test(host)) return true;
  // URL's IPv6 parser validates raw literals; brackets belong to transport only.
  if (!host.includes(":") || !/^[a-f0-9:.]+$/i.test(host)) return false;
  try { return new URL(`http://[${host}]/`).hostname.startsWith("["); }
  catch { return false; }
}

export function validateChannelDraft(draft, channel, secret = "") {
  const config = draft.channelConfig?.[channel] || {};
  if (channel === WHATSAPP) return validateWhatsAppDraft(draft, { ...config, secret });
  const errors = {};
  if (!draft.enabled || !draft.channelEnabled?.[channel]) return errors;
  const has = name => !!String(name === "secret" ? secret : config[name] || "").trim() || (draft.configured?.[channel] || []).includes(name);
  if (channel === "Telegram" && ((!has("secret")) || (secret && !/^[0-9]{6,12}:[A-Za-z0-9_-]{20,}$/.test(secret)))) errors.secret = "Enter the bot token provided by BotFather.";
  if (channel === "Email") {
    const host = mailHost;
    if (!host(config.host)) errors.host = "Enter an SMTP hostname without a URL or port.";
    const port = value => /^\d+$/.test(String(value)) && Number(value) >= 1 && Number(value) <= 65535;
    if (!port(config.port)) errors.port = "Use a port from 1 to 65535.";
    if (!/^(?:[^<>\r\n]+ <[^\s@<>]+@[^\s@<>]+>|[^\s@<>]+@[^\s@<>]+)$/.test(config.from || "")) errors.from = "Enter the sender email address.";
    if (config.username && !has("secret")) errors.secret = "Enter the SMTP password.";
    if (config.imap_host) {
      if (!host(config.imap_host)) errors.imap_host = "Enter an IMAP hostname without a URL or port.";
      if (!port(config.imap_port || 993)) errors.imap_port = "Use a port from 1 to 65535.";
      if (!config.imap_username && !config.username) errors.imap_username = "Enter an IMAP username or the shared SMTP username.";
      if (!has("imap_password") && !has("secret")) errors.imap_password = "Enter an IMAP password or the shared SMTP password.";
    }
    if (String(config.imap_folder || "").length > 200) errors.imap_folder = "Use a folder name of at most 200 characters.";
    for (const key of ["trusted_authserv_id", "trusted_dsn_domain"]) if (config[key] && !host(config[key])) errors[key] = "Enter the exact trusted hostname or domain.";
  }
  const callback = ["core_api_key", "core_api_secret", "callback_secret"];
  if (callback.some(has) && !callback.every(has)) for (const name of callback) if (!has(name)) errors[name] = "Complete all three Core delivery credentials.";
  if (config.callback_secret && config.callback_secret.length < 32) errors.callback_secret = "Use at least 32 characters for the callback signing secret.";
  return errors;
}
