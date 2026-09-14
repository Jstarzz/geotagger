const $ = (id) => document.getElementById(id);

const state = {
  keys: [],
  token: "",
};

function showNotice(message, isError = false) {
  const node = $("notice");
  node.textContent = message;
  node.classList.toggle("error", isError);
  node.classList.remove("hidden");
}

function clearNotice() {
  const node = $("notice");
  node.textContent = "";
  node.classList.add("hidden");
  node.classList.remove("error");
}

async function request(path, options = {}) {
  const headers = new Headers(options.headers || {});
  headers.set("Accept", "application/json");
  if (options.method && options.method !== "GET") {
    headers.set("Content-Type", "application/json");
    headers.set("X-GeoTagger-Admin-CSRF", "1");
  }
  const response = await fetch(path, {
    credentials: "same-origin",
    cache: "no-store",
    ...options,
    headers,
  });
  const contentType = response.headers.get("content-type") || "";
  const payload = contentType.includes("application/json") ? await response.json() : null;
  if (!response.ok) {
    throw new Error(payload?.error || `Request failed with HTTP ${response.status}`);
  }
  return payload;
}

function formatDate(value) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat(undefined, {
    year: "numeric", month: "short", day: "2-digit", hour: "2-digit", minute: "2-digit"
  }).format(date);
}

function setHealth(id, ok, goodText, badText) {
  const node = $(id);
  node.textContent = ok ? goodText : badText;
  node.classList.toggle("status-ok", ok);
  node.classList.toggle("status-bad", !ok);
}

function renderKeys() {
  const body = $("key-rows");
  body.replaceChildren();
  if (!state.keys.length) {
    const row = document.createElement("tr");
    const cell = document.createElement("td");
    cell.colSpan = 7;
    cell.className = "empty";
    cell.textContent = "No managed API keys yet. Static break-glass keys are not shown.";
    row.appendChild(cell);
    body.appendChild(row);
    return;
  }

  for (const key of state.keys) {
    const row = document.createElement("tr");
    row.appendChild(textCell(key.id, "key-id"));
    row.appendChild(textCell(key.owner || key.display_name || "—"));
    row.appendChild(textCell(key.environment || "—"));

    const statusCell = document.createElement("td");
    const status = document.createElement("span");
    status.className = `state ${key.status}`;
    status.textContent = key.status;
    statusCell.appendChild(status);
    row.appendChild(statusCell);

    row.appendChild(textCell(formatDate(key.created_at)));
    row.appendChild(textCell(formatDate(key.expires_at)));

    const actionCell = document.createElement("td");
    actionCell.className = "actions-col";
    const actions = document.createElement("div");
    actions.className = "actions";

    const rotate = document.createElement("button");
    rotate.type = "button";
    rotate.className = "secondary";
    rotate.textContent = "Rotate";
    rotate.disabled = key.status === "revoked";
    rotate.addEventListener("click", () => rotateKey(key.id));

    const revoke = document.createElement("button");
    revoke.type = "button";
    revoke.className = "danger";
    revoke.textContent = "Revoke";
    revoke.disabled = key.status === "revoked";
    revoke.addEventListener("click", () => revokeKey(key.id));

    actions.append(rotate, revoke);
    actionCell.appendChild(actions);
    row.appendChild(actionCell);
    body.appendChild(row);
  }
}

function textCell(value, className = "") {
  const cell = document.createElement("td");
  if (className) cell.className = className;
  cell.textContent = value;
  return cell;
}

function showToken(token) {
  state.token = token;
  $("one-time-token").textContent = token;
  $("token-panel").classList.remove("hidden");
  $("token-panel").scrollIntoView({ behavior: "smooth", block: "center" });
}

function dismissToken() {
  state.token = "";
  $("one-time-token").textContent = "";
  $("token-panel").classList.add("hidden");
}

async function loadSession() {
  const session = await request("/admin/api/session");
  $("session-email").textContent = session.email;
}

