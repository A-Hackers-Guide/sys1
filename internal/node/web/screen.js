// A VM's screen, through noVNC (bundled in ./novnc, MPL-2.0). The page
// asks the dashboard's own API where the screen is and for its password,
// so the password never appears in a URL.
import RFB from "./novnc/core/rfb.js";

const $ = (s) => document.querySelector(s);
const status = (text) => ($("#status").textContent = text);

async function main() {
  const id = new URLSearchParams(location.search).get("task");
  let key = "";
  try { key = localStorage.getItem("sys1.key") || ""; } catch {}
  const r = await fetch("/api/state", { headers: { Authorization: "Bearer " + key } });
  if (r.status === 401) { $("#title").textContent = "This dashboard is locked"; return; }
  const state = await r.json();
  const t = (state.tasks || []).find((x) => x.id === id);
  if (!t || !t.screen) {
    $("#title").textContent = "No screen";
    status("This app isn't running, or has no screen.");
    return;
  }
  $("#title").textContent = t.title;
  document.title = t.title + " · sys1";
  status(`on ${t.agent}`);
  const rfb = new RFB($("#screen"), `ws://${t.screen.host}:${t.screen.port}`, { credentials: { password: t.screen.password } });
  rfb.scaleViewport = true;
  rfb.focusOnClick = true;
  rfb.addEventListener("connect", () => status(`on ${t.agent} · connected`));
  rfb.addEventListener("disconnect", (e) => status(e.detail.clean ? "Disconnected" : "Connection lost. Is the machine reachable from this computer?"));
  rfb.addEventListener("securityfailure", () => status("The screen refused the password. Reload to try again."));
  $("#cad").addEventListener("click", () => rfb.sendCtrlAltDel());
  $("#full").addEventListener("click", () => document.documentElement.requestFullscreen?.().catch(() => {}));
}

main().catch((e) => status(e.message));
