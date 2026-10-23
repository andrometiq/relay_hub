import test from "node:test";
import assert from "node:assert/strict";
import {
  accountSavePayload,
  activeChannels,
  whatsappReady,
  validateWhatsAppDraft,
} from "../src/composables/accountForm.js";

test("disabled retained credentials are not active channels", () => {
  const account = {
    channels: [
      { channel: "Email", enabled: false },
      { channel: "Telegram", enabled: false },
      { channel: "WhatsApp", enabled: true },
    ],
  };
  assert.deepEqual(
    activeChannels(account).map((channel) => channel.channel),
    ["WhatsApp"],
  );
});

test("WhatsApp readiness reflects required Meta fields and write-only secrets", () => {
  const channel = {
    channel: "WhatsApp",
    enabled: true,
    config: {
      phone_id: "123",
      waba_id: "456",
      graph_version: "v25.0",
      connection_status: "connected",
    },
    configured_secrets: ["secret", "app_secret", "verify_token"],
  };
  assert.equal(whatsappReady({ channels: [channel] }), true);
  assert.equal(
    whatsappReady({
      channels: [{ ...channel, config: { ...channel.config, connection_status: "pending" } }],
    }),
    false,
  );
  assert.equal(
    whatsappReady({
      channels: [{ ...channel, configured_secrets: ["secret", "verify_token"] }],
    }),
    false,
  );
});

test("enabled WhatsApp requires webhook-safe Meta configuration", () => {
  const draft = {
    enabled: true,
    channelEnabled: { WhatsApp: true },
    channelConfig: {
      WhatsApp: { graph_version: "25", phone_id: "phone", waba_id: "" },
    },
    configured: { WhatsApp: [] },
  };
  const errors = validateWhatsAppDraft(draft);
  for (const field of [
    "secret",
    "phone_id",
    "waba_id",
    "graph_version",
    "app_secret",
    "verify_token",
  ])
    assert.ok(errors[field], field);
});

test("saved secrets are accepted and partial Core delivery is rejected", () => {
  const draft = {
    enabled: true,
    channelEnabled: { WhatsApp: true },
    channelConfig: {
      WhatsApp: {
        graph_version: "v25.0",
        phone_id: "123",
        waba_id: "456",
        core_api_key: "new-key",
      },
    },
    configured: {
      WhatsApp: ["secret", "app_secret", "verify_token"],
    },
  };
  const errors = validateWhatsAppDraft(draft, {
    core_api_key: "new-key",
  });
  assert.equal(errors.secret, undefined);
  assert.ok(errors.core_api_secret);
  assert.ok(errors.callback_secret);
});

test("template management requires the numeric Meta app ID", () => {
  const draft = {
    enabled: true,
    channelEnabled: { WhatsApp: true },
    channelConfig: {
      WhatsApp: {
        graph_version: "v25.0",
        phone_id: "123",
        waba_id: "456",
        templates: true,
      },
    },
    configured: {
      WhatsApp: ["secret", "app_secret", "verify_token"],
    },
  };
  assert.ok(validateWhatsAppDraft(draft).app_id);
  draft.channelConfig.WhatsApp.app_id = "789";
  assert.equal(validateWhatsAppDraft(draft).app_id, undefined);
});

test("account save preserves hidden WhatsApp capabilities without submitting other channels", () => {
  const config = { phone_id: "123", groups: true, calls: true, templates: true };
  const draft = { id: " a ", site: "https://core.test/", enabled: true,
    channelEnabled: { WhatsApp: false }, channelConfig: { WhatsApp: config } };
  const payload = accountSavePayload(draft);
  assert.deepEqual(payload, { id: "a", site: "https://core.test", enabled: true,
    channels: [{ channel: "WhatsApp", enabled: false, config }] });
  assert.notEqual(payload.channels[0].config, config);
  assert.equal(config.groups, true);
});
