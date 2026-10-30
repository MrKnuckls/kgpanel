const API = "/api";
let token = localStorage.getItem("kg_token") || "";
// fall back to the auth cookie if localStorage has none (covers sessions
// established purely via cookie, e.g. after check() sync on the login page).
if (!token) {
  const m = document.cookie.match(/(?:^|; )kg_token=([^;]+)/);
  if (m) token = decodeURIComponent(m[1]);
}
let currentServer = null;
let ws = null;
let cron = null;
let currentPath = "/";
const $ = (id) => document.getElementById(id);

/* ---------- helpers ---------- */
function authHeaders(extra) {
  return Object.assign({ Authorization: "Bearer " + token }, extra || {});
}
async function jfetch(url, opts) {
  const r = await fetch(API + url, Object.assign({ headers: authHeaders({ "Content-Type": "application/json" }) }, opts || {}));
  if (r.status === 401) { localStorage.removeItem("kg_token"); document.cookie = "kg_token=; path=/; Max-Age=0"; location.href = "/login.html"; }
  if (!r.ok) { let m = "request failed"; try { m = (await r.json()).error || m; } catch (e) {} throw new Error(m); }
  return r.json();
}

function badgen(state) {
  state = state || "unknown";
  const cls = ["running", "stopped", "starting"].includes(state) ? state : "";
  return `<span class="badge ${cls}"><span class="dot"></span>${state}</span>`;
}
function fmtSize(b) {
  if (b == null) return "—";
  if (b < 1024) return b + " B";
  if (b < 1024 * 1024) return (b / 1024).toFixed(1) + " KB";
  return (b / 1024 / 1024).toFixed(1) + " MB";
}
function esc(s) { return String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c])); }

/* ---------- views ---------- */
function showDash() { $("viewDash").style.display = "block"; $("viewDetail").style.display = "none"; currentServer = null; if (ws) { try { ws.close(); } catch (e) {} ws = null; } refresh(); }
function showDetail(id) {
  currentServer = id; $("viewDash").style.display = "none"; $("viewDetail").style.display = "block";
  document.querySelectorAll(".side-item").forEach((x) => x.classList.toggle("active", x.dataset.id === id));
  $("dName").textContent = id; refreshStatus(); connectConsole(); loadFiles(); loadSchedules(); loadBackups(); loadInfo();
}
$("backBtn").addEventListener("click", showDash);

/* ---------- topbar (whoami) ---------- */
async function refresh() {
  try {
    const me = await jfetch("/me");
    $("whoami").textContent = me.username + (me.admin ? " · admin" : "");
  } catch (e) {}
  try {
    const d = await jfetch("/servers");
    // sidebar list
    $("serverList").innerHTML = d.servers.map((s) => `
      <button class="side-item ${s.state || ""}" data-id="${s.id}">
        <span class="dot"></span>${esc(s.name)}
      </button>`).join("") || `<div class="muted" style="font-size:12px;padding:8px 10px">no servers yet</div>`;
    $("serverList").querySelectorAll(".side-item").forEach((b) => b.addEventListener("click", () => showDetail(b.dataset.id)));
    // grid cards
    const grid = $("serverGrid");
    grid.innerHTML = d.servers.map((s) => `
      <div class="server-card ${s.state || ""}" data-id="${s.id}">
        <div class="row"><span class="sname grow">${esc(s.name)}</span>${badgen(s.state)}</div>
        <div class="smeta">${esc(s.egg_name || "?")} · ${esc(s.node_name || "?")} · ${s.memory_mb}MB</div>
        <div class="smeta mono">${(s.ports || []).map(esc).join(", ")}</div>
      </div>`).join("");
    $("empty").style.display = d.servers.length ? "none" : "block";
    grid.querySelectorAll(".server-card").forEach((c) => c.addEventListener("click", () => showDetail(c.dataset.id)));
  } catch (e) { console.error(e); }
}

/* ---------- power / detail ---------- */
async function refreshStatus() {
  if (!currentServer) return;
  try {
    const d = await jfetch("/servers/" + currentServer + "/status");
    $("dState").textContent = d.state;
    const dd = await jfetch("/servers");
    const sv = dd.servers.find((x) => x.id === currentServer);
    if (sv) { $("dName").textContent = sv.name; $("dMeta").textContent = `${sv.memory_mb}MB · ${sv.cpu_percent}%`; }
  } catch (e) { $("dState").textContent = "unreachable"; }
}
document.querySelectorAll("[data-power]").forEach((b) => b.addEventListener("click", async () => {
  const signal = b.dataset.power;
  try {
    await jfetch("/servers/" + currentServer + "/power", { method: "POST", body: JSON.stringify({ signal }) });
    $("dState").textContent = "sent " + signal;
    setTimeout(refreshStatus, 1500);
  } catch (e) { alert(e.message); }
}));
document.querySelectorAll(".dtab").forEach((b) => b.addEventListener("click", () => {
  document.querySelectorAll(".dtab").forEach((x) => x.classList.remove("active"));
  b.classList.add("active");
  document.querySelectorAll(".dtab-pane").forEach((x) => (x.style.display = "none"));
  $("dtab-" + b.dataset.tab).style.display = "block";
  if (b.dataset.tab === "files") loadFiles();
  if (b.dataset.tab === "schedules") loadSchedules();
  if (b.dataset.tab === "backups") loadBackups();
  if (b.dataset.tab === "info") loadInfo();
}));

