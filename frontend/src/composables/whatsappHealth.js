// Labels and badge colours for the WhatsApp number and account values the Hub reads from Meta.
const QUALITY = { GREEN: ["High", "green"], YELLOW: ["Medium", "orange"], RED: ["Low", "red"] };
const THEMES = {
  status: { CONNECTED: "green", FLAGGED: "orange", RATE_LIMITED: "orange", UNVERIFIED: "orange", RESTRICTED: "red", DISCONNECTED: "red", BANNED: "red", DELETED: "red" },
  name_status: { APPROVED: "green", AVAILABLE_WITHOUT_REVIEW: "green", EXPIRED: "orange", DECLINED: "red" },
  account_review_status: { APPROVED: "green", REJECTED: "red" },
  business_verification_status: { verified: "green", not_verified: "orange", expired: "orange", failed: "red", rejected: "red" },
};
const THROUGHPUT = { STANDARD: "Standard (up to 80 messages a second)", HIGH: "High (up to 1,000 messages a second)", NOT_APPLICABLE: "Not applicable" };

function sentence(value) {
  const text = String(value).replaceAll("_", " ").toLowerCase();
  return text.charAt(0).toUpperCase() + text.slice(1);
}

export function qualityBadge(value) {
  const [label, theme] = QUALITY[String(value || "").toUpperCase()] || ["Unknown", "gray"];
  return { label, theme };
}

export function statusBadge(field, value) {
  if (!value || value === "NONE") return { label: "Unknown", theme: "gray" };
  return { label: sentence(value), theme: THEMES[field]?.[value] || "gray" };
}

export function messagingLimit(value) {
  if (value === "TIER_UNLIMITED") return "Unlimited";
  const tier = /^TIER_(\d+)(K?)$/.exec(value || "");
  if (!tier) return "Unknown";
  return (Number(tier[1]) * (tier[2] ? 1000 : 1)).toLocaleString("en-US") + " customers a day";
}

export function throughputLabel(value) {
  return THROUGHPUT[value] || "Unknown";
}

// The account health rows of a cached profile, in display order.
export function whatsappHealth(profile = {}) {
  const business = profile.business || {};
  return [
    { name: "Quality rating", ...qualityBadge(profile.quality_rating) },
    { name: "Number status", ...statusBadge("status", profile.status) },
    { name: "Display name", ...statusBadge("name_status", profile.name_status) },
    { name: "Messaging limit", text: messagingLimit(profile.whatsapp_business_manager_messaging_limit || business.whatsapp_business_manager_messaging_limit) },
    { name: "Throughput", text: throughputLabel(profile.throughput_level) },
    { name: "Account review", ...statusBadge("account_review_status", business.account_review_status) },
    { name: "Business verification", ...statusBadge("business_verification_status", business.business_verification_status) },
  ];
}
