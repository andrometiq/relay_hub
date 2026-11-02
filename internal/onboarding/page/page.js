"use strict";
(function () {
  const sessionId = location.pathname.split("/").filter(Boolean).pop();
  const storageKey = "relay-launch:" + sessionId;
  const STEP_LABELS = {
    verify: "Checking access", channel: "Adding your number", subscribe: "Turning on messages",
    register: "Registering the number", sync_contacts: "Copying contacts", sync_history: "Copying chats",
  };
  const STATE_LABELS = { pending: "Waiting", running: "Working…", done: "Done", failed: "Failed", skipped: "Skipped" };
  const FLOW_LABELS = { new: "A new number", coexistence: "My WhatsApp Business app number", migrate: "A number on another provider" };
  const $ = (id) => document.getElementById(id);
  let secret = "";
  let config = null;
  let lastSignup = null;
  let codeSent = false;

  function readSecret() {
    const fragment = location.hash.replace(/^#/, "");
    try {
      if (fragment) sessionStorage.setItem(storageKey, fragment);
      secret = fragment || sessionStorage.getItem(storageKey) || "";
    } catch (error) {
      secret = fragment;
    }
    if (fragment) history.replaceState(null, "", location.pathname);
  }

  async function api(path, body) {
    const response = await fetch(location.pathname + path, {
      method: body ? "POST" : "GET", credentials: "omit", cache: "no-store",
      headers: Object.assign({ "X-Relay-Launch": secret }, body ? { "Content-Type": "application/json" } : {}),
      body: body ? JSON.stringify(body) : undefined,
    });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw Object.assign(new Error((data.error && data.error.message) || "Relay Hub is not available."), { status: response.status });
    return data;
  }

  function show(id, visible) { $(id).hidden = !visible; }

  function message(text, kind) {
    const box = $("message");
    box.textContent = text || "";
    box.className = "message " + (kind || "info");
    show("message", !!text);
  }

  function notifyOpener(session) {
    if (!window.opener || !config || !config.site_origin) return;
    try {
      window.opener.postMessage({ type: "relay-whatsapp-signup", session_id: sessionId, state: session.state }, config.site_origin);
    } catch (error) { /* the opener moved to another origin */ }
  }

  function renderSteps(session) {
    const list = $("steps");
    list.replaceChildren();
    for (const step of session.steps || []) {
      if (!STEP_LABELS[step.name]) continue;
      if (step.status === "skipped" && ["register", "sync_contacts", "sync_history"].includes(step.name)) continue;
      const item = document.createElement("li");
      item.className = step.status;
      const name = document.createElement("span");
      name.textContent = STEP_LABELS[step.name];
      const state = document.createElement("span");
      state.className = "state";
      state.textContent = STATE_LABELS[step.status] || step.status;
      item.append(name, state);
      list.append(item);
    }
    show("steps", list.children.length > 0);
  }

  function render(session) {
    lastSignup = session;
    const started = !["created", "waiting_for_meta"].includes(session.state);
    show("intro", !started);
    show("actions", started);
    for (const id of ["retry", "return", "close"]) show(id, false);
    if (["working", "completed"].includes(session.state) || (session.state === "failed" && codeSent)) renderSteps(session);
    else show("steps", false);
    const error = session.error;
    if (session.state === "completed") {
      message(error ? "Connected. " + error.message : "Done. You can close this window.", "ok");
      finish();
    } else if (session.state === "working") {
      message(error ? error.message : "Relay is setting up your number. You can close this window; Relay keeps going.", "info");
      notifyOpener(session);
    } else if (session.state === "cancelled") {
      message(error ? error.message : "You closed Facebook.", "info");
      show("retry", true); show("close", true);
    } else if (session.state === "failed") {
      message(error ? error.message : "The signup failed.", "bad");
      show("retry", !codeSent || !!(error && error.code === "code_invalid")); show("close", true);
      notifyOpener(session);
    } else if (["expired", "superseded"].includes(session.state)) {
      message("This signup window is no longer active. Start again in Relay.", "info");
      showReturn();
    } else {
      message("", "info");
    }
  }

  function showReturn() {
    if (!config || !config.site_origin) return;
    const link = $("return");
    link.href = config.site_origin + "/relay#/setup?session=" + encodeURIComponent(sessionId);
    show("actions", true);
    show("return", true);
  }

  function finish() {
    notifyOpener(lastSignup);
    if (window.opener) setTimeout(() => window.close(), 2000);
    else showReturn();
  }

  function loadSDK() {
    window.fbAsyncInit = function () {
      window.FB.init({ appId: config.app_id, autoLogAppEvents: true, xfbml: false, version: config.graph_version });
      $("start").disabled = false;
    };
    const script = document.createElement("script");
    script.src = config.sdk_url;
    script.async = true;
    script.crossOrigin = "anonymous";
    script.onerror = () => message("Facebook could not be loaded. Check your connection and try again.", "bad");
    document.head.append(script);
  }

  let finishInfo = null;
  let finishWaiter = null;
  window.addEventListener("message", (event) => {
    if (event.origin === location.origin) return;
    let data = event.data;
    if (typeof data === "string") {
      try { data = JSON.parse(data); } catch (error) { return; }
    }
    if (!data || data.type !== "WA_EMBEDDED_SIGNUP" || typeof data.event !== "string") return;
    if (data.event.startsWith("FINISH")) {
      finishInfo = { event: data.event, data: data.data || {} };
      if (finishWaiter) finishWaiter();
    }
    api("/event", { event: data.event, data: data.data || {}, origin: event.origin })
      .then((session) => { if (!codeSent) render(session); })
      .catch(() => {});
  });

  function waitForFinish(ms) {
    if (finishInfo) return Promise.resolve();
    return new Promise((resolve) => { finishWaiter = resolve; setTimeout(resolve, ms); });
  }

  async function sendCode(code) {
    codeSent = true;
    show("intro", false);
    message("Checking your number with Meta…", "info");
    await waitForFinish(1500);
    try {
      render(await api("/code", { code, session_info: finishInfo }));
    } catch (error) {
      message(error.message, "bad");
      show("actions", true); show("retry", true);
    }
  }

  function login() {
    if (!window.FB) return;
    codeSent = false;
    finishInfo = null;
    message("", "info");
    window.FB.login((response) => {
      const code = response && response.authResponse && response.authResponse.code;
      if (code) { sendCode(code); return; }
      refresh();
    }, { config_id: config.config_id, response_type: "code", override_default_response_type: true, extras: config.extras });
  }

  async function refresh() {
    try { render(await api("/state")); } catch (error) { message(error.message, "bad"); }
  }

  async function start() {
    readSecret();
    if (!secret) { message("Open this window from Relay to connect WhatsApp.", "bad"); return; }
    try {
      config = await api("/config");
    } catch (error) {
      message(error.status === 404 ? "This signup link is not valid. Start again in Relay." : error.message, "bad");
      return;
    }
    $("title").textContent = "Connect WhatsApp to " + (config.site_name || "Relay");
    $("flow").textContent = FLOW_LABELS[config.flow] || "";
    codeSent = !!(config.session && config.session.state !== "created" && config.session.state !== "waiting_for_meta" && config.session.state !== "cancelled");
    render(config.session);
    loadSDK();
  }

  $("start").addEventListener("click", login);
  $("retry").addEventListener("click", login);
  $("close").addEventListener("click", () => window.close());
  window.addEventListener("focus", () => { if (config) refresh(); });
  document.addEventListener("visibilitychange", () => { if (config && document.visibilityState === "visible") refresh(); });
  start();
})();