/* ---------- console ---------- */
function connectConsole() {
  const box = $("console");
  box.innerHTML = `<div class="term-line muted">connecting…</div>`;
  if (ws) { try { ws.close(); } catch (e) {} ws = null; }
  ws = new WebSocket((location.protocol === "https:" ? "wss://" : "ws://") + location.host + API + "/console/" + currentServer + "?token=" + encodeURIComponent(token));
  ws.onopen = () => { box.innerHTML = ""; };
  ws.onmessage = (ev) => {
    const line = document.createElement("div");
    line.className = "term-line";
    line.textContent = ev.data;
    box.appendChild(line);
    box.scrollTop = box.scrollHeight;
    while (box.childElementCount > 3000) box.removeChild(box.firstChild);
  };
  ws.onclose = () => { if (!box.childElementCount) box.innerHTML = `<div class="term-line muted">console disconnected (server stopped?)</div>`; };
  ws.onerror = () => { box.innerHTML = `<div class="term-line err">console error — check wings is up</div>`; };
}
$("cmd").addEventListener("keydown", (e) => {
  if (e.key === "Enter" && e.target.value.trim() && ws && ws.readyState === 1) {
    ws.send(e.target.value + "\n"); e.target.value = "";
  }
});

/* ---------- files ---------- */
async function loadFiles() {
  try {
    const d = await jfetch("/servers/" + currentServer + "/files?path=" + encodeURIComponent(currentPath));
    $("filePath").textContent = d.path || "/";
    $("fileTable").querySelector("tbody").innerHTML = d.entries.map((f) => `
      <tr>
        <td><span class="fname ${f.dir ? "dir" : ""}" ${f.dir ? `data-dir="${esc(f.name)}"` : ""}>
          <span class="ico">${f.dir ? "📁" : "📄"}</span>${esc(f.name)}
        </span></td>
        <td class="mono muted">${f.dir ? "—" : fmtSize(f.size)}</td>
        <td class="mono muted">${f.modified ? new Date(f.modified).toLocaleString() : "—"}</td>
        <td style="text-align:right">
          ${f.dir ? "" : `<button class="fdl" data-dl="${esc(f.name)}">⬇</button>`}
          <button class="fdel" data-del="${esc(f.name)}">✕</button>
        </td>
      </tr>`).join("");
    $("fileTable").querySelectorAll("[data-dir]").forEach((e) => e.addEventListener("click", () => { currentPath = (currentPath === "/" ? "" : currentPath) + "/" + e.dataset.dir; loadFiles(); }));
    $("fileTable").querySelectorAll("[data-dl]").forEach((e) => e.addEventListener("click", () => { window.location = API + "/servers/" + currentServer + "/files/download?path=" + encodeURIComponent((currentPath === "/" ? "" : currentPath) + "/" + e.dataset.dl) + "&token=" + encodeURIComponent(token); }));
    $("fileTable").querySelectorAll("[data-del]").forEach((e) => e.addEventListener("click", async () => { if (!confirm("delete " + e.dataset.del + "?")) return; try { await jfetch("/servers/" + currentServer + "/files/delete", { method: "POST", body: JSON.stringify({ path: (currentPath === "/" ? "" : currentPath) + "/" + e.dataset.del }) }); loadFiles(); } catch (err) { alert(err.message); } }));
  } catch (e) { $("fileTable").querySelector("tbody").innerHTML = `<tr><td class="muted">${esc(e.message)}</td></tr>`; }
}
$("fUp").addEventListener("click", () => {
  const p = currentPath.split("/").slice(0, -1).join("/") || "/";
  currentPath = p; loadFiles();
});
$("fMkdir").addEventListener("click", async () => {
  const name = prompt("folder name"); if (!name) return;
  try { await jfetch("/servers/" + currentServer + "/files/mkdir", { method: "POST", body: JSON.stringify({ path: (currentPath === "/" ? "" : currentPath) + "/" + name }) }); loadFiles(); } catch (e) { alert(e.message); }
});
$("fUpload").addEventListener("click", () => $("fFile").click());
$("fFile").addEventListener("change", async (ev) => {
  const files = [...ev.target.files]; ev.target.value = "";
  for (const f of files) {
    const fd = new FormData();
    fd.append("file", f);
    const r = await fetch(API + "/servers/" + currentServer + "/files/upload?path=" + encodeURIComponent(currentPath), { method: "POST", headers: authHeaders({}), body: fd });
    if (!r.ok) { const j = await r.json().catch(() => ({})); alert("upload failed: " + (j.error || r.status)); return; }
  }
  loadFiles();
});

