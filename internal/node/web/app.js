// sys1 dashboard. Plain JavaScript, no build step, no third-party code:
// everything the node serves is in this binary and covered by its CSP.
"use strict";

const $ = (sel) => document.querySelector(sel);

// The dashboard key arrives once as ?key= (from `sys1 open`), is kept in
// this page's own storage (which, unlike a cookie, no other port on this
// machine can read) and is sent as a header.
let KEY = (() => {
  const fromURL = new URLSearchParams(location.search).get("key");
  if (fromURL) history.replaceState(null, "", location.pathname);
  try {
    if (fromURL) localStorage.setItem("sys1.key", fromURL);
    return fromURL || localStorage.getItem("sys1.key") || "";
  } catch { return fromURL || ""; }
})();

// h builds DOM nodes. Text is always set as text, never parsed as HTML,
// so names and messages from other machines cannot inject markup.
function h(tag, props, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k === "style") Object.assign(el.style, v);
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const kid of kids.flat()) {
    if (kid == null || kid === false) continue;
    el.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
  }
  return el;
}

function fill(el, ...kids) {
  el.replaceChildren(...kids.flat().filter((k) => k != null && k !== false));
}

const OS_ICON = { darwin: "🍎", windows: "🪟", linux: "🐧", freebsd: "😈", android: "🤖", ios: "📱", openbsd: "🐡", netbsd: "🚩" };
const OS_NAME = { darwin: "macOS", windows: "Windows", linux: "Linux", freebsd: "FreeBSD", android: "Android", ios: "iOS", openbsd: "OpenBSD", netbsd: "NetBSD" };
const STATE = {
  queued: ["Queued", "warn"], assigned: ["Starting", "accent"], running: ["Running", "ok"], stopping: ["Stopping", "warn"],
  done: ["Finished", "ok"], stopped: ["Stopped", ""], failed: ["Failed", "bad"],
};

function bytes(b) {
  if (!b) return "0 B";
  const u = ["B", "KB", "MB", "GB", "TB", "PB"];
  const i = Math.min(u.length - 1, Math.floor(Math.log(b) / Math.log(1024)));
  return (b / 1024 ** i).toFixed(i > 2 ? 1 : 0) + " " + u[i];
}

function ago(iso) {
  const s = (Date.now() - new Date(iso)) / 1000;
  if (!isFinite(s) || s < 0) return "just now";
  if (s < 60) return Math.round(s) + "s ago";
  if (s < 3600) return Math.round(s / 60) + "m ago";
  if (s < 86400) return Math.round(s / 3600) + "h ago";
  return Math.round(s / 86400) + "d ago";
}

function toast(msg) {
  const t = $("#toast");
  t.textContent = msg;
  t.classList.add("show");
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => t.classList.remove("show"), 2600);
}

async function api(path, body) {
  const opts = body === undefined ? { headers: { Authorization: "Bearer " + KEY } } : {
    method: "POST", headers: { Authorization: "Bearer " + KEY, "Content-Type": "application/json", "X-Sys1": "1" }, body: JSON.stringify(body),
  };
  const r = await fetch(path, opts);
  if (r.status === 401) {
    locked(true);
    throw new Error("locked");
  }
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(data.error || r.statusText);
  return data;
}

function locked(on) {
  $("#locked").classList.toggle("hidden", !on);
  document.querySelectorAll(".tab").forEach((t) => t.classList.toggle("hidden", on || t.id !== "tab-" + current));
}

// ---- tabs ----
let current = "overview";
function show(tab) {
  current = tab;
  document.querySelectorAll("#tabs button").forEach((b) => b.classList.toggle("active", b.dataset.tab === tab));
  document.querySelectorAll(".tab").forEach((t) => t.classList.toggle("hidden", t.id !== "tab-" + tab));
  if (tab === "market") loadCatalog();
  if (tab === "storage") loadStorage();
  if (tab === "machines") loadCrashes();
  try { localStorage.setItem("sys1.tab", tab); } catch {}
}
document.querySelectorAll("#tabs button").forEach((b) => b.addEventListener("click", () => show(b.dataset.tab)));

