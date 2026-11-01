// sys1 crash intake: a Cloudflare Worker that turns crash reports from
// sys1 nodes into GitHub issues on A-Hackers-Guide/sys1.
//
// It is the only place that holds a GitHub token, so no app ever carries
// one. Reports are checked against a strict shape and scrubbed again, so
// even a modified client can't post anything but a crash: an app, a
// version, an OS, a CPU, a message and Go stack frames. One issue per
// distinct crash; repeats add a short comment at most once a day.
//
// Deploy (docs/CRASHES.md):
//   npx wrangler deploy
//   npx wrangler secret put GITHUB_TOKEN   # fine-grained, Issues: read & write on A-Hackers-Guide/sys1 only

const REPO = "A-Hackers-Guide/sys1";
const PER_IP_PER_HOUR = 30;
const NEW_ISSUES_PER_HOUR = 10; // per Cloudflare location

const FRAME = /^[A-Za-z0-9_.*()\[\]{},\/-]+ \([A-Za-z0-9_.\/-]+\.(go|s):\d+\)$/;
const SAFE = /^[A-Za-z0-9._+-]{1,32}$/;
const SCRUB = [
  [/sys1-(join|cluster):[A-Za-z0-9_=-]+/g, "<code>"],
  [/[a-zA-Z][a-zA-Z0-9+.-]*:\/\/[^\s"'<>]+/g, "<url>"],
  [/[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/g, "<email>"],
  [/\[?(?:[0-9a-fA-F]{0,4}:){2,7}[0-9a-fA-F]{0,4}(?:%\w+)?\]?(?::\d+)?/g, "<ip>"],
  [/\b(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?\b/g, "<ip>"],
  [/(?:[A-Za-z]:\\|\\\\)[^\s"':]+/g, "<path>"],
  [/(^|[\s"'=(])\/[^\s"':,)]+/g, "$1<path>"],
  [/\b(?:[0-9a-fA-F]{2}[:-]){5}[0-9a-fA-F]{2}\b/g, "<mac>"],
  [/\b[A-Za-z0-9+\/_=-]{24,}\b/g, "<redacted>"],
  [/\b[a-z0-9-]+(?:\.[a-z0-9-]+)*\.(?:local|lan|home|internal|localdomain|localhost|arpa|box|corp|intranet|private|com|net|org|io|dev|app|me|de|uk|fr|nl|jp|cn|ru|br|in|au|ca|us|eu|tv|cloud|xyz)\b/gi, "<host>"],
  [/@/g, "(at)"], // no @mentions in issues
  [/`/g, "'"], // can't close the code block it's shown in
  [/\]\(/g, "] ("], // no markdown links
];

export function scrub(s) {
  for (const [re, put] of SCRUB) s = s.replace(re, put);
  s = s.replace(/\s+/g, " ").trim();
  return s.length > 400 ? s.slice(0, 400) + "…" : s;
}

// clean returns the report in the only shape the intake accepts, or null.
export function clean(r) {
  if (!r || typeof r !== "object") return null;
  if (r.app !== "sys0" && r.app !== "sys1") return null;
  if (typeof r.signature !== "string" || !/^[0-9a-f]{12}$/.test(r.signature)) return null;
  for (const k of ["version", "os", "arch"]) if (typeof r[k] !== "string" || !SAFE.test(r[k])) return null;
  if (!Array.isArray(r.frames) || r.frames.length > 32) return null;
  for (const f of r.frames) {
    if (typeof f !== "string" || f.length > 300 || !FRAME.test(f) || f.includes("](") || f.includes("//")) return null;
  }
  const versions = Array.isArray(r.versions) ? r.versions.filter((v) => typeof v === "string" && SAFE.test(v)).slice(0, 20) : [];
  const count = Number.isInteger(r.count) && r.count > 0 ? Math.min(r.count, 1e6) : 1;
  return {
    app: r.app, version: r.version, os: r.os, arch: r.arch, signature: r.signature,
    message: scrub(typeof r.message === "string" ? r.message.slice(0, 2000) : ""),
    frames: r.frames, versions, count,
  };
}

// signature names the bug by the functions it crashed in, exactly as
// sys1 computes it (internal/crash), so a sender can't pick its own.
export async function signature(r) {
  const names = [];
  for (const f of r.frames) {
    const name = f.split(" (")[0];
    if (name.startsWith("runtime.") || name.startsWith("panic(")) continue;
    names.push(name);
    if (names.length === 6) break;
  }
  let text = r.app;
  for (const n of names) text += "\n" + n;
  if (!names.length) text += "\n" + r.message.replace(/\d+/g, "N");
  const hash = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text));
  return [...new Uint8Array(hash)].map((b) => b.toString(16).padStart(2, "0")).join("").slice(0, 12);
}

export function title(r) {
  const m = r.message.length > 80 ? r.message.slice(0, 80) + "…" : r.message;
  return `crash ${r.signature}: ${m || "crash"}`;
}

export function body(r) {
  return [
    `**${r.app} ${r.version}** on ${r.os}/${r.arch} crashed` +
      (r.count > 1 ? ` (${r.count} times in one pool; versions ${r.versions.join(", ")})` : "") + ".",
    "",
    "```",
    r.message,
    "",
    ...r.frames,
    "```",
    "",
    `Signature \`${r.signature}\`. Reported automatically by a sys1 node; scrubbed of names, addresses and paths. See docs/CRASHES.md.`,
  ].join("\n");
}

async function gh(env, method, path, payload) {
  const res = await fetch("https://api.github.com" + path, {
    method,
    headers: {
      Authorization: "Bearer " + env.GITHUB_TOKEN,
      Accept: "application/vnd.github+json",
      "User-Agent": "sys1-crash-intake",
      "X-GitHub-Api-Version": "2022-11-28",
      ...(payload ? { "Content-Type": "application/json" } : {}),
    },
    body: payload ? JSON.stringify(payload) : undefined,
  });
  if (!res.ok) throw new Error(`GitHub ${method} ${path}: ${res.status}`);
  return res.json();
}

// once reports whether key hasn't been seen within ttl seconds, and
// remembers it. The Cache API is per data center, which is enough to
// keep floods and repeats down.
async function once(key, ttl) {
  const cache = caches.default;
  const url = new Request("https://intake.invalid/" + encodeURIComponent(key));
  if (await cache.match(url)) return false;
  await cache.put(url, new Response("1", { headers: { "Cache-Control": `max-age=${ttl}` } }));
  return true;
}

async function underLimit(ip, max = PER_IP_PER_HOUR) {
  const hour = Math.floor(Date.now() / 3600e3);
  const cache = caches.default;
  const key = new Request(`https://intake.invalid/rate/${encodeURIComponent(ip)}/${hour}`);
  const hit = await cache.match(key);
  const n = hit ? parseInt(await hit.text(), 10) || 0 : 0;
  if (n >= max) return false;
  await cache.put(key, new Response(String(n + 1), { headers: { "Cache-Control": "max-age=3600" } }));
  return true;
}

const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });

export default {
  async fetch(req, env) {
    if (req.method !== "POST") return json({ error: "POST a crash report" }, 405);
    if (!env.GITHUB_TOKEN) return json({ error: "intake not configured" }, 503);
    const ip = req.headers.get("CF-Connecting-IP") || "unknown";
    if (!(await underLimit(ip))) return json({ error: "too many reports" }, 429);
    const text = await req.text();
    if (text.length > 64 * 1024) return json({ error: "too large" }, 413);
    let r;
    try { r = clean(JSON.parse(text)); } catch { r = null; }
    if (!r) return json({ error: "not a crash report" }, 400);
    r.signature = await signature(r);

    const q = encodeURIComponent(`repo:${REPO} is:issue in:title "crash ${r.signature}"`);
    const found = await gh(env, "GET", `/search/issues?q=${q}&per_page=1`);
    const existing = found.items && found.items[0];
    if (existing) {
      if (await once(`comment/${r.signature}`, 86400)) {
        await gh(env, "POST", `/repos/${REPO}/issues/${existing.number}/comments`, {
          body: `Seen again: ${r.app} ${r.version} on ${r.os}/${r.arch}` + (r.count > 1 ? `, ${r.count} times in one pool` : "") + ".",
        });
      }
      return json({ issue: existing.html_url });
    }
    // Two nodes reporting the same new crash at once: only one files it.
    if (!(await once(`new/${r.signature}`, 600))) return json({ issue: "" }, 202);
    if (!(await underLimit("new-issues", NEW_ISSUES_PER_HOUR))) return json({ error: "too many new crashes; try later" }, 429);
    const issue = await gh(env, "POST", `/repos/${REPO}/issues`, { title: title(r), body: body(r), labels: ["crash"] });
    return json({ issue: issue.html_url }, 201);
  },
};
