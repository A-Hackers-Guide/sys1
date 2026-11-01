// node --test intake/
import test from "node:test";
import assert from "node:assert/strict";
import { clean, scrub, body } from "./worker.js";

const good = {
  app: "sys0", version: "0.8.0", os: "linux", arch: "amd64", signature: "0123456789ab", count: 2, versions: ["0.8.0"],
  message: "panic: dial 10.0.0.2:7443 for bob@example.com", frames: ["github.com/A-Hackers-Guide/sys1/internal/agent.(*Agent).Run (github.com/A-Hackers-Guide/sys1/internal/agent/agent.go:12)"],
};

test("accepts a crash and scrubs it again", () => {
  const r = clean(good);
  assert.equal(r.message, "panic: dial <ip> for <email>");
  assert.ok(!body(r).includes("@"));
});

test("refuses anything else", () => {
  for (const bad of [
    { ...good, app: "evil" }, { ...good, signature: "xyz" }, { ...good, version: "1 2" },
    { ...good, frames: ["hello @everyone, buy crypto"] }, { ...good, frames: Array(33).fill(good.frames[0]) }, null, "x",
  ]) assert.equal(clean(bad), null);
});

test("removes mentions, paths and links", () => {
  assert.equal(scrub("open /home/alice/x: @octocat see https://evil.example"), "open <path>: (at)octocat see <url>");
});

test("computes the same signature as sys1", async () => {
  // The same value internal/crash/sig_test.go expects from Go.
  const { signature } = await import("./worker.js");
  const r = clean(good);
  r.signature = await signature(r);
  assert.equal(r.signature, "8b49c6d1a16e");
});

test("can't break out of the code block or link anywhere", () => {
  assert.equal(clean({ ...good, frames: ["[Download](//evil.example/x) (a.go:1)"] }), null);
  const r = clean({ ...good, message: "``` [click](https://x) " });
  assert.ok(!r.message.includes("`") && !r.message.includes("]("));
});
