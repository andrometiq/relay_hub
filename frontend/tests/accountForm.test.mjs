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

test('Telegram and email validation is channel scoped and honors saved secrets', async () => {
  const {validateChannelDraft} = await import('../src/composables/accountForm.js');
  const draft = {enabled:true,channelEnabled:{Telegram:true,Email:true},configured:{Telegram:['secret'],Email:['secret','imap_password']},channelConfig:{Telegram:{},Email:{host:'smtp.example.invalid',port:587,from:'sender@example.invalid',username:'sender',imap_host:'imap.example.invalid'}}};
  assert.deepEqual(validateChannelDraft(draft,'Telegram'), {});
  assert.ok(validateChannelDraft(draft,'Telegram','broken').secret);
  assert.deepEqual(validateChannelDraft(draft,'Email'), {});
  Object.assign(draft.channelConfig.Email,{port:65536,imap_port:-1,from:'bad',trusted_authserv_id:'https://bad',core_api_key:'partial'});
  const errors=validateChannelDraft(draft,'Email');
  for (const key of ['port','imap_port','from','trusted_authserv_id','core_api_secret','callback_secret']) assert.ok(errors[key],key);
  draft.channelEnabled.Email=false;
  assert.deepEqual(validateChannelDraft(draft,'Email'), {});
});


test('email provider hosts accept raw IPv6 and reject brackets and malformed literals', async () => {
  const {validateChannelDraft} = await import('../src/composables/accountForm.js');
  for (const [host, valid] of [
    ['::1', true], ['2001:db8::25', true], ['::ffff:192.0.2.1', true],
    ['2001:db8:0:1:2:3:4:5', true], ['smtp.example.invalid', true], ['127.0.0.1', true],
    ['[::1]', false], ['[2001:db8::25]', false], ['2001:::1', false],
    ['1:2:3', false], ['fe80::1%eth0', false], ['::ffff:999.0.0.1', false],
    ['smtp.invalid:587', false], ['https://smtp.invalid', false],
  ]) {
    const draft = {enabled:true, channelEnabled:{Email:true}, configured:{Email:['secret']},
      channelConfig:{Email:{host, imap_host:host, port:587, username:'local', from:'sender@example.invalid'}}};
    const errors = validateChannelDraft(draft, 'Email');
    assert.equal(!errors.host, valid, host);
    assert.equal(!errors.imap_host, valid, host);
  }
});