async function loadStatus() {
  const status = await request("/admin/api/status");
  setHealth("service-ready", Boolean(status.ready), "Ready", "Degraded");
  setHealth("key-store-status", Boolean(status.managed_key_store), "Connected", "Unavailable");
  const counts = status.managed_keys || {};
  $("key-count").textContent = `${counts.active || 0} active · ${counts.revoked || 0} revoked · ${counts.expired || 0} expired`;
  $("mmdb-version").textContent = status.mmdb_version || "Unavailable";
  $("server-time").textContent = status.server_time ? `Server ${formatDate(status.server_time)}` : "";
}

async function loadKeys() {
  const payload = await request("/admin/api/keys");
  state.keys = Array.isArray(payload.keys) ? payload.keys : [];
  renderKeys();
}

async function refresh() {
  try {
    await Promise.all([loadSession(), loadStatus(), loadKeys()]);
  } catch (error) {
    showNotice(error.message, true);
  }
}

async function createKey(event) {
  event.preventDefault();
  clearNotice();
  dismissToken();
  const form = new FormData(event.currentTarget);
  const rawExpiry = String(form.get("expires_at") || "").trim();
  const payload = {
    id: String(form.get("id") || "").trim(),
    display_name: String(form.get("display_name") || "").trim(),
    owner: String(form.get("owner") || "").trim(),
    environment: String(form.get("environment") || "").trim(),
    expires_at: rawExpiry ? new Date(rawExpiry).toISOString() : null,
  };
  try {
    const result = await request("/admin/api/keys", { method: "POST", body: JSON.stringify(payload) });
    showToken(result.token);
    event.currentTarget.reset();
    $("create-panel").classList.add("hidden");
    showNotice(`Created ${result.key.id}. Copy the token before dismissing it.`);
    await Promise.all([loadKeys(), loadStatus()]);
  } catch (error) {
    showNotice(error.message, true);
  }
}

async function rotateKey(id) {
  clearNotice();
  dismissToken();
  if (!window.confirm(`Rotate ${id}? The previous token will stop authenticating after the managed-key update propagates.`)) return;
  try {
    const result = await request(`/admin/api/keys/${encodeURIComponent(id)}/rotate`, { method: "POST", body: "{}" });
    showToken(result.token);
    showNotice(`Rotated ${id}. Distribute the replacement token now.`);
    await Promise.all([loadKeys(), loadStatus()]);
  } catch (error) {
    showNotice(error.message, true);
  }
}

async function revokeKey(id) {
  clearNotice();
  dismissToken();
  const reason = window.prompt(`Revoke ${id}? Optional revocation reason:`, "") ?? null;
  if (reason === null) return;
  try {
    await request(`/admin/api/keys/${encodeURIComponent(id)}/revoke`, {
      method: "POST",
      body: JSON.stringify({ reason: reason.trim() }),
    });
    showNotice(`Revoked ${id}.`);
    await Promise.all([loadKeys(), loadStatus()]);
  } catch (error) {
    showNotice(error.message, true);
  }
}

$("show-create").addEventListener("click", () => {
  clearNotice();
  $("create-panel").classList.remove("hidden");
  $("create-panel").scrollIntoView({ behavior: "smooth", block: "start" });
});
$("cancel-create").addEventListener("click", () => $("create-panel").classList.add("hidden"));
$("create-form").addEventListener("submit", createKey);
$("dismiss-token").addEventListener("click", dismissToken);
$("copy-token").addEventListener("click", async () => {
  if (!state.token) return;
  try {
    await navigator.clipboard.writeText(state.token);
    $("copy-token").textContent = "Copied";
    window.setTimeout(() => { $("copy-token").textContent = "Copy"; }, 1400);
  } catch {
    showNotice("Clipboard access was blocked. Select and copy the token manually.", true);
  }
});

window.addEventListener("pagehide", dismissToken);
refresh();
