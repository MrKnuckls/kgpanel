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
// whoami: {username, admin, subuser, servers:[{id,permission}...]}; myPerm is
// the caller's permission on the currently open server ("admin" for owners).
let me = null;
let myPerm = "admin";
const $ = (id) => document.getElementById(id);

const PERM_RANK = { view: 1, control: 2, admin: 3 };
function hasPerm(min) { return (PERM_RANK[myPerm] || 0) >= (PERM_RANK[min] || 0); }

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
function showDash() { $("viewDash").style.display = "block"; $("viewDetail").style.display = "none"; currentServer = null; stopMetrics(); if (ws) { try { ws.close(); } catch (e) {} ws = null; } refresh(); }
function showDetail(id) {
  currentServer = id; $("viewDash").style.display = "none"; $("viewDetail").style.display = "block";
  document.querySelectorAll(".side-item").forEach((x) => x.classList.toggle("active", x.dataset.id === id));
  // resolve this caller's permission on this server and gate the UI
  myPerm = "admin";
  if (me && me.subuser) {
    const hit = (me.servers || []).find((s) => s.id === id);
    myPerm = (hit && hit.permission) || "none";
  }
  applyPermUI();
  $("dName").textContent = id; refreshStatus(); connectConsole(); loadFiles(); loadSchedules(); loadBackups(); loadDatabases();
  loadNetwork(); loadStartup(); loadUsers(); loadActivity();
  startMetrics();
}

// show/hide power buttons + tabs according to myPerm (view < control < admin)
function applyPermUI() {
  const canControl = hasPerm("control");
  const canAdmin = hasPerm("admin");
  document.querySelectorAll("[data-power]").forEach((b) => (b.style.display = canControl ? "" : "none"));
  const showTab = (name, on) => {
    const b = document.querySelector('.dtab[data-tab="' + name + '"]');
    if (b) b.style.display = on ? "" : "none";
  };
  showTab("users", canAdmin);
  showTab("settings", canControl);
  showTab("databases", canControl);
  showTab("schedules", canControl);
  showTab("backups", canControl);
  // settings-tab cards + header action buttons carry data-perm
  // (details=admin, sftp/danger/repair/reinstall=control)
  document.querySelectorAll("#dtab-settings .card[data-perm], .detail-head [data-perm]").forEach((c) => {
    c.style.display = hasPerm(c.dataset.perm) ? "" : "none";
  });
  // hide the console command bar for view-only users
  const ti = document.querySelector(".term-input");
  if (ti) ti.style.display = canControl ? "" : "none";
}
$("backBtn").addEventListener("click", showDash);

/* ---------- metrics (live resource sparklines) ---------- */
let metricsTimer = null;
function startMetrics() {
  if (metricsTimer) clearInterval(metricsTimer);
  loadMetrics();
  metricsTimer = setInterval(loadMetrics, 5000);
}
function stopMetrics() { if (metricsTimer) { clearInterval(metricsTimer); metricsTimer = null; } }
function sparkLine(svg, values, max) {
  const w = 120, h = 36, pad = 2;
  if (!values || values.length < 2) { svg.innerHTML = ""; return; }
  max = max || Math.max(...values, 1);
  const pts = values.map((v, i) => {
    const x = pad + (i * (w - 2 * pad)) / (values.length - 1);
    const y = h - pad - (v / max) * (h - 2 * pad);
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  }).join(" ");
  svg.innerHTML = `<polyline points="${pts}"/>`;
}
function fmtNet(bps) {
  if (!bps || isNaN(bps) || bps < 0) return "—";
  if (bps >= 1048576) return (bps / 1048576).toFixed(1) + " MB/s";
  if (bps >= 1024) return (bps / 1024).toFixed(1) + " KB/s";
  return Math.round(bps) + " B/s";
}
function fmtBytes(b) {
  if (!b) return "—";
  if (b >= 1099511627776) return (b / 1099511627776).toFixed(1) + " TB";
  if (b >= 1073741824) return (b / 1073741824).toFixed(1) + " GB";
  if (b >= 1048576) return (b / 1048576).toFixed(1) + " MB";
  return (b / 1024).toFixed(0) + " KB";
}
async function loadMetrics() {
  if (!currentServer) return;
  try {
    const d = await jfetch("/servers/" + currentServer + "/metrics");
    const s = d.samples || [];
    const cpu = s.map((x) => x.cpu_percent || 0);
    const mem = s.map((x) => (x.mem_limit_mb ? (x.mem_used_mb / x.mem_limit_mb) * 100 : 0));
    const net = s.map((x) => (x.net_rx_bps || 0) + (x.net_tx_bps || 0));
    const disk = s.map((x) => x.disk_bytes || 0);
    sparkLine($("mCpuSvg"), cpu, 100);
    sparkLine($("mMemSvg"), mem, 100);
    sparkLine($("mNetSvg"), net);
    sparkLine($("mDiskSvg"), disk);
    const last = s[s.length - 1];
    if (last) {
      $("mCpuNow").textContent = last.cpu_percent.toFixed(1) + "%";
      $("mMemNow").textContent = (last.mem_limit_mb ? last.mem_used_mb.toFixed(0) + " / " + last.mem_limit_mb.toFixed(0) + " MB" : last.mem_used_mb.toFixed(0) + " MB");
      $("mNetNow").textContent = fmtNet(last.net_rx_bps) + " ↓ / " + fmtNet(last.net_tx_bps) + " ↑";
      $("mDiskNow").textContent = last.disk_total_bytes ? `${fmtBytes(last.disk_bytes)} / ${fmtBytes(last.disk_total_bytes)}` : fmtBytes(last.disk_bytes);
    }
  } catch (e) { /* wings may be starting; next tick retries */ }
}