/* ---------- schedules ---------- */
async function loadSchedules() {
  try {
    const d = await jfetch("/servers/" + currentServer + "/schedules");
    $("scheduleTable").querySelector("tbody").innerHTML = d.schedules.map((s) => `
      <tr>
        <td>${esc(s.name)}</td>
        <td class="mono">${esc(s.cron)}</td>
        <td>${esc(s.action)}</td>
        <td class="mono muted">${s.last_run ? new Date(s.last_run).toLocaleString() : "never"}</td>
        <td style="text-align:right"><button class="fdel" data-del="${s.id}">✕</button></td>
      </tr>`).join("");
    $("scheduleTable").querySelectorAll("[data-del]").forEach((e) => e.addEventListener("click", async () => { try { await jfetch("/servers/" + currentServer + "/schedules/" + e.dataset.del + "/delete", { method: "POST" }); loadSchedules(); } catch (err) { alert(err.message); } }));
  } catch (e) { $("scheduleTable").querySelector("tbody").innerHTML = `<tr><td class="muted">${esc(e.message)}</td></tr>`; }
}
$("sAdd").addEventListener("click", async () => {
  try {
    await jfetch("/servers/" + currentServer + "/schedules/create", {
      method: "POST",
      body: JSON.stringify({ name: $("sName").value.trim(), cron: $("sCron").value.trim(), action: $("sAction").value }),
    });
    $("sName").value = ""; $("sCron").value = ""; loadSchedules();
  } catch (e) { alert(e.message); }
});

/* ---------- backups ---------- */
async function loadBackups() {
  try {
    const d = await jfetch("/servers/" + currentServer + "/backups");
    $("backupTable").querySelector("tbody").innerHTML = d.backups.map((b) => `
      <tr>
        <td class="mono">${esc(b.name)}</td>
        <td class="mono muted">${fmtSize(b.size)}</td>
        <td class="mono muted">${new Date(b.created).toLocaleString()}</td>
        <td style="text-align:right">
          <button class="fdl" data-dl="${esc(b.name)}">⬇</button>
          <button class="fdl" data-restore="${esc(b.name)}" title="restore">↺</button>
          <button class="fdel" data-del="${esc(b.name)}">✕</button>
        </td>
      </tr>`).join("");
    $("backupTable").querySelectorAll("[data-dl]").forEach((e) => e.addEventListener("click", () => { window.location = API + "/servers/" + currentServer + "/backups/download?name=" + encodeURIComponent(e.dataset.dl) + "&token=" + encodeURIComponent(token); }));
    $("backupTable").querySelectorAll("[data-restore]").forEach((e) => e.addEventListener("click", async () => { if (!confirm("restore " + e.dataset.restore + "? this overwrites current files.")) return; try { await jfetch("/servers/" + currentServer + "/backups/restore", { method: "POST", body: JSON.stringify({ name: e.dataset.restore }) }); alert("restore triggered"); } catch (err) { alert(err.message); } }));
    $("backupTable").querySelectorAll("[data-del]").forEach((e) => e.addEventListener("click", async () => { if (!confirm("delete " + e.dataset.del + "?")) return; try { await jfetch("/servers/" + currentServer + "/backups/delete", { method: "POST", body: JSON.stringify({ name: e.dataset.del }) }); loadBackups(); } catch (err) { alert(err.message); } }));
  } catch (e) { $("backupTable").querySelector("tbody").innerHTML = `<tr><td class="muted">${esc(e.message)}</td></tr>`; }
}
$("bCreate").addEventListener("click", async () => {
  try { await jfetch("/servers/" + currentServer + "/backups/create", { method: "POST" }); $("bHint").textContent = "backup created (may take a moment)"; loadBackups(); } catch (e) { $("bHint").textContent = "failed: " + e.message; }
});

/* ---------- info ---------- */
async function loadInfo() {
  try {
    const dd = await jfetch("/servers");
    const sv = dd.servers.find((x) => x.id === currentServer);
    if (!sv) return;
    $("dInfo").innerHTML = [
      ["id", sv.id], ["egg", sv.egg_name], ["node", sv.node_name],
      ["memory", sv.memory_mb + " MB"], ["cpu", sv.cpu_percent + "%"], ["ports", (sv.ports || []).join(", ")],
    ].map(([k, v]) => `<div class="info-item"><div class="k">${k}</div><div class="v">${esc(v || "—")}</div></div>`).join("");
  } catch (e) {}
}

