import { hubApi } from "../services/hubApi.js";

export const PROVIDERS = [
  { channel: "WhatsApp", icon: "message-circle", tone: "wa", noun: "number", plural: "numbers", add: "Add WhatsApp number" },
  { channel: "Telegram", icon: "send", tone: "tg", noun: "bot", plural: "bots", add: "Add Telegram bot" },
  { channel: "Email", icon: "mail", tone: "em", noun: "mailbox", plural: "mailboxes", add: "Add mailbox" },
];
export const provider = (channel) => PROVIDERS.find((row) => row.channel === channel) || PROVIDERS[0];
export const SITE_SECRETS = ["core_api_key", "core_api_secret", "callback_secret"];
const PAGE_LIMIT = 20;

export function hubURL(path, location = window.location) {
  const prefix = location.pathname === "/hub" || location.pathname.startsWith("/hub/") ? "/hub" : "";
  return location.origin + prefix + path;
}

export function siteHost(origin) {
  try { return new URL(origin).host; } catch { return origin || ""; }
}

export function countByProvider(channels = []) {
  const counts = Object.fromEntries(PROVIDERS.map((row) => [row.channel, 0]));
  for (const row of channels) counts[row.channel] = (counts[row.channel] || 0) + 1;
  return counts;
}

export function plural(count, noun, many = noun + "s") {
  return `${count} ${count === 1 ? noun : many}`;
}

export function channelStatus(channel) {
  const config = channel.config || {};
  if (!channel.enabled) return { label: "Disabled", theme: "gray" };
  if (!channel.site) return { label: "Idle", theme: "gray" };
  if (channel.channel === "Telegram") {
    return config.webhook_status === "registered" ? { label: "Registered", theme: "green" } : { label: "Not checked", theme: "gray" };
  }
  if (config.connection_status === "connected") return { label: "Connected", theme: "green" };
  if (config.connection_status === "failed") return { label: channel.channel === "Email" ? "Mail error" : "Failed", theme: "red" };
  return channel.channel === "WhatsApp" ? { label: "Setup needed", theme: "orange" } : { label: "Not checked", theme: "gray" };
}

export function channelAddress(channel) {
  const config = channel.config || {};
  if (channel.channel === "WhatsApp") return config.display_number || channel.address || "";
  if (channel.channel === "Telegram") return config.username ? "@" + config.username : channel.address ? "Bot ID " + channel.address : "";
  return config.from || channel.address || "";
}

// Connection rows: one per channel of every container, with its container and site names.
export function connectionRows(containers = [], sites = [], query = "") {
  const needle = query.trim().toLowerCase();
  return containers.flatMap((container) => (container.channels || []).map((channel) => ({
    ...channel, id: `${channel.channel}/${channel.key}`, containerName: container.name || container.id,
    siteName: channel.site ? siteLabel(channel.site, sites) : "", address: channelAddress(channel),
  }))).filter((row) => !needle || [row.key, row.label, row.address, row.containerName, row.siteName].join(" ").toLowerCase().includes(needle));
}

export function channelKey(label, taken = []) {
  const base = String(label || "").toLowerCase().normalize("NFKD").replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 90) || "account";
  let key = base;
  for (let suffix = 2; taken.includes(key); suffix++) key = `${base}-${suffix}`;
  return key;
}

export function siteLabel(id, sites = []) {
  const site = sites.find((row) => row.id === id);
  return site ? siteHost(site.origin) : id;
}

// One row per channel, grouped by container. A channel on another site cannot be picked.
export function pickerGroups(containers, siteID, query = "", sites = []) {
  const needle = query.trim().toLowerCase();
  return containers.map((container) => ({
    id: container.id,
    name: container.name || container.id,
    rows: (container.channels || [])
      .filter((row) => !needle || [row.label, row.key, channelAddress(row)].some((value) => String(value || "").toLowerCase().includes(needle)))
      .map((row) => ({
        ...row,
        state: !row.site ? "free" : row.site === siteID ? "here" : "elsewhere",
        siteName: row.site ? siteLabel(row.site, sites) : "",
      })),
  })).filter((group) => group.rows.length);
}

export function assignedRefs(channels, add = [], remove = null) {
  const refs = channels.filter((row) => !remove || row.channel !== remove.channel || row.key !== remove.key).map(({ channel, key }) => ({ channel, key }));
  for (const row of add) if (!refs.some((ref) => ref.channel === row.channel && ref.key === row.key)) refs.push({ channel: row.channel, key: row.key });
  return refs;
}

