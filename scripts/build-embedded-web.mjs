import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { brotliCompressSync, constants, gzipSync } from "node:zlib";

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");

// The parent and Lynx repositories have independent tags and commit identities.
export function frontendEnvironment(env, clientVersion, clientCommit) {
  const serverVersion = (env.VERSION ?? "dev").replace(/^v/, "");
  if (serverVersion !== "dev" && !/^\d+\.\d+\.\d+(?:-[\w.-]+)?$/.test(serverVersion)) {
    throw new Error(`Invalid server version: ${serverVersion}`);
  }
  return {
    ...env,
    GITHUB_REF: serverVersion === "dev" ? "refs/heads/main" : `refs/tags/v${clientVersion}`,
    GITHUB_SHA: clientCommit,
    GITHUB_OUTPUT: "",
    SONGLOFT_TEST_BRIDGE: "false",
    // The client prepares fresh metadata before compiling; ignore a caller's old file.
    SONGLOFT_BUILD_METADATA: "",
  };
}

export function precompress(directory) {
  let count = 0;
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      count += precompress(path);
    } else if (/\.(?:bundle|js|mjs|css|html|json|svg|wasm|xml|txt|otf)$/.test(entry.name)) {
      const bytes = readFileSync(path);
      if (bytes.length < 128) continue;
      writeFileSync(`${path}.br`, brotliCompressSync(bytes, {
        params: { [constants.BROTLI_PARAM_QUALITY]: 8 },
      }));
      writeFileSync(`${path}.gz`, gzipSync(bytes, { level: 9 }));
      count++;
    }
  }
  return count;
}

export function verifyWebBundle(path) {
  const bytes = readFileSync(path);
  if (!bytes.length) throw new Error("Empty Web bundle");
  for (const marker of ["TestBridge.eval", "__E2E_PLAYER_STORE__", "__E2E_AUTH_STORE__"]) {
    if (bytes.includes(marker)) throw new Error(`Web bundle contains test evaluator: ${marker}`);
  }
}

export function buildEmbeddedWeb({ root = repoRoot, outputRoot = "clients/player-build", env = process.env } = {}) {
  const client = join(root, "clients/player-lynx");
  if (!existsSync(join(client, "package.json"))) {
    throw new Error("Initialize clients/player-lynx first: git submodule update --init --recursive -- clients/player-lynx");
  }
  const destination = resolve(root, outputRoot, "web-embedded");
  // Output must never clear source code or a directory containing it.
  if (destination === root || root.startsWith(destination + "/") ||
      destination === client || client.startsWith(destination + "/")) {
    throw new Error("The embedded output directory must not contain repository source");
  }
  const version = JSON.parse(readFileSync(join(client, "package.json"), "utf8")).version;
  const commit = execFileSync("git", ["rev-parse", "HEAD"], { cwd: client, encoding: "utf8" }).trim();
  const preparedEnv = frontendEnvironment(env, version, commit);
  const run = (command, args, runEnv = preparedEnv) => execFileSync(command, args, {
    cwd: client, env: runEnv, stdio: "inherit",
  });
  run("pnpm", ["install", "--frozen-lockfile"]);
  // pnpm installations may disable lifecycle scripts locally. The production
  // engine patches are part of the product, so apply this idempotent step explicitly.
  run("pnpm", ["run", "postinstall"]);
  run(process.execPath, ["scripts/prepare-build.mjs"]);
  const metadataPath = join(client, ".build/version.json");
  const buildEnv = { ...preparedEnv, SONGLOFT_BUILD_METADATA: metadataPath };
  run("pnpm", ["exec", "rspeedy", "build", "--environment", "web"], buildEnv);
  verifyWebBundle(join(client, "dist/web/main.web.bundle"));
  const copyArgs = ["scripts/copy-bundle-web.mjs", "--embedded"];
  // The client's default path supports replacing legacy Flutter output. Custom
  // paths retain its marker-based protection against deleting unrelated files.
  if (destination !== join(root, "clients/player-build/web-embedded")) {
    copyArgs.push("--out-dir", destination);
  }
  run(process.execPath, copyArgs, buildEnv);
  mkdirSync(destination, { recursive: true });
  writeFileSync(join(destination, "embedded-build.json"), JSON.stringify({
    frontend: "lynx",
    server_version: env.VERSION ?? "dev",
    server_commit: env.GIT_COMMIT ?? "unknown",
    server_build_time: env.BUILD_TIME ?? "unknown",
    client: JSON.parse(readFileSync(metadataPath, "utf8")),
  }, null, 2) + "\n");
  const count = precompress(destination);
  console.log(`Embedded Lynx Web: ${destination} (${count} files precompressed with Brotli/gzip)`);
  return destination;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    buildEmbeddedWeb({ outputRoot: process.argv[2] ?? "clients/player-build" });
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
