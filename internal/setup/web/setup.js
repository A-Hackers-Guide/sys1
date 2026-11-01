"use strict";
const $ = (s) => document.querySelector(s);
const steps = [...document.querySelectorAll(".step")];
let at = 0;

function go(i) {
  at = i;
  steps.forEach((s) => s.classList.toggle("hidden", Number(s.dataset.step) !== i));
  $("#bar").style.width = (i / (steps.length - 1)) * 100 + "%";
}

function bytes(b) {
  const u = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(u.length - 1, Math.floor(Math.log(b || 1) / Math.log(1024)));
  return (b / 1024 ** i).toFixed(i > 2 ? 1 : 0) + " " + u[i];
}

const OS = { darwin: "macOS", windows: "Windows", linux: "Linux", freebsd: "FreeBSD", android: "Android" };

fetch("api/info").then((r) => r.json()).then((info) => {
  const r = info.resources;
  $("#ver").textContent = info.version;
  $("#name").value = r.hostname || "";
  const freeGB = Math.floor((r.diskFree || 0) / 2 ** 30);
  $("#disk").max = Math.max(0, Math.min(2000, freeGB - 5));
  if (Number($("#disk").value) > Number($("#disk").max)) { $("#disk").value = $("#disk").max; $("#diskv").textContent = $("#disk").max + " GB"; }
  $("#machine").textContent = `This machine: ${OS[r.os] || r.os} on ${r.arch}, ${r.cpuCores} cores, ${bytes(r.memTotal)} memory, ${bytes(r.diskFree)} free disk` +
    (r.runtimes && r.runtimes.length ? `, ${r.runtimes.join(" + ")} ready` : ". Tip: install Docker or Podman so it can run containers.");
  if (info.configured) $("#machine").textContent += " · Already joined a pool; continuing will switch it to a new one.";
});

document.querySelectorAll("[data-next]").forEach((b) => b.addEventListener("click", () => {
  if (at === 1) {
    const code = $("#code").value.trim();
    if (!code.startsWith("sys1-join:")) {
      $("#err1").textContent = "That doesn't look like a join code. It starts with sys1-join:";
      return;
    }
    $("#err1").textContent = "";
  }
  go(at + 1);
}));
document.querySelectorAll("[data-back]").forEach((b) => b.addEventListener("click", () => go(at - 1)));
$("#max").addEventListener("input", () => ($("#maxv").textContent = $("#max").value));
$("#disk").addEventListener("input", () => {
  const v = Number($("#disk").value);
  $("#diskv").textContent = v === 0 ? "none" : v + " GB";
});

$("#finish").addEventListener("click", async () => {
  go(3);
  try {
    const r = await fetch("api/finish", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        joinCode: $("#code").value.trim(),
        name: $("#name").value.trim(),
        trust: document.querySelector("input[name=trust]:checked").value,
        maxTasks: Number($("#max").value),
        storageGB: Number($("#disk").value),
      }),
    });
    const res = await r.json();
    if (res.ok) { go(4); return; }
    go(2);
    $("#err2").textContent = res.message;
  } catch {
    go(2);
    $("#err2").textContent = "Lost contact with the installer. Run sys0 setup again.";
  }
});

go(0);
