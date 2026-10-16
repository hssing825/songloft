import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync, writeFileSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { brotliDecompressSync, gunzipSync } from "node:zlib";
import test from "node:test";
import { frontendEnvironment, precompress, verifyWebBundle } from "../build-embedded-web.mjs";

test("parent dev identity becomes the actual client SHA and never enables TestBridge", () => {
  const input = { VERSION: "dev", GITHUB_REF: "refs/tags/v2.13.3", GITHUB_SHA: "parent", GITHUB_OUTPUT: "/tmp/output", SONGLOFT_TEST_BRIDGE: "true", SONGLOFT_BUILD_METADATA: "/old.json" };
  const env = frontendEnvironment(input, "0.1.3", "client-sha");
  assert.equal(env.GITHUB_REF, "refs/heads/main");
  assert.equal(env.GITHUB_SHA, "client-sha");
  assert.equal(env.GITHUB_OUTPUT, "");
  assert.equal(env.SONGLOFT_TEST_BRIDGE, "false");
  assert.equal(env.SONGLOFT_BUILD_METADATA, "");
  assert.equal(input.GITHUB_SHA, "parent");
});

test("parent release tags do not enter the independent client's tag validator", () => {
  for (const VERSION of ["2.13.3", "v2.13.3", "2.14.0-preview.1"]) {
    const env = frontendEnvironment({ VERSION }, "0.1.3", "abcdef0");
    assert.equal(env.GITHUB_REF, "refs/tags/v0.1.3");
  }
  assert.throws(() => frontendEnvironment({ VERSION: "bad" }, "0.1.3", "abcdef0"), /Invalid server version/);
});

test("production Web output refuses test evaluators and empty artifacts", t => {
  const root = mkdtempSync(join(tmpdir(), "songloft-web-bundle-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const path = join(root, "main.web.bundle");
  for (const marker of ["TestBridge.eval", "__E2E_PLAYER_STORE__", "__E2E_AUTH_STORE__"]) {
    writeFileSync(path, `prefix ${marker} suffix`);
    assert.throws(() => verifyWebBundle(path), /test evaluator/);
  }
  writeFileSync(path, "");
  assert.throws(() => verifyWebBundle(path), /Empty/);
  writeFileSync(path, "console.log('production')");
  assert.doesNotThrow(() => verifyWebBundle(path));
});

test("Lynx bundle and engine WASM are precompressed losslessly, overwriting stale encodings", t => {
  const root = mkdtempSync(join(tmpdir(), "songloft-web-compress-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  mkdirSync(join(root, "engine"));
  const inputs = { "main.lynx.bundle": "new bundle ".repeat(100), "engine/runtime.wasm": Buffer.alloc(512, 12), "index.html": "<head></head>".repeat(30) };
  for (const [path, bytes] of Object.entries(inputs)) writeFileSync(join(root, path), bytes);
  writeFileSync(join(root, "main.lynx.bundle.br"), "stale");
  writeFileSync(join(root, "icon.png"), Buffer.alloc(256));
  assert.equal(precompress(root), 3);
  for (const [path, original] of Object.entries(inputs)) {
    const bytes = Buffer.from(original);
    assert.deepEqual(brotliDecompressSync(readFileSync(join(root, path + ".br"))), bytes);
    assert.deepEqual(gunzipSync(readFileSync(join(root, path + ".gz"))), bytes);
  }
  assert.equal(precompress(root), 3);
});
