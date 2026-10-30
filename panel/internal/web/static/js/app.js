const API = "/api";
let token = localStorage.getItem("kg_token") || "";
let currentServer = null;
let ws = null;
const $ = (id) => document.getElementById(id);

function authHeaders(extra) {
  return Object.assign({ Authorization: "Bearer " + token }, extra || {});
}
async function jfetch(url, opts) {
  const r = await fetch(API + url, Object.assign({ headers: authHeaders({ "Content-Type": "application/json" }) }, opts || {}));
  if (r.status === 401) { localStorage.removeItem("kg_token"); location.href = "/login.html"; }
  if (!r.ok) { let m = "request failed"; try { m = (await r.json()).error || m; } catch (e) {} throw new Error(m); }
  return r.json();
}

function badge(state) {
  state = state || "unknown";
  const cls = ["running", "stopped", "starting"].includes(state) ? state : "";
  return `<span class="badge ${cls}"><span class="dot"></span>${state}</span>`;
}

async function refresh() {
  try {
    const me = await jfetch("/me");
    $("whoami").textContent = me.username + (me.admin ? " · admin" : "");
  } catch (e) {}
  try {
    const d = await jfetch("/servers");
    const grid = $("serverGrid");
    grid.innerHTML = d.servers.map((s) => `
      <div class="server-card" data-id="${s.id}">
        <div class="row"><span class="sname grow">${s.name}</span>${badge(s.state)}</div>
        <div class="smeta">${s.egg_name || "?"} · ${s.node_name || "?"} · ${s.memory_mb}MB</div>
        <div class="smeta mono">${(s.ports || []).join(", ")}</div>
      </div>`).join("");
    $("empty").style.display = d.servers.length ? "none" : "block";
    grid.querySelectorAll(".server-card").forEach((c) => c.addEventListener("click", () => openDetail(c.dataset.id)));
  } catch (e) { console.error(e); }
}

/* ---------- detail ---------- */
async function openDetail(id) {
  currentServer = id;
  $("detail").style.display = "flex";
  await refreshStatus();
  connectConsole();
}
async function refreshStatus() {
  try {
    const d = await jfetch("/servers/" + currentServer + "/status");
    $("dState").textContent = d.state;
    const dd = await jfetch("/servers");
    const sv = dd.servers.find((x) => x.id === currentServer);
    if (sv) {
      $("dName").textContent = sv.name;
      $("dName").classList.remove("mono");
      $("dMeta").textContent = `${sv.memory_mb}MB · ${sv.cpu_percent}% · ${(sv.ports || []).join(", ")}`;
    }
  } catch (e) { $("dState").textContent = "unreachable"; }
}
function connectConsole() {
  const box = $("console");
  if (ws) { try { ws.close(); } catch (e) {} ws = null; }
  box.textContent = "connecting…";
  ws = new WebSocket((location.protocol === "https:" ? "wss://" : "ws://") + location.host + API + "/console/" + currentServer + "?token=" + encodeURIComponent(token));
  ws.onopen = () => { box.textContent = ""; };
  ws.onmessage = (ev) => {
    const pre = ws ? "" : "";
    box.textContent += ev.data;
    box.scrollTop = box.scrollHeight;
    if (box.textContent.length > 200000) box.textContent = box.textContent.slice(-100000);
  };
  ws.onclose = () => { if (box.textContent === "connecting…" || box.textContent === "") { box.textContent = "console disconnected (server stopped?)"; } };
  ws.onerror = () => { box.textContent = "console error — check wings is up"; };
}
$("sendCmd").addEventListener("click", () => {
  const cmd = $("cmd").value;
  if (!cmd || !ws || ws.readyState !== 1) return;
  ws.send(cmd + "\n");
});
$("cmd").addEventListener("keydown", (e) => { if (e.key === "Enter") $("sendCmd").click(); });

document.querySelectorAll("[data-power]").forEach((b) => b.addEventListener("click", async () => {
  const signal = b.dataset.power;
  try {
    await jfetch("/servers/" + currentServer + "/power", {
      method: "POST",
      body: JSON.stringify({ signal }),
    });
    $("dState").textContent = "sent " + signal;
    setTimeout(refreshStatus, 1500);
  } catch (e) { alert(e.message); }
}));

