// sys1 dashboard. Plain JavaScript, no build step, no third-party code:
// everything the node serves is in this binary and covered by its CSP.
"use strict";

const $ = (sel) => document.querySelector(sel);

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
  const opts = body === undefined ? {} : {
    method: "POST", headers: { "Content-Type": "application/json", "X-Sys1": "1" }, body: JSON.stringify(body),
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
  const recent = (s.tasks || []).slice(0, 6);
  fill($("#recent"), recent.length ? recent.map(taskRow) : h("div", { class: "empty" },
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
        r.onBattery && h("span", { class: "chip warn" }, "On battery, paused")),
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
    h("span", { class: "chip " + cls }, label),
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

function renderApps(s) {
  const list = s.tasks || [];
  fill($("#apps"), list.length ? list.map(taskRow) : h("div", { class: "panel empty" },
    h("div", { class: "big" }, "📭"), h("p", {}, "No apps yet. Deploy something from the marketplace."),
    h("button", { class: "primary", onclick: () => show("market") }, "Open marketplace")));
}

function renderAdd(s) {
  const input = $("#joincode");
  if (input.value !== s.joinCode) input.value = s.joinCode;
  $("#addr").textContent = s.publicAddr;
  $("#fp").textContent = s.fingerprint.match(/.{1,4}/g).join(" ");
  $("#cli").textContent = `sys0 join ${s.joinCode}`;
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
    h("input", { name: i.name, value: i.default || "", type: i.secret ? "password" : "text", autocomplete: "off", required: !i.default }))));
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

$("#copy").addEventListener("click", async () => {
  try { await navigator.clipboard.writeText($("#joincode").value); toast("Join code copied"); }
  catch { $("#joincode").select(); toast("Press Ctrl+C / ⌘C to copy"); }
});
$("#rotate").addEventListener("click", async () => {
  if (!confirm("Make a new join code? The current one stops working for new machines.")) return;
  try { await api("/api/joincode/rotate", {}); toast("New join code ready"); refresh(); } catch (e) { toast(e.message); }
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
setInterval(() => { if (current === "market") loadCatalog(); }, 10000);