// ---- rendering ----
let state = null;
let catalog = [];
let category = "All";

function stat(label, value, hint) {
  return h("div", { class: "stat" }, h("div", { class: "label" }, label), h("div", { class: "value" }, value), h("div", { class: "hint" }, hint));
}

function meter(label, used, total, text) {
  const pct = total > 0 ? Math.max(0, Math.min(100, (used / total) * 100)) : 0;
  return h("div", { class: "meter" },
    h("div", { class: "row" }, h("span", {}, label), h("span", {}, text)),
    h("div", { class: "bar" }, h("i", { style: { width: pct + "%" } })));
}

function renderOverview(s) {
  const t = s.totals;
  fill($("#stats"),
    stat("Machines online", `${t.online}/${t.machines}`, t.machines ? "connected to this node" : "add one to get started"),
    stat("Spare CPU", `${t.spareCores}`, `of ${t.cores} cores idle`),
    stat("Spare memory", bytes(t.memFree), `of ${bytes(t.memTotal)}`),
    stat("Spare storage", bytes(t.diskFree), `of ${bytes(t.diskTotal)}`),
    stat("Apps running", `${t.running}`, `${t.gpus} GPU${t.gpus === 1 ? "" : "s"} in the pool`));
  fill($("#capacity"),
    meter("CPU available", t.spareCores, t.cores, `${t.spareCores} / ${t.cores} cores`),
    meter("Memory available", t.memFree, t.memTotal, `${bytes(t.memFree)} / ${bytes(t.memTotal)}`),
    meter("Storage available", t.diskFree, t.diskTotal, `${bytes(t.diskFree)} / ${bytes(t.diskTotal)}`),
    h("p", { class: "muted small" }, "Available means unused right now. sys0 runs at the lowest priority, so the pool only ever uses what each machine's owner isn't using."));
  const recent = [...(s.jobs || []).map(jobRow), ...(s.tasks || []).map(taskRow)].slice(0, 6);
  fill($("#recent"), recent.length ? recent : h("div", { class: "empty" },
    h("div", { class: "big" }, "🌱"), h("p", {}, "Nothing deployed yet."),
    h("button", { class: "primary", onclick: () => show("market") }, "Browse the marketplace")));
}

function renderMachines(s) {
  const list = s.agents || [];
  if (!list.length) {
    fill($("#machines"), h("div", { class: "panel empty" }, h("div", { class: "big" }, "🖥️"),
      h("p", {}, "No machines yet."), h("button", { class: "primary", onclick: () => show("add") }, "Add a machine")));
    return;
  }
  fill($("#machines"), list.map((a) => {
    const r = a.resources || {};
    const cpuFree = (r.cpuIdlePercent || 0).toFixed(0);
    return h("div", { class: "card" + (a.online ? "" : " offline") },
      h("div", { class: "head" },
        h("div", { class: "os" }, OS_ICON[r.os] || "💻"),
        h("div", {},
          h("h3", {}, a.name),
          h("div", { class: "meta" }, `${OS_NAME[r.os] || r.os || "?"} · ${r.arch || "?"} · ${a.online ? "online" : "offline, last seen " + ago(a.lastSeen)}`))),
      h("div", { class: "chips" },
        a.local && h("span", { class: "chip accent" }, "This node"),
        h("span", { class: "chip " + (a.trust === "owner" ? "ok" : "") }, a.trust === "owner" ? "Your machine" : "Community machine"),
        (r.runtimes || []).map((x) => h("span", { class: "chip" }, x)),
        (r.gpus || []).map((g) => h("span", { class: "chip accent" }, "GPU: " + g)),
        r.onBattery && h("span", { class: "chip warn" }, "On battery, paused"),
        a.disagreed > 0 && h("span", { class: "chip " + (a.disagreed >= 3 ? "bad" : "warn"), title: "Times this machine's output didn't match other machines on verified work" },
          `Disagreed ${a.disagreed}×` + (a.disagreed >= 3 ? ", kept off verified work" : ""))),
      meter("CPU idle", r.cpuIdlePercent || 0, 100, `${cpuFree}% of ${r.cpuCores || 0} cores`),
      meter("Memory free", r.memFree, r.memTotal, `${bytes(r.memFree)} / ${bytes(r.memTotal)}`),
      meter("Disk free", r.diskFree, r.diskTotal, `${bytes(r.diskFree)} / ${bytes(r.diskTotal)}`),
      h("div", { class: "meta" }, `${a.tasks} app${a.tasks === 1 ? "" : "s"} · sys0 ${a.version || "?"} · joined ${ago(a.joined)}`),
      !a.local && h("div", { class: "actions" }, h("button", {
        class: "danger ghost", onclick: async () => {
          if (!confirm(`Remove ${a.name} from the pool? Its apps will move to other machines.`)) return;
          try { await api(`/api/agents/${a.id}/remove`, {}); toast("Machine removed"); refresh(); } catch (e) { toast(e.message); }
        },
      }, "Remove")));
  }));
}

