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
    channels: [{
      channel: "WhatsApp",
      enabled: !!draft.channelEnabled.WhatsApp,
      config: { ...(draft.channelConfig.WhatsApp || {}) },
    }],
  };
}