/* ---------- create modal ---------- */
async function openCreate() {
  const [eggs, nodes] = await Promise.all([jfetch("/eggs"), jfetch("/nodes")]);
  $("cEgg").innerHTML = eggs.eggs.map((e) => `<option value="${e.id}">${e.name}</option>`).join("");
  $("cNode").innerHTML = nodes.nodes.map((n) => `<option value="${n.id}">${n.name}</option>`).join("");
  $("create").style.display = "flex";
}
$("newServer").addEventListener("click", openCreate);
$("cCancel").addEventListener("click", () => ($("create").style.display = "none"));
$("createForm").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  try {
    await jfetch("/servers/create", {
      method: "POST",
      body: JSON.stringify({
        name: $("cName").value.trim(),
        egg_id: $("cEgg").value,
        node_id: $("cNode").value,
        memory_mb: parseInt($("cMem").value, 10),
        cpu_percent: parseInt($("cCpu").value, 10) || 100,
        ports: $("cPorts").value.split(",").map((s) => s.trim()).filter(Boolean),
      }),
    });
    $("create").style.display = "none";
    refresh();
  } catch (e) { alert(e.message); }
});

/* ---------- admin ---------- */
$("adminBtn").addEventListener("click", () => { $("admin").style.display = "flex"; loadNodes(); loadEggs(); loadAudit(); });
document.querySelectorAll(".tabs li").forEach((t) => t.addEventListener("click", () => {
  document.querySelectorAll(".tabs li").forEach((x) => x.classList.remove("active"));
  t.classList.add("active");
  document.querySelectorAll(".tab").forEach((x) => (x.style.display = "none"));
  $("tab-" + t.dataset.tab).style.display = "block";
}));

async function loadNodes() {
  const d = await jfetch("/nodes");
  $("nodeTable").querySelector("tbody").innerHTML = d.nodes.map((n) => `
    <tr><td>${n.name}</td><td class="mono">${n.url}</td><td>${n.online ? "yes" : "no"}</td></tr>`).join("");
}
$("nodeForm").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  try {
    await jfetch("/nodes/create", {
      method: "POST",
      body: JSON.stringify({ name: $("nName").value.trim(), url: $("nUrl").value.trim(), token: $("nToken").value.trim() }),
    });
    $("nName").value = $("nUrl").value = $("nToken").value = "";
    loadNodes();
  } catch (e) { alert(e.message); }
});

async function loadEggs() {
  const d = await jfetch("/eggs");
  $("eggTable").querySelector("tbody").innerHTML = d.eggs.map((e) => `
    <tr><td>${e.name}</td><td class="mono">${e.image}</td><td class="mono">${e.startup}</td></tr>`).join("");
}
$("eggForm").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  try {
    await jfetch("/eggs/create", {
      method: "POST",
      body: JSON.stringify({
        name: $("eName").value.trim(),
        image: $("eImage").value.trim(),
        startup: $("eStartup").value.trim(),
        stop_command: $("eStop").value.trim(),
        ports: $("ePorts").value.split(",").map((s) => s.trim()).filter(Boolean),
        variables: $("eVars").value.split(",").map((s) => s.trim().replace(/[{}]/g, "")).filter(Boolean),
      }),
    });
    $("eName").value = $("eImage").value = $("eStartup").value = "";
    loadEggs();
  } catch (e) { alert(e.message); }
});

async function loadAudit() {
  const d = await jfetch("/audit?limit=50");
  $("auditTable").querySelector("tbody").innerHTML = d.audit.map((a) => `
    <tr><td class="mono">${new Date(a.time).toLocaleString()}</td><td>${a.actor}</td><td>${a.action}</td><td class="mono">${a.target}</td></tr>`).join("");
}

$("logout").addEventListener("click", () => { localStorage.removeItem("kg_token"); location.href = "/login.html"; });

// close modals on backdrop click
["detail", "create", "admin"].forEach((id) => {
  $(id).addEventListener("click", (ev) => { if (ev.target === $(id)) $(id).style.display = "none"; });
});

if (!token) location.href = "/login.html";
refresh();
setInterval(refresh, 15000);