function taskRow(t) {
  const [label, cls] = STATE[t.state] || [t.state, ""];
  const pkg = catalog.find((p) => p.name === t.package);
  const where = t.agent ? ` · on ${t.agent}` : "";
  const ports = (t.ports || []).map((p) => p.split(":")[0]);
  return h("div", { class: "item" },
    h("div", { class: "emoji" }, pkg?.icon || "📦"),
    h("div", { class: "grow" },
      h("div", { class: "title" }, t.title || t.package),
      h("div", { class: "msg" }, (t.message || "") + where + (t.attempt > 1 ? ` · attempt ${t.attempt}` : "")),
      ports.length > 0 && t.state === "running" && h("div", { class: "chips" }, ports.map((p) => h("span", { class: "chip" }, `port ${p} on ${t.agentHost || t.agent}`)))),
    t.hasCheckpoint && h("span", { class: "chip accent", title: "Fast-saved state is cached on the node" }, "saved"),
    t.pooledData && h("span", { class: "chip " + (t.dataSavedAt && t.dataSavedAt > "2000" ? "ok" : "warn"), title: "This app's data is copied to the pool, so it moves with the app" },
      t.dataSavedAt && t.dataSavedAt > "2000" ? "data saved " + ago(t.dataSavedAt) : "data not saved yet"),
    h("span", { class: "chip " + cls }, label),
    t.screen && h("a", { class: "button primary", href: "/screen.html?task=" + encodeURIComponent(t.id), target: "_blank", rel: "noopener" }, "Open screen"),
    t.output && h("button", { class: "ghost", onclick: () => showOutput(t) }, "Output"),
    h("button", {
      class: "ghost" + (["done", "stopped", "failed"].includes(t.state) ? "" : " danger"),
      onclick: async () => {
        try { await api(`/api/tasks/${t.id}/stop`, {}); refresh(); } catch (e) { toast(e.message); }
      },
    }, ["done", "stopped", "failed"].includes(t.state) ? "Clear" : "Stop"));
}

function showOutput(t) {
  $("#output-title").textContent = `${t.title}: output`;
  $("#output-text").textContent = t.output || "(no output)";
  $("#output").showModal();
}