/* ---------- create modal ---------- */
async function openCreate() {
  const [eggs, nodes] = await Promise.all([jfetch("/eggs"), jfetch("/nodes")]);
  $("cEgg").innerHTML = eggs.eggs.map((e) => `<option value="${e.id}">${esc(e.name)}</option>`).join("");
  $("cNode").innerHTML = nodes.nodes.map((n) => `<option value="${n.id}">${esc(n.name)}</option>`).join("");
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
        name: $("cName").value.trim(), egg_id: $("cEgg").value, node_id: $("cNode").value,
        memory_mb: parseInt($("cMem").value, 10), cpu_percent: parseInt($("cCpu").value, 10) || 100,
        ports: $("cPorts").value.split(",").map((s) => s.trim()).filter(Boolean),
      }),
    });
    $("create").style.display = "none"; refresh();
  } catch (e) { alert(e.message); }
});

/* ---------- admin ---------- */
document.querySelectorAll(".tabs li").forEach((t) => t.addEventListener("click", () => {
  document.querySelectorAll(".tabs li").forEach((x) => x.classList.remove("active"));
  t.classList.add("active");
  document.querySelectorAll(".tab").forEach((x) => (x.style.display = "none"));
  $("tab-" + t.dataset.tab).style.display = "block";
}));
async function loadNodes() {
  const d = await jfetch("/nodes");
  $("nodeTable").querySelector("tbody").innerHTML = d.nodes.map((n) => `<tr><td>${esc(n.name)}</td><td class="mono">${esc(n.url)}</td><td>${n.online ? "yes" : "no"}</td></tr>`).join("");
}
$("nodeForm").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  try {
    await jfetch("/nodes/create", { method: "POST", body: JSON.stringify({ name: $("nName").value.trim(), url: $("nUrl").value.trim(), token: $("nToken").value.trim() }) });
    $("nName").value = $("nUrl").value = $("nToken").value = ""; loadNodes();
  } catch (e) { alert(e.message); }
});
async function loadEggs() {
  const d = await jfetch("/eggs");
  $("eggTable").querySelector("tbody").innerHTML = d.eggs.map((e) => `<tr><td>${esc(e.name)}</td><td class="mono">${esc(e.image)}</td><td class="mono">${esc(e.startup)}</td></tr>`).join("");
}
$("eggForm").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  try {
    await jfetch("/eggs/create", { method: "POST", body: JSON.stringify({ name: $("eName").value.trim(), image: $("eImage").value.trim(), startup: $("eStartup").value.trim(), stop_command: $("eStop").value.trim(), ports: $("ePorts").value.split(",").map((s) => s.trim()).filter(Boolean), variables: $("eVars").value.split(",").map((s) => s.trim().replace(/[{}]/g, "")).filter(Boolean) }) });
    $("eName").value = $("eImage").value = $("eStartup").value = ""; loadEggs();
  } catch (e) { alert(e.message); }
});
async function loadAudit() {
  const d = await jfetch("/audit?limit=50");
  $("auditTable").querySelector("tbody").innerHTML = d.audit.map((a) => `<tr><td class="mono">${new Date(a.time).toLocaleString()}</td><td>${esc(a.actor)}</td><td>${esc(a.action)}</td><td class="mono">${esc(a.target)}</td></tr>`).join("");
}
$("adminBtn").addEventListener("click", () => { $("admin").style.display = "flex"; loadNodes(); loadEggs(); loadAudit(); loadUpdates(); });
async function loadUpdates() {
  const el = $("updStatus"); el.textContent = "checking…";
  try {
    const d = await jfetch("/update/check");
    const cur = d.current.replace(/^v/, ""), lat = d.latest.replace(/^v/, "");
    el.textContent = d.update_needed ? `v${cur} → v${lat} (${d.asset}) — update available` : `up to date (v${cur})`;
  } catch (e) { el.textContent = "feed unreachable — " + e.message; }
}
$("updCheck").addEventListener("click", loadUpdates);
$("updApply").addEventListener("click", async () => {
  if (!confirm("apply update? the panel will swap its binary and restart.")) return;
  const el = $("updStatus"); el.textContent = "downloading…";
  try { const d = await jfetch("/update", { method: "POST" }); el.textContent = d.message || "done"; }
  catch (e) { el.textContent = "update failed — " + e.message; }
});
$("logout").addEventListener("click", () => { localStorage.removeItem("kg_token"); document.cookie = "kg_token=; path=/; Max-Age=0"; location.href = "/login.html"; });
["create", "admin"].forEach((id) => $(id).addEventListener("click", (ev) => { if (ev.target === $(id)) $(id).style.display = "none"; }));
if (!token) location.href = "/login.html";
refresh();
setInterval(refresh, 15000);