/* ---------- topbar (whoami) ---------- */
async function refresh() {
  try {
    me = await jfetch("/me");
    $("whoami").textContent = me.username + (me.admin ? " · admin" : "");
    const verEl = document.querySelector(".side-title .ver");
    if (verEl && me.version) verEl.textContent = me.version;
    const isSub = !!me.subuser;
    // subusers don't manage nodes/eggs/updates or create servers
    $("adminBtn").style.display = isSub ? "none" : "";
    $("newServer").style.display = isSub ? "none" : "";
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
function setBadge(state) {
  const b = $("dBadge");
  b.className = "badge " + (["running", "stopped", "starting"].includes(state) ? state : "");
  $("dState").textContent = state;
}
async function refreshStatus() {
  if (!currentServer) return;
  try {
    const d = await jfetch("/servers/" + currentServer + "/status");
    setBadge(d.state);
    const dd = await jfetch("/servers");
    const sv = dd.servers.find((x) => x.id === currentServer);
    if (sv) { $("dName").textContent = sv.name; $("dMeta").textContent = `${sv.memory_mb}MB · ${sv.cpu_percent}%`; $("dDesc").textContent = sv.description || ""; }
    showConnect(sv);
  } catch (e) { $("dState").textContent = "unreachable"; }
}

/* connection address = node host + first published port */
function showConnect(sv) {
  const box = $("connectBox");
  const addr = $("connectAddr");
  let nodeHost = (sv.node_url || "").replace(/^https?:\/\//, "").replace(/\/.*$/, "");
  if (nodeHost.includes(":")) nodeHost = nodeHost.split(":")[0]; // strip wings port (8081)
  const ports = (sv.ports || []);
  // ports look like "8211/udp" (container port). Take the numeric part for the join address.
  const first = ports[0] ? (ports[0].match(/^(\d+)/) || [])[1] || "" : "";
  const proto = ports[0] && ports[0].includes("/udp") ? " (udp)" : "";
  if (!nodeHost || !first) { box.style.display = "none"; return; }
  addr.textContent = `${nodeHost}:${first}${proto ? " " + proto : ""}`;
  box.title = "join address for this server (host:port)";
  box.style.display = "flex";
}
$("copyAddr").addEventListener("click", () => {
  const a = $("connectAddr").textContent.trim();
  navigator.clipboard?.writeText(a).then(() => {
    const b = $("copyAddr"); const old = b.textContent; b.textContent = "copied!";
    setTimeout(() => (b.textContent = old), 1200);
  }).catch(() => {});
});
document.querySelectorAll("[data-power]").forEach((b) => b.addEventListener("click", async () => {
  const signal = b.dataset.power;
  try {
    setBadge("starting"); // immediate visual feedback while the action runs
    await jfetch("/servers/" + currentServer + "/power", { method: "POST", body: JSON.stringify({ signal }) });
    setTimeout(refreshStatus, 1500);
    // the power action tore down the old log stream — reconnect the console
    // after a beat so start/restart output actually shows up.
    setTimeout(connectConsole, 2500);
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
  if (b.dataset.tab === "settings") loadSettings();
  if (b.dataset.tab === "databases") loadDatabases();
  if (b.dataset.tab === "users") loadUsers();
  if (b.dataset.tab === "network") loadNetwork();
  if (b.dataset.tab === "startup") loadStartup();
  if (b.dataset.tab === "activity") loadActivity();
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
  ws.onclose = () => {
    if (!box.childElementCount) box.innerHTML = `<div class="term-line muted">console disconnected (server stopped?)</div>`;
    // a live server shouldn't drop its stream — if the container is still
    // running, reconnect so a brief wings/container blip doesn't leave a
    // dead console. If it's stopped, leave the disconnected message.
    let tries = 0;
    const retry = setInterval(async () => {
      if (++tries > 5 || !currentServer || (ws && ws.readyState <= 1)) { clearInterval(retry); return; }
      try {
        const d = await jfetch("/servers/" + currentServer + "/status");
        if (d.state === "running" || d.state === "starting") {
          connectConsole();
          clearInterval(retry);
        } else if (d.state === "stopped") {
          clearInterval(retry);
        }
      } catch (e) { clearInterval(retry); }
    }, 3000);
  };
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

/* ---------- databases ---------- */
async function loadDatabases() {
  const empty = $("dbEmpty");
  try {
    const [svd, eggd] = await Promise.all([jfetch("/servers"), jfetch("/eggs")]);
    const sv = svd.servers.find((x) => x.id === currentServer);
    const egg = sv && eggd.eggs.find((e) => e.id === sv.egg_id);
    empty.style.display = "block";
    if (egg && egg.db) {
      const net = egg.network || "kggames-net";
      empty.innerHTML = `<p style="font-size:13px">this egg uses a shared MariaDB on <b>${esc(net)}</b>.</p>
        <p class="muted" style="font-size:12px;margin-top:8px">a per-server database is created automatically on the first start. your game connects with the <code>KG_DB_*</code> env vars (host <code>db</code>, port 3306, per-server DB + user + password).</p>`;
    } else {
      empty.innerHTML = `<p style="font-size:13px">this egg needs no database.</p>
        <p class="muted" style="font-size:12px;margin-top:8px">toggle <b>needs a database</b> on its egg in <b>startup</b> or <b>settings</b> to get a shared MariaDB database on the node.</p>`;
    }
  } catch (e) { empty.style.display = "block"; empty.textContent = e.message; }
}

/* ---------- users (subusers) ---------- */
async function loadUsers() {
  const body = $("userTable").querySelector("tbody");
  try {
    const d = await jfetch("/servers/" + currentServer + "/users");
    const rows = (d.users || []);
    $("uPass").value = "";
    body.innerHTML = rows.length ? rows.map((u) => `
      <tr>
        <td>${esc(u.username)}</td>
        <td><span class="badge">${esc(u.permission)}</span></td>
        <td style="text-align:right"><button class="fdel" data-del="${esc(u.id)}">✕</button></td>
      </tr>`).join("") : `<tr><td class="muted" colspan="3">no subusers yet — add one above. the server owner always has full access.</td></tr>`;
    body.querySelectorAll("[data-del]").forEach((e) => e.addEventListener("click", async () => {
      if (!confirm("remove this user?")) return;
      try { await jfetch("/servers/" + currentServer + "/users/" + e.dataset.del + "/delete", { method: "POST" }); loadUsers(); } catch (err) { alert(err.message); }
    }));
  } catch (e) { body.innerHTML = `<tr><td class="muted">${esc(e.message)}</td></tr>`; }
}
$("uAdd").addEventListener("click", async () => {
  const name = $("uName").value.trim(), perm = $("uPerm").value;
  if (!name) { alert("enter a username"); return; }
  try { await jfetch("/servers/" + currentServer + "/users/create", { method: "POST", body: JSON.stringify({ username: name, permission: perm }) }); $("uName").value = ""; loadUsers(); } catch (e) { alert(e.message); }
});

/* ---------- network (allocations) ---------- */
async function loadNetwork() {
  try {
    const dd = await jfetch("/servers");
    const sv = dd.servers.find((x) => x.id === currentServer);
    if (!sv) return;
    const allocs = (sv.ports || []).map((p) => {
      const m = p.match(/^(?:([^:]+):)?(\d+)\/(udp|tcp)?$/);
      if (!m) return [p, "—"];
      return [`${m[1] || m[2]}:${m[2]}/${m[3] || "tcp"}`, `container port ${m[2]} (${m[3] || "tcp"})`];
    });
    $("netInfo").innerHTML = [
      ["node", sv.node_name], ["node url", sv.node_url],
      ...allocs.map(([a, note]) => [a, note]),
    ].map(([k, v]) => `<div class="info-item"><div class="k">${k}</div><div class="v">${esc(v || "—")}</div></div>`).join("");
  } catch (e) { $("netInfo").textContent = e.message; }
}

/* ---------- startup ---------- */
let startupEggId = null, startupEgg = null;
async function loadStartup() {
  try {
    const [svd, eggd] = await Promise.all([jfetch("/servers"), jfetch("/eggs")]);
    const sv = svd.servers.find((x) => x.id === currentServer);
    const egg = sv && eggd.eggs.find((e) => e.id === sv.egg_id);
    if (!egg) return;
    startupEggId = egg.id; startupEgg = egg;
    $("stImage").value = egg.image || "";
    $("stStartup").value = egg.startup || "";
    $("stStop").value = egg.stop_command || "";
    $("stVars").value = (egg.variables || []).join(",");
    $("stMsg").textContent = "";
  } catch (e) { $("stMsg").textContent = e.message; }
}
$("stSave").addEventListener("click", async () => {
  try {
    if (!startupEgg) return loadStartup();
    $("stMsg").textContent = "saving…";
    await jfetch("/eggs/update", {
      method: "POST",
      body: JSON.stringify({
        id: startupEggId,
        name: startupEgg.name || "",
        description: startupEgg.description || "",
        image: $("stImage").value.trim(),
        startup: $("stStartup").value.trim(),
        stop_command: $("stStop").value.trim(),
        variables: $("stVars").value.split(",").map((s) => s.trim()).filter(Boolean),
        ports: startupEgg.ports || [],
        network: startupEgg.network || "",
        db: !!startupEgg.db,
      }),
    });
    $("stMsg").textContent = "saved — applies on next start.";
  } catch (e) { $("stMsg").textContent = "failed: " + e.message; }
});

/* ---------- activity ---------- */
async function loadActivity() {
  const body = $("activityTable").querySelector("tbody");
  try {
    const d = await jfetch("/servers/" + currentServer + "/activity?limit=50");
    body.innerHTML = (d.activity || []).length ? d.activity.map((a) => `
      <tr>
        <td class="mono muted">${new Date(a.time).toLocaleString()}</td>
        <td>${esc(a.actor)}</td>
        <td>${esc(a.action)}</td>
        <td class="mono">${esc(a.detail || "")}</td>
      </tr>`).join("") : `<tr><td class="muted" colspan="4">no activity yet.</td></tr>`;
  } catch (e) { body.innerHTML = `<tr><td class="muted">${esc(e.message)}</td></tr>`; }
}

/* ---------- settings (server + egg) ---------- */
let currentEggId = null;
async function loadSettings() {
  try {
    const [svd, eggd] = await Promise.all([jfetch("/servers"), jfetch("/eggs")]);
    const sv = svd.servers.find((x) => x.id === currentServer);
    if (!sv) return;
    $("setMem").value = sv.memory_mb;
    $("setCpu").value = sv.cpu_percent;
    $("setName").value = sv.name || "";
    $("setDesc").value = sv.description || "";
    $("detailMsg").textContent = "";
    // display just the container port (8211:8211/udp -> 8211/udp) so the field
    // reads cleanly; save normalizes it back to the explicit map form.
    $("setPorts").value = (sv.ports || []).map((p) => p.replace(/^.*?:/, "")).join(",");
    const egg = eggd.eggs.find((e) => e.id === sv.egg_id);
    if (egg) {
      currentEggId = egg.id;
      $("setEggName").value = egg.name;
      $("setEggImage").value = egg.image;
      $("setEggStartup").value = egg.startup;
      $("setEggStop").value = egg.stop_command || "";
      $("setEggPorts").value = (egg.ports || []).map((p) => p.replace(/^.*?:/, "")).join(",");
      $("setEggVars").value = (egg.variables || []).join(",");
      $("setEggDb").checked = !!egg.db;
      $("setEggNet").value = egg.network || "";
    }
    $("setMsg").textContent = "";
    // show DB info line on the server detail too
    const dbLine = $("dbInfo");
    if (dbLine) {
      dbLine.style.display = egg && egg.db ? "block" : "none";
      dbLine.textContent = egg && egg.db
        ? "Database: mariadb on " + (egg.network || "kggames-net") + " — KG_DB_* env vars injected on start"
        : "";
    }
  } catch (e) { $("setMsg").textContent = e.message; }
}
$("setSave").addEventListener("click", async () => {
  try {
    $("setMsg").textContent = "saving & rebuilding…";
    await jfetch("/servers/" + currentServer + "/settings", {
      method: "POST",
      body: JSON.stringify({
        memory_mb: parseInt($("setMem").value, 10) || undefined,
        cpu_percent: parseInt($("setCpu").value, 10),
        ports: $("setPorts").value.split(",").map((s) => s.trim()).filter(Boolean),
      }),
    });
    $("setMsg").textContent = "saved — rebuilding container…";
    setTimeout(() => { $("setMsg").textContent = "done."; refreshStatus(); loadSettings(); }, 4000);
  } catch (e) { $("setMsg").textContent = "failed: " + e.message; }
});
$("setReload").addEventListener("click", loadSettings);
$("setEggSave").addEventListener("click", async () => {
  try {
    $("setEggMsg").textContent = "saving egg…";
    await jfetch("/eggs/update", {
      method: "POST",
      body: JSON.stringify({
        id: currentEggId,
        name: $("setEggName").value.trim(),
        image: $("setEggImage").value.trim(),
        startup: $("setEggStartup").value.trim(),
        stop_command: $("setEggStop").value.trim(),
        ports: $("setEggPorts").value.split(",").map((s) => s.trim()).filter(Boolean),
        variables: $("setEggVars").value.split(",").map((s) => s.trim()).filter(Boolean),
        db: $("setEggDb").checked,
        network: $("setEggNet").value.trim(),
      }),
    });
    $("setEggMsg").textContent = "egg saved.";
  } catch (e) { $("setEggMsg").textContent = "failed: " + e.message; }
});

/* ---------- settings: server details / sftp / danger zone ---------- */
async function loadSftp() {
  const msg = $("sftpMsg");
  try {
    msg.textContent = "loading…";
    const d = await jfetch("/servers/" + currentServer + "/sftp");
    $("sftpHost").value = d.host;
    $("sftpPort").value = d.port;
    $("sftpUser").value = d.username;
    $("sftpPass").value = d.password;
    msg.textContent = "copy the values into your SFTP client (FileZilla/WinSCP).";
  } catch (e) { msg.textContent = "failed: " + e.message; }
}
$("detailSave").addEventListener("click", async () => {
  const msg = $("detailMsg");
  const name = $("setName").value.trim(), desc = $("setDesc").value.trim();
  if (!name && !desc) { msg.textContent = "nothing to change."; return; }
  try {
    msg.textContent = "saving…";
    await jfetch("/servers/" + currentServer + "/details", {
      method: "POST",
      body: JSON.stringify({ name, description: desc }),
    });
    msg.textContent = "details saved.";
    refreshStatus();
  } catch (e) { msg.textContent = "failed: " + e.message; }
});
async function dangerAction(wipe) {
  if (!confirm(wipe
    ? "REINSTALL " + currentServer + "?\n\nThis DELETES the container AND wipes ALL files — your saves and config are gone. A fresh copy is installed from the egg. This cannot be undone."
    : "Repair " + currentServer + "?\n\nThe docker container is rebuilt from the egg. ALL files and saves are KEPT. Use this if the container is broken or won't start.")) return;
  const msg = $("dangerMsg");
  try {
    msg.textContent = wipe ? "reinstalling…" : "repairing…";
    await jfetch("/servers/" + currentServer + "/reinstall", {
      method: "POST",
      body: JSON.stringify({ wipe_files: wipe }),
    });
    msg.textContent = (wipe ? "reinstall started. files wiped — reinstall runs on next start." : "repair started. container rebuilt — start the server when ready.");
  } catch (e) { msg.textContent = "failed: " + e.message; }
}
$("repairBtn").addEventListener("click", () => dangerAction(false));
$("reinstallBtn").addEventListener("click", () => dangerAction(true));

/* ---------- header action buttons (SFTP / details / repair / reinstall) ---------- */
function openModal(id) { $(id).style.display = "flex"; }
function closeModal(id) { $(id).style.display = "none"; }
$("hdSftp").addEventListener("click", async () => {
  openModal("sftpModal");
  try {
    const d = await jfetch("/servers/" + currentServer + "/sftp");
    $("mSftpHost").value = d.host;
    $("mSftpPort").value = d.port;
    $("mSftpUser").value = d.username;
    $("mSftpPass").value = d.password;
  } catch (e) {
    $("mSftpPass").value = "failed: " + e.message;
  }
});
$("mSftpClose").addEventListener("click", () => closeModal("sftpModal"));
$("mSftpCopy").addEventListener("click", () => {
  const txt = ["host: " + $("mSftpHost").value, "port: " + $("mSftpPort").value,
    "username: " + $("mSftpUser").value, "password: " + $("mSftpPass").value].join("\n");
  navigator.clipboard.writeText(txt).then(() => {
    $("mSftpCopy").textContent = "copied ✓";
    setTimeout(() => ($("mSftpCopy").textContent = "copy all"), 1500);
  });
});
// clicking the dark backdrop closes the modal
["sftpModal", "detailsModal"].forEach((id) => {
  $(id).addEventListener("click", (e) => { if (e.target === $(id)) $(id).style.display = "none"; });
});
$("hdDetails").addEventListener("click", async () => {
  openModal("detailsModal");
  $("mDetailsMsg").textContent = "";
  try {
    const d = await jfetch("/servers");
    const sv = d.servers.find((x) => x.id === currentServer);
    // prefill rather than overwrite if user already typed something
    if (!$("mName").value) $("mName").value = sv ? (sv.name || "") : "";
    if (!$("mDesc").value) $("mDesc").value = sv ? (sv.description || "") : "";
  } catch (e) { $("mDetailsMsg").textContent = e.message; }
});
$("mDetailsCancel").addEventListener("click", () => closeModal("detailsModal"));
$("mDetailsSave").addEventListener("click", async () => {
  const msg = $("mDetailsMsg");
  const name = $("mName").value.trim(), desc = $("mDesc").value.trim();
  if (!name && !desc) { msg.textContent = "nothing to change."; return; }
  try {
    msg.textContent = "saving…";
    await jfetch("/servers/" + currentServer + "/details", {
      method: "POST",
      body: JSON.stringify({ name, description: desc }),
    });
    msg.textContent = "saved ✓";
    closeModal("detailsModal");
    refreshStatus();
  } catch (e) { msg.textContent = "failed: " + e.message; }
});
$("hdRepair").addEventListener("click", () => dangerAction(false));
$("hdReinstall").addEventListener("click", () => dangerAction(true));

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
  $("eggTable").querySelector("tbody").innerHTML = d.eggs.map((e) => `<tr><td>${esc(e.name)}</td><td class="mono">${esc(e.image)}</td><td>${e.db ? "🗄 DB" : ""}</td><td class="mono">${esc(e.startup)}</td></tr>`).join("");
}
$("eggForm").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  try {
    await jfetch("/eggs/create", { method: "POST", body: JSON.stringify({ name: $("eName").value.trim(), image: $("eImage").value.trim(), startup: $("eStartup").value.trim(), stop_command: $("eStop").value.trim(), ports: $("ePorts").value.split(",").map((s) => s.trim()).filter(Boolean), variables: $("eVars").value.split(",").map((s) => s.trim().replace(/[{}]/g, "")).filter(Boolean), db: $("eDb").checked, network: $("eNet").value.trim() }) });
    $("eName").value = $("eImage").value = $("eStartup").value = ""; loadEggs();
  } catch (e) { alert(e.message); }
});
async function loadAudit() {
  const d = await jfetch("/audit?limit=50");
  $("auditTable").querySelector("tbody").innerHTML = d.audit.map((a) => `<tr><td class="mono">${new Date(a.time).toLocaleString()}</td><td>${esc(a.actor)}</td><td>${esc(a.action)}</td><td class="mono">${esc(a.target)}</td></tr>`).join("");
}
$("adminBtn").addEventListener("click", () => { $("admin").style.display = "flex"; loadNodes(); loadEggs(); loadAudit(); loadUpdates(); });
let updPoll = null;
async function loadUpdates() {
  const el = $("updStatus"); el.textContent = "checking…";
  try {
    const d = await jfetch("/update/check");
    const cur = d.current.replace(/^v/, ""), lat = d.latest.replace(/^v/, "");
    el.textContent = d.update_needed ? `v${cur} → v${lat} — update available` : `up to date (v${cur})`;
  } catch (e) { el.textContent = "feed unreachable — " + e.message; }
  try {
    const st = await jfetch("/update/status");
    if (st.mode === "wings-docker") {
      if (st.running) {
        el.textContent = "updating via wings… building + swapping the panel container (this takes a minute or two)";
        pollUpdateStatus();
      } else if (st.last_ok) {
        el.textContent = "last update: ok" + (st.last_tag ? " (v" + st.last_tag.replace(/^v/, "") + ")" : "") + " — panel restarted on the new image";
      } else if (st.last_error) {
        el.textContent = "last update failed: " + st.last_error;
      }
    }
  } catch (e) { /* bare-metal: no wings status */ }
}
async function pollUpdateStatus() {
  clearInterval(updPoll);
  updPoll = setInterval(async () => {
    try {
      const st = await jfetch("/update/status");
      if (!st.running) {
        clearInterval(updPoll);
        const el = $("updStatus");
        el.textContent = st.last_ok ? "update finished ✓ — panel restarted on the new image" : "update failed: " + (st.last_error || "unknown error");
        loadUpdates();
      } else {
        $("updStatus").textContent = "updating via wings… " + (st.last_tag ? "v" + st.last_tag.replace(/^v/, "") : "") + " (this can take a couple minutes)";
      }
    } catch (e) { /* panel may be restarting — keep polling */ }
  }, 4000);
}
$("updCheck").addEventListener("click", loadUpdates);
$("updApply").addEventListener("click", async () => {
  if (!confirm("apply the update? wings will download the release, rebuild the panel image, and recreate the panel container — the panel restarts and you may briefly lose this page.")) return;
  const el = $("updStatus"); el.textContent = "starting update — waiting for wings…";
  try {
    const d = await jfetch("/update", { method: "POST" });
    el.textContent = d.mode === "wings-docker" ? "update started via wings ✓" : (d.message || "done");
    pollUpdateStatus();
  } catch (e) { el.textContent = "update failed — " + e.message; }
});
$("updRollback").addEventListener("click", async () => {
  if (!confirm("roll back to the previous panel image? the container is recreated from the -previous snapshot taken before the last update.")) return;
  const el = $("updStatus"); el.textContent = "rolling back — swapping container…";
  try {
    await jfetch("/rollback", { method: "POST" });
    el.textContent = "rollback started ✓ — panel will restart on the previous image";
  } catch (e) { el.textContent = "rollback failed — " + e.message; }
});
$("logout").addEventListener("click", () => { localStorage.removeItem("kg_token"); document.cookie = "kg_token=; path=/; Max-Age=0"; location.href = "/login.html"; });
["create", "admin"].forEach((id) => $(id).addEventListener("click", (ev) => { if (ev.target === $(id)) $(id).style.display = "none"; }));
if (!token) location.href = "/login.html";
refresh();
setInterval(refresh, 15000);