function jobRow(j) {
  const p = j.progress;
  const finished = p.Running + p.Waiting === 0;
  const group = j.kind === "group";
  const label = group
    ? (p.Running === p.Total ? "Running" : p.Failed ? "Problem" : "Starting")
    : (finished ? (p.Failed ? `Finished, ${p.Failed} failed` : "Finished") : `${p.Done} of ${p.Total} done`);
  const cls = p.Failed ? "bad" : finished || (group && p.Running === p.Total) ? "ok" : "accent";
  return h("div", { class: "item" },
    h("div", { class: "emoji" }, group ? "🧩" : "🧮"),
    h("div", { class: "grow" },
      h("div", { class: "title" }, j.title),
      h("div", { class: "msg" }, group
        ? `Group of ${j.shards} worker${j.shards === 1 ? "" : "s"} plus a head, on ${j.machines} machine${j.machines === 1 ? "" : "s"}`
        : `${j.shards} pieces` + (j.verify ? ` · each checked on ${j.verify} machines` : "") + ` · ${p.Running} running on ${j.machines} machine${j.machines === 1 ? "" : "s"} · ${p.Waiting} waiting` + (p.Saved ? ` · ${p.Saved} with saved output` : "")),
      !group && h("div", { class: "bar small" }, h("i", { style: { width: (p.Total ? (p.Done / p.Total) * 100 : 0) + "%" } }))),
    h("span", { class: "chip " + cls }, label),
    !group && p.Saved > 0 && h("button", { class: "ghost", onclick: () => openCollect(j) }, "Collect results"),
    h("button", {
      class: "ghost" + (finished ? "" : " danger"),
      onclick: async () => { try { await api(`/api/jobs/${j.id}/stop`, {}); refresh(); } catch (e) { toast(e.message); } },
    }, finished ? "Clear" : "Stop"));
}

async function openCollect(j) {
  if (!storageState) await loadStorage();
  $("#cl-what").textContent = `${j.title}: ${j.progress.Saved} piece(s) with saved output`;
  const owners = (storageState?.machines || []).filter((m) => m.owner && m.online);
  fill($("#cl-machine"), owners.map((m) => h("option", { value: m.id }, m.name)));
  $("#cl-dest").value = "";
  $("#cl-dest").placeholder = "/home/you/results or C:\\Users\\you\\results";
  $("#cl-error").textContent = "";
  $("#collect").dataset.job = j.id;
  $("#collect").showModal();
}

$("#cl-go").addEventListener("click", async (e) => {
  e.preventDefault();
  if (!$("#collect-form").reportValidity()) return;
  try {
    const r = await api(`/api/jobs/${$("#collect").dataset.job}/collect`, { agentId: $("#cl-machine").value, dest: $("#cl-dest").value.trim() });
    $("#collect").close();
    toast(`Collecting ${r.restores} piece(s). Progress is on the Storage tab.`);
  } catch (err) { $("#cl-error").textContent = err.message; }
});

function renderApps(s) {
  const list = [...(s.jobs || []).map(jobRow), ...(s.tasks || []).map(taskRow)];
  fill($("#apps"), list.length ? list : h("div", { class: "panel empty" },
    h("div", { class: "big" }, "📭"), h("p", {}, "No apps yet. Deploy something from the marketplace."),
    h("button", { class: "primary", onclick: () => show("market") }, "Open marketplace")));
}