export function newSecret(bytes = 32) {
  const values = crypto.getRandomValues(new Uint8Array(bytes));
  return btoa(String.fromCharCode(...values)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function containerReady(container) {
  const secrets = container.whatsapp?.configured_secrets || [];
  return ["app_secret", "verify_token"].every((name) => secrets.includes(name));
}

export function siteReady(site) {
  return SITE_SECRETS.every((name) => (site.configured_secrets || []).includes(name));
}

// Pickers on the Deliveries and Events pages choose one channel of one site.
export function siteChannelOptions(sites = [], channel = "WhatsApp") {
  return sites.flatMap((site) => (site.channels || []).filter((row) => row.channel === channel)
    .map((row) => ({ key: row.key, site: site.id, label: `${site.name || siteHost(site.origin) || site.id} · ${row.label || row.key}` })));
}

export function siteStatus(site) {
  if (!site.enabled) return { label: "Disabled", theme: "gray" };
  return siteReady(site) ? { label: "Enabled", theme: "green" } : { label: "Setup needed", theme: "orange" };
}

export function tokenState(token, now = Date.now()) {
  if (token.Revoked) return "Revoked";
  return token.Expires !== "Never" && Date.parse(token.Expires) <= now ? "Expired" : "Active";
}

// Auth keys of the signed-in user for one site. The auth page filters by name or site, so rows are checked again here.
export async function siteTokens(site) {
  const response = await fetch(hubURL("/auth/tokens?" + new URLSearchParams({ query: site })), { headers: { Accept: "application/json" }, credentials: "same-origin" });
  if (!response.ok) throw Error(`Auth keys could not be loaded (${response.status}).`);
  const data = await response.json();
  return { tokens: (data.Tokens || []).filter((row) => row.Account === site), more: !!data.More };
}

async function loadPages(kind, query) {
  const rows = [];
  let after = "";
  for (let page = 0; page < PAGE_LIMIT; page++) {
    const data = await hubApi(`${kind}?` + new URLSearchParams({ query, after }));
    rows.push(...(data[kind] || []));
    if (!data.next) break;
    after = data.next;
  }
  return rows;
}

const item = (kind, id) => `${kind}/${encodeURIComponent(id)}`;

export const adminApi = {
  containers: (query = "") => loadPages("containers", query),
  container: (id) => hubApi(item("containers", id)),
  createContainer: (container) => hubApi("containers", container),
  updateContainer: (id, changes) => hubApi(item("containers", id), changes, "PUT"),
  containerWebhook: (id) => hubApi(item("containers", id) + "/whatsapp/webhook"),
  createChannel: (container, channel) => hubApi(item("containers", container) + "/channels", channel),
  updateChannel: (container, channel) => hubApi(`${item("containers", container)}/channels/${encodeURIComponent(channel.channel)}/${encodeURIComponent(channel.key)}`, channel, "PUT"),
  turnOffOldAddress: (container, channel) => hubApi(item("containers", container) + (channel ? `/channels/${encodeURIComponent(channel.channel)}/${encodeURIComponent(channel.key)}` : "") + "/old-address", {}, "DELETE"),
  checkChannel: (channel, action) => hubApi(`channels/${channel.channel.toLowerCase()}/${encodeURIComponent(channel.key)}/connection`, { action }),
  sites: (query = "") => loadPages("sites", query),
  site: (id) => hubApi(item("sites", id)),
  createSite: (site) => hubApi("sites", site),
  updateSite: (id, changes) => hubApi(item("sites", id), changes, "PUT"),
  assignChannels: (site, channels) => hubApi(item("sites", site) + "/channels", { channels }, "PUT"),
  forwards: (provider, key, before = "") => hubApi(forwardPath(provider, key) + (before ? "?before=" + encodeURIComponent(before) : "")),
  addForward: (provider, key, url) => hubApi(forwardPath(provider, key), { url }),
  removeForward: (provider, key, id) => hubApi(`${forwardPath(provider, key)}/${encodeURIComponent(id)}`, {}, "DELETE"),
};

function forwardPath(provider, key) {
  return `forwards/${provider.toLowerCase()}/${encodeURIComponent(key)}`;
}

// forwardState labels one forward result for the results table.
export function forwardState(record) {
  if (record.state === "delivered") return { label: "Delivered", theme: "green" };
  if (record.state === "failed") return { label: "Failed", theme: "red" };
  return { label: record.attempts ? "Retrying" : "Waiting", theme: "orange" };
}

// Moves one channel between sites. It leaves the old site first, so the Hub never sees two owners.
export async function moveChannel(ref, from, to) {
  if (from === to) return;
  if (from) {
    const site = await adminApi.site(from);
    await adminApi.assignChannels(from, assignedRefs(site.channels || [], [], ref));
  }
  if (to) {
    const site = await adminApi.site(to);
    await adminApi.assignChannels(to, assignedRefs(site.channels || [], [ref]));
  }
}