async function renderNodes(s) {
  const c = s.cluster || {};
  const sb = $("#standby");
  sb.classList.toggle("hidden", !c.enabled || c.leading);
  if (c.enabled && !c.leading) {
    const leader = (c.members || []).find((m) => m.leader);
    $("#standby-text").textContent = leader
      ? `${leader.name} is leading the pool. Its dashboard shows live work and accepts changes; this page is read-only.`
      : "The nodes are electing a leader. This takes a few seconds.";
  }
  const box = $("#nodes");
  if (!c.enabled) {
    fill(box, h("p", { class: "muted" }, "This is a single node. To keep the pool running if this machine fails, run 2 more nodes as a cluster (3 in total, so any one can fail): restart this node with "),
      h("pre", {}, h("code", {}, "sys1 --cluster")),
      h("p", { class: "muted small" }, "then add the others from here. See docs/CLUSTER.md."));
    return;
  }
  const rows = (c.members || []).map((m) => h("div", { class: "item" },
    h("div", { class: "emoji" }, m.leader ? "👑" : "🖥️"),
    h("div", { class: "grow" },
      h("div", { class: "title" }, m.name + (m.id === c.self ? " (this node)" : "")),
      h("div", { class: "msg mono" }, (m.apiAddrs || []).join(", "))),
    h("span", { class: "chip " + (m.leader ? "ok" : m.voter ? "accent" : "warn") }, m.leader ? "Leading" : m.voter ? "Standby" : "Joining")));
  if (box.dataset.code === undefined && c.leading) {
    box.dataset.code = "";
    try { box.dataset.code = (await api("/api/cluster/code")).code; } catch {}
  }
  const code = box.dataset.code;
  fill(box, rows,
    c.leading && code && h("p", { class: "muted small" }, "To add another node, run this on it, then start it with sys1 --cluster:"),
    c.leading && code && h("pre", {}, h("code", {}, "sys1 cluster join " + code)));
}

function renderAdd(s) {
  renderNodes(s);
  const input = $("#joincode");
  if (input.value !== s.joinCode) input.value = s.joinCode;
  const own = $("#ownercode");
  if (own.value !== s.ownerJoinCode) own.value = s.ownerJoinCode;
  fill($("#addrs"), (s.addrs || []).map((a) => h("li", {}, a)));
  const net = s.internet || {};
  const box = $("#internet");
  if (document.activeElement !== box) box.checked = !!net.on;
  $("#inet-port").textContent = (s.addrs?.[0] || ":7443").split(":").pop();
  const st = $("#inet-status");
  st.className = "small " + (net.working && !net.cgnat ? "ok-text" : net.on && net.problem ? "bad-text" : "muted");
  st.textContent = !net.on ? "Off. Machines join over your local network or a VPN." :
    net.working && !net.cgnat ? `On: your router (${net.method}) forwards the port. The public address is in the join code.` :
    net.problem || "Working on it…";
  $("#fp").textContent = s.fingerprint.match(/.{1,4}/g).join(" ");
  $("#cli").textContent = `sys0 join ${s.ownerJoinCode}`;
}

function renderMarket() {
  const cats = ["All", ...new Set(catalog.map((p) => p.category))];
  fill($("#filters"), cats.map((c) => h("button", { class: c === category ? "active" : "", onclick: () => { category = c; renderMarket(); } }, c)));
  const list = catalog.filter((p) => category === "All" || p.category === category);
  fill($("#market"), list.map((p) => h("div", { class: "card" },
    h("div", { class: "icon" }, p.icon || "📦"),
    h("h3", {}, p.title),
    h("div", { class: "meta" }, `${p.category} · v${p.version}${p.author ? " · by " + p.author : ""}`),
    h("p", {}, p.description),
    h("div", { class: "chips" },
      p.verified ? h("span", { class: "chip ok" }, p.source === "builtin" ? "Built in" : "Verified signature") : h("span", { class: "chip bad", title: p.problem }, "Unverified"),
      p.compatible ? h("span", { class: "chip ok" }, "Runs on your pool") : h("span", { class: "chip warn" }, p.reason)),
    h("button", { class: "primary", disabled: !(p.verified && p.compatible), onclick: () => openDeploy(p) }, "Deploy"))));
}

async function loadCatalog() {
  try { catalog = await api("/api/catalog"); renderMarket(); } catch {}
}

function openDeploy(p) {
  $("#deploy-title").textContent = `${p.icon || ""} ${p.title}`;
  $("#deploy-desc").textContent = p.description;
  fill($("#deploy-perms"), p.permissions.map((x) => h("li", {}, x)));
  fill($("#deploy-inputs"), (p.inputs || []).map((i) => h("label", { class: "field" }, i.label,
    h("input", { name: i.name, value: i.default || "", placeholder: i.example || null, type: i.secret ? "password" : "text", autocomplete: "off", required: !i.default && !i.optional }))));
  $("#deploy-error").textContent = "";
  const dlg = $("#deploy");
  dlg.dataset.pkg = p.name;
  dlg.showModal();
}

$("#deploy-go").addEventListener("click", async (e) => {
  e.preventDefault();
  const form = $("#deploy-form");
  if (!form.reportValidity()) return;
  const inputs = {};
  form.querySelectorAll("#deploy-inputs input").forEach((i) => (inputs[i.name] = i.value));
  try {
    await api("/api/deploy", { package: $("#deploy").dataset.pkg, inputs });
    $("#deploy").close();
    toast("Deployed. The pool is picking the best machine.");
    show("apps");
    refresh();
  } catch (err) {
    $("#deploy-error").textContent = err.message;
  }
});

document.querySelectorAll("[data-goto]").forEach((b) => b.addEventListener("click", async () => {
  if (!catalog.length) await loadCatalog();
  const p = catalog.find((x) => x.name === b.dataset.goto);
  if (!p) return;
  if (!p.compatible) { toast(p.reason); return; }
  openDeploy(p);
}));

$("#internet").addEventListener("change", async (e) => {
  try { await api("/api/internet", { on: e.target.checked }); toast(e.target.checked ? "Asking your router to open the port" : "Closing the port on your router"); refresh(); }
  catch (err) { toast(err.message); e.target.checked = !e.target.checked; }
});

for (const [btn, field] of [["#copy", "#joincode"], ["#copy-owner", "#ownercode"]]) {
  $(btn).addEventListener("click", async () => {
    try { await navigator.clipboard.writeText($(field).value); toast("Join code copied"); }
    catch { $(field).select(); toast("Press Ctrl+C / ⌘C to copy"); }
  });
}
$("#rotate-dash").addEventListener("click", async () => {
  if (!confirm("Make a new dashboard key? Other browsers and old links stop working.")) return;
  try {
    const { key } = await api("/api/dashboard-key/rotate", {});
    KEY = key;
    try { localStorage.setItem("sys1.key", key); } catch {}
    toast("New dashboard key; this browser keeps working");
  } catch (e) { toast(e.message); }
});
$("#rotate").addEventListener("click", async () => {
  if (!confirm("Make new join codes? The current ones stop working for new machines.")) return;
  try { await api("/api/joincode/rotate", {}); toast("New join code ready"); refresh(); } catch (e) { toast(e.message); }
});

// ---- storage ----
let storageState = null;

async function loadStorage() {
  try { storageState = await api("/api/storage"); renderStorage(storageState); } catch {}
}

function renderStorage(s) {
  s.machines = s.machines || [];
  s.backups = (s.backups || []).map((b) => ({ ...b, snapshots: b.snapshots || [] }));
  s.restores = s.restores || [];
  const hl = s.health;
  const lent = s.machines.reduce((a, m) => a + (m.online ? m.quota : 0), 0);
  const pct = hl.chunks ? Math.round((hl.healthy / hl.chunks) * 100) : 100;
  fill($("#storage-stats"),
    stat("Backed up", bytes(s.backedUp), `${s.backups.length} folder${s.backups.length === 1 ? "" : "s"}`),
    stat("App data", bytes(s.appData), "moves with its apps"),
    stat("Fully protected", hl.chunks ? pct + "%" : "–", hl.chunks ? `${hl.healthy} of ${hl.chunks} chunks have ${s.copies}+ copies` : "nothing stored yet"),
    stat("Space lent", bytes(lent), `${bytes(hl.storedBytes)} in use, every copy counted`));

  const owners = s.machines.filter((m) => m.owner);
  const sel = $("#bk-machine");
  const keep = sel.value;
  fill(sel, owners.length ? owners.map((m) => h("option", { value: m.id }, m.name + (m.online ? "" : " (offline)"))) : h("option", { value: "" }, "No machines of yours yet"));
  if (keep) sel.value = keep;

  fill($("#backups"), s.backups.length ? s.backups.map((b) => {
    const last = b.snapshots[0];
    return h("div", { class: "item" },
      h("div", { class: "emoji" }, "🗂️"),
      h("div", { class: "grow" },
        h("div", { class: "title mono" }, b.path),
        h("div", { class: "msg" }, `${b.machine || "removed machine"} · ${b.copies} copies · every ${b.intervalMinutes >= 60 ? b.intervalMinutes / 60 + "h" : b.intervalMinutes + "m"} · ` +
          (last ? `last backup ${ago(last.created)}, ${bytes(last.size)}, ${last.files} files, ${b.snapshots.length} version${b.snapshots.length === 1 ? "" : "s"}` : "first backup pending"))),
      h("button", { class: "ghost", onclick: async () => { try { await api(`/api/backups/${b.id}/run`, {}); toast("Backing up now"); } catch (e) { toast(e.message); } } }, "Back up now"),
      h("button", { class: "ghost", disabled: !last, onclick: () => openRestore(b) }, "Restore"),
      h("button", { class: "ghost danger", onclick: async () => {
        if (!confirm(`Stop backing up ${b.path} and delete its ${b.snapshots.length} saved versions from the pool?`)) return;
        try { await api(`/api/backups/${b.id}/remove`, {}); toast("Backup removed"); loadStorage(); } catch (e) { toast(e.message); }
      } }, "Remove"));
  }) : h("p", { class: "muted" }, "No backups yet. Pick a folder on one of your machines below."));

  fill($("#storage-machines"), s.machines.length ? s.machines.map((m) =>
    meter(`${m.name}${m.online ? "" : " (offline)"}`, m.used, m.quota || 1, m.quota ? `${bytes(m.used)} of ${bytes(m.quota)}` : "not lending space")) :
    h("p", { class: "muted" }, "No machines yet."));

  fill($("#restores"), s.restores.length ? s.restores.map((r) => h("div", { class: "item" },
    h("div", { class: "grow" },
      h("div", { class: "title mono" }, r.dest),
      h("div", { class: "msg" }, `${r.machine} · ${ago(r.created)}` + (r.message ? " · " + r.message : "") + (r.files ? ` · ${r.files} items` : ""))),
    h("span", { class: "chip " + ({ done: "ok", failed: "bad" }[r.state] || "warn") }, { done: "Restored", failed: "Failed", pending: "Restoring" }[r.state] || r.state))) :
    h("p", { class: "muted" }, "Nothing restored yet."));
}

function openRestore(b) {
  $("#rs-from").textContent = `${b.path} on ${b.machine || "a removed machine"}`;
  fill($("#rs-version"), b.snapshots.map((s) => h("option", { value: s.root }, `${new Date(s.created).toLocaleString()} · ${bytes(s.size)} · ${s.files} files`)));
  const owners = storageState.machines.filter((m) => m.owner && m.online);
  fill($("#rs-machine"), owners.map((m) => h("option", { value: m.id }, m.name)));
  const d = new Date();
  const stamp = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
  $("#rs-dest").value = b.path.replace(/[\\/]+$/, "") + "-restored-" + stamp;
  $("#rs-error").textContent = "";
  $("#restore").showModal();
}

$("#rs-go").addEventListener("click", async (e) => {
  e.preventDefault();
  if (!$("#restore-form").reportValidity()) return;
  try {
    await api("/api/restores", { root: $("#rs-version").value, agentId: $("#rs-machine").value, dest: $("#rs-dest").value.trim() });
    $("#restore").close();
    toast("Restore started");
    loadStorage();
  } catch (err) { $("#rs-error").textContent = err.message; }
});

$("#bk-add").addEventListener("click", async (e) => {
  e.preventDefault();
  if (!$("#backup-form").reportValidity()) return;
  try {
    await api("/api/backups", {
      agentId: $("#bk-machine").value, path: $("#bk-path").value.trim(),
      copies: Number($("#bk-copies").value), intervalMinutes: Number($("#bk-every").value), keep: Number($("#bk-keep").value),
    });
    $("#bk-path").value = "";
    $("#bk-error").textContent = "";
    toast("Backup added. The first copy starts within a minute.");
    loadStorage();
  } catch (err) { $("#bk-error").textContent = err.message; }
});

$("#show-key").addEventListener("click", async () => {
  const input = $("#recovery");
  if (input.dataset.shown) {
    try { await navigator.clipboard.writeText(input.value); toast("Recovery key copied"); } catch { input.select(); }
    return;
  }
  try {
    const { key } = await api("/api/storage/recovery-key");
    input.value = key;
    input.dataset.shown = "1";
    $("#show-key").textContent = "Copy";
  } catch (err) { toast(err.message); }
});

// ---- crash reports ----
async function loadCrashes() {
  let c;
  try { c = await api("/api/crashes"); } catch { return; }
  const sw = $("#crash-sending");
  sw.checked = c.sending;
  sw.disabled = !c.automatic;
  $("#crash-sending-text").textContent = c.automatic
    ? "Send new crashes to the sys1 project, which files them as GitHub issues"
    : "Automatic sending isn't available yet: use \u201cReport on GitHub\u201d to file a crash yourself";
  const open = new Set([...document.querySelectorAll("#crashes details[open]")].map((d) => d.dataset.sig));
  fill($("#crashes"), c.crashes.length ? c.crashes.map((e) =>
    h("details", { class: "crash", "data-sig": e.signature, open: open.has(e.signature) },
      h("summary", {},
        h("strong", {}, `${e.app} ${e.version}`), ` on ${OS_NAME[e.os] || e.os} ${e.arch} · `,
        e.count > 1 ? `${e.count} times, last ` : "", ago(e.last), " · ", e.message || "crash"),
      h("pre", { class: "small" }, e.message + "\n\n" + (e.frames || []).join("\n")),
      h("div", { class: "actions" },
        e.issue ? h("a", { class: "button ghost", href: e.issue, target: "_blank", rel: "noopener" }, "Open the issue")
          : e.sentCount ? h("span", { class: "chip ok" }, "Sent")
          : h("a", { class: "button ghost", href: e.link, target: "_blank", rel: "noopener" }, "Report on GitHub"),
        h("button", { class: "ghost", onclick: async () => {
          try { await api(`/api/crashes/${e.signature}/forget`, {}); loadCrashes(); } catch (err) { toast(err.message); }
        } }, "Dismiss"))))
    : h("p", { class: "muted" }, "No crashes. 🎉"));
}
$("#crash-sending").addEventListener("change", async (ev) => {
  try { await api("/api/crashes/sending", { on: ev.target.checked }); toast(ev.target.checked ? "Crash reports on" : "Crash reports off"); }
  catch (err) { toast(err.message); }
});

// ---- live updates ----
async function refresh() {
  try {
    state = await api("/api/state");
    locked(false);
    $("#live").className = "live ok";
    $("#live-text").textContent = "Live";
    $("#version").textContent = /^\d/.test(state.version) ? "v" + state.version : state.version + " build";
    renderOverview(state);
    renderMachines(state);
    renderApps(state);
    renderAdd(state);
    if (current === "storage") loadStorage();
  } catch (e) {
    if (e.message === "locked") {
      $("#live").className = "live bad";
      $("#live-text").textContent = "Locked";
    } else {
      $("#live").className = "live bad";
      $("#live-text").textContent = "Node unreachable";
    }
  }
}

try { const t = localStorage.getItem("sys1.tab"); if (t && document.getElementById("tab-" + t)) show(t); } catch {}
loadCatalog();
refresh();
setInterval(refresh, 2000);
setInterval(() => { if (current === "market") loadCatalog(); if (current === "machines") loadCrashes(); }, 10000);
